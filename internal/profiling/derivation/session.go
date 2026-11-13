package profilederivation

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"gonum.org/v1/gonum/stat"
	"gonum.org/v1/gonum/stat/distuv"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

const levelKindSaturation = "saturation"

// SessionInput is everything a session observed, as plain values.
type SessionInput struct {
	Condition SessionCondition
	Settings  SessionSettings
	// Batches are the measured batch sizes in increasing order.
	Batches []SessionBatchInput
	// Power holds the node power samples covering the whole session.
	Power         []Sample
	PostLoadStart time.Time
	PostLoadEnd   time.Time
	Stop          *SessionStop
}

// SessionBatchInput is one fixed-batch instance's observation.
type SessionBatchInput struct {
	Batch           int
	Levels          []SessionLevelInput
	PeakMemoryBytes int64
	MemoryKind      string
	PrepareCostKey  string
	RemoveCostKey   string
}

// SessionLevelInput is the driver's row of one level and the executor's batch
// rows carrying that level's identifier.
type SessionLevelInput struct {
	Row    DriverLevelRow
	Native []NativeBatchRow
}

// NativeBatchRow is one executor batch record. A log follow reopen repeats
// rows; repeats of one batch identifier are counted once.
type NativeBatchRow struct {
	BatchID              uint64
	BatchSize            int
	StartedAt            time.Time
	FinishedAt           time.Time
	ServiceDurationNanos int64
	PrecedingIdleNanos   *int64
	// LatestEnqueuedAt is the last member's enqueue time; dispatch occupancy
	// reads it on saturation rows to prove the next batch was already formed.
	LatestEnqueuedAt time.Time
}

// DeriveSession computes the stored result of one session.
func DeriveSession(input SessionInput) (SessionResult, error) {
	settings := input.Settings
	if settings.MeterLagSeconds < 0 || settings.MinimumPowerWindowSeconds <= 0 || settings.Percentile <= 0 || settings.Percentile >= 100 {
		return SessionResult{}, errors.New("session settings need a nonnegative meter lag, a positive minimum power window and a percentile in (0, 100)")
	}
	if len(input.Batches) == 0 {
		return SessionResult{}, errors.New("session has no measured batch")
	}
	minimumWindow := seconds(settings.MinimumPowerWindowSeconds)
	if input.PostLoadEnd.Sub(input.PostLoadStart) < 2*minimumWindow {
		return SessionResult{}, fmt.Errorf("post-load window %v is shorter than two minimum power windows of %v", input.PostLoadEnd.Sub(input.PostLoadStart), minimumWindow)
	}
	power := slices.Clone(input.Power)
	sort.Slice(power, func(i, j int) bool { return power[i].ObservedAt.Before(power[j].ObservedAt) })
	// Identification tests run at the session's percentile: the fit compares a
	// residual increase with the one-degree chi-square quantile, and the
	// post-load tests compare a drop in mean watts with the one-sided normal
	// quantile times its standard error.
	confidence := settings.Percentile / 100
	threshold := distuv.ChiSquared{K: 1}.Quantile(confidence)
	normal := distuv.UnitNormal.Quantile(confidence)

	result := SessionResult{SchemaVersion: SessionResultSchemaVersion, Condition: input.Condition, Settings: settings, Stop: input.Stop}
	var executions []NativeBatchRow
	var levels []fitLevel
	for index, batchInput := range input.Batches {
		if batchInput.Batch < 1 || (index > 0 && batchInput.Batch <= input.Batches[index-1].Batch) {
			return SessionResult{}, errors.New("session batch sizes must be positive and increasing")
		}
		batch := SessionBatch{
			Batch: batchInput.Batch, PeakMemoryBytes: batchInput.PeakMemoryBytes, MemoryKind: batchInput.MemoryKind,
			PrepareCostKey: batchInput.PrepareCostKey, RemoveCostKey: batchInput.RemoveCostKey,
			ServiceNanos: []int64{}, PrecedingIdleNanos: []int64{}, DispatchOccupancyNanos: []float64{},
		}
		for _, levelInput := range batchInput.Levels {
			rows, err := distinctNativeRows(levelInput, batchInput.Batch)
			if err != nil {
				return SessionResult{}, err
			}
			executions = append(executions, rows...)
			for _, row := range rows {
				// The first execution after prepare has no preceding idle gap.
				if row.PrecedingIdleNanos != nil {
					batch.ServiceNanos = append(batch.ServiceNanos, row.ServiceDurationNanos)
					batch.PrecedingIdleNanos = append(batch.PrecedingIdleNanos, *row.PrecedingIdleNanos)
				}
			}
			if levelInput.Row.Kind == levelKindSaturation {
				batch.DispatchOccupancyNanos = append(batch.DispatchOccupancyNanos, saturationOccupancy(levelInput.Row.ID, rows, batchInput.Batch)...)
			}
			level, fitted, err := measureLevel(levelInput.Row, power, settings)
			if err != nil {
				return SessionResult{}, err
			}
			if fitted != nil {
				fitted.at = [2]int{index, len(batch.Levels)}
				levels = append(levels, *fitted)
			}
			batch.Levels = append(batch.Levels, level)
		}
		result.Batches = append(result.Batches, batch)
	}
	sort.Slice(executions, func(i, j int) bool { return executions[i].StartedAt.Before(executions[j].StartedAt) })

	postLoad := make([][2]float64, 0)
	for _, sample := range power {
		if !sample.ObservedAt.Before(input.PostLoadStart) && !sample.ObservedAt.After(input.PostLoadEnd) {
			postLoad = append(postLoad, [2]float64{sample.ObservedAt.Sub(input.PostLoadStart).Seconds(), sample.Watts})
		}
	}
	result.Power.PostLoad = postLoad
	idle, idleVariance, err := settledIdle(power, input.PostLoadEnd, minimumWindow, normal)
	if err != nil {
		return SessionResult{}, err
	}
	result.Power.IdleWatts = idle
	if idle.Status != ParameterIdentified {
		reason := "settled idle watts not identified"
		result.Power.AwakeStepWatts, result.Power.HoldSeconds = notIdentified(reason), notIdentified(reason)
		for index := range result.Batches {
			result.Batches[index].JoulesPerInput = notIdentified(reason)
		}
		return result, nil
	}
	hold, err := decayHold(power, input.PostLoadStart, *idle.Value, idleVariance, minimumWindow, seconds(settings.MeterLagSeconds), normal)
	if err != nil {
		return SessionResult{}, err
	}
	if err := fitPower(&result, executions, levels, *idle.Value, hold, threshold); err != nil {
		return SessionResult{}, err
	}
	return result, nil
}

// distinctNativeRows drops repeated batch identifiers and checks the distinct
// count against the driver's completed batches.
func distinctNativeRows(level SessionLevelInput, batch int) ([]NativeBatchRow, error) {
	seen := make(map[uint64]NativeBatchRow, len(level.Native))
	rows := make([]NativeBatchRow, 0, len(level.Native))
	for _, row := range level.Native {
		if previous, ok := seen[row.BatchID]; ok {
			if !sameNativeRow(previous, row) {
				return nil, fmt.Errorf("level %s: native batch %d repeats with different values", level.Row.ID, row.BatchID)
			}
			continue
		}
		if row.BatchSize != batch {
			return nil, fmt.Errorf("level %s: native batch %d has %d inputs on a batch %d instance", level.Row.ID, row.BatchID, row.BatchSize, batch)
		}
		if row.ServiceDurationNanos <= 0 || !row.FinishedAt.After(row.StartedAt) || (row.PrecedingIdleNanos != nil && *row.PrecedingIdleNanos < 0) {
			return nil, fmt.Errorf("level %s: native batch %d has no positive execution span", level.Row.ID, row.BatchID)
		}
		seen[row.BatchID] = row
		rows = append(rows, row)
	}
	if len(rows) != level.Row.CompletedBatches {
		return nil, fmt.Errorf("level %s: native rows %d of %d", level.Row.ID, len(rows), level.Row.CompletedBatches)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].BatchID < rows[j].BatchID })
	return rows, nil
}

func sameNativeRow(left, right NativeBatchRow) bool {
	sameIdle := (left.PrecedingIdleNanos == nil) == (right.PrecedingIdleNanos == nil) &&
		(left.PrecedingIdleNanos == nil || *left.PrecedingIdleNanos == *right.PrecedingIdleNanos)
	return sameIdle && left.BatchSize == right.BatchSize && left.StartedAt.Equal(right.StartedAt) &&
		left.FinishedAt.Equal(right.FinishedAt) && left.ServiceDurationNanos == right.ServiceDurationNanos &&
		left.LatestEnqueuedAt.Equal(right.LatestEnqueuedAt)
}

func saturationOccupancy(levelID string, rows []NativeBatchRow, batch int) []float64 {
	samples := make([]NativeServiceSample, len(rows))
	for i, row := range rows {
		samples[i] = NativeServiceSample{
			BatchID: row.BatchID, BatchSize: row.BatchSize,
			// SaturationDispatchOccupancy reads only the member count here;
			// a native row carries the count, not the member identities.
			RecordIDs:     make([]string, row.BatchSize),
			ServiceSample: firstfloor.ServiceSample{ServiceDurationNanos: row.ServiceDurationNanos, TrialID: levelID},
			StartedAt:     row.StartedAt, FinishedAt: row.FinishedAt, LatestEnqueuedAt: row.LatestEnqueuedAt,
		}
	}
	return SaturationDispatchOccupancy(samples, batch)
}

// fitLevel is one level whose power window entered the fit.
type fitLevel struct {
	// at is the level's batch index and level index in the result.
	at    [2]int
	rate  float64
	watts float64
	// variance is the squared standard error of watts.
	variance   float64
	start, end time.Time
}

func measureLevel(row DriverLevelRow, power []Sample, settings SessionSettings) (SessionLevel, *fitLevel, error) {
	duration := row.End.Sub(row.Start)
	if duration <= 0 {
		return SessionLevel{}, nil, fmt.Errorf("level %s has no positive duration", row.ID)
	}
	level := SessionLevel{DriverLevelRow: row, InputRate: float64(row.CompletedInputs) / duration.Seconds()}
	start := row.Start.Add(seconds(settings.MeterLagSeconds))
	if row.End.Sub(start) < seconds(settings.MinimumPowerWindowSeconds) {
		return level, nil, nil
	}
	watts, variance, err := windowPower(power, start, row.End)
	if err != nil {
		return SessionLevel{}, nil, fmt.Errorf("level %s: %w", row.ID, err)
	}
	level.MeanWatts = &watts
	return level, &fitLevel{rate: level.InputRate, watts: watts, variance: variance, start: start, end: row.End}, nil
}

// windowPower returns the integrated mean watts over [start, end] and the
// squared standard error of the samples inside it.
func windowPower(power []Sample, start, end time.Time) (float64, float64, error) {
	joules, err := measurePower(start, end, power)
	if err != nil {
		return 0, 0, err
	}
	var inside []float64
	for _, sample := range power {
		if !sample.ObservedAt.Before(start) && !sample.ObservedAt.After(end) {
			inside = append(inside, sample.Watts)
		}
	}
	return joules / end.Sub(start).Seconds(), stat.Variance(inside, nil) / float64(len(inside)), nil
}

// settledIdle is the mean of the final minimum power window of the post-load
// observation. Power relaxes downward after load, so it has not settled when
// that mean lies below the mean of the window before it by more than the
// one-sided noise bound. A final window above the one before is background
// activity: genesis2 idles with a 30 s cycle that alternates 15 s windows by
// up to 4.5 W.
func settledIdle(power []Sample, end time.Time, window time.Duration, normal float64) (SessionParameter, float64, error) {
	tail, tailVariance, err := windowPower(power, end.Add(-window), end)
	if err != nil {
		return SessionParameter{}, 0, fmt.Errorf("post-load tail: %w", err)
	}
	before, beforeVariance, err := windowPower(power, end.Add(-2*window), end.Add(-window))
	if err != nil {
		return SessionParameter{}, 0, fmt.Errorf("post-load tail: %w", err)
	}
	if before-tail > normal*math.Sqrt(tailVariance+beforeVariance) {
		return notIdentified(fmt.Sprintf("post-load power has not settled: final %v mean %.3f W, preceding %.3f W", window, tail, before)), 0, nil
	}
	return SessionParameter{Status: ParameterIdentified, Value: &tail}, tailVariance, nil
}

// decayHold reads the hold time from the post-load drop: the first sample at
// or below the midpoint between the loaded and the settled level, less the
// meter lag. It returns nil when the drop is within the noise or the hold is
// within the meter lag; the fit then searches the hold time.
func decayHold(power []Sample, loadEnd time.Time, idle, idleVariance float64, window, lag time.Duration, normal float64) (*float64, error) {
	loaded, loadedVariance, err := windowPower(power, loadEnd.Add(-window), loadEnd)
	if err != nil {
		return nil, fmt.Errorf("loaded power before post-load: %w", err)
	}
	if loaded-idle <= normal*math.Sqrt(loadedVariance+idleVariance) {
		return nil, nil
	}
	for _, sample := range power {
		if sample.ObservedAt.Before(loadEnd) || sample.Watts > (loaded+idle)/2 {
			continue
		}
		hold := sample.ObservedAt.Sub(loadEnd) - lag
		if hold <= lag {
			return nil, nil
		}
		value := hold.Seconds()
		return &value, nil
	}
	return nil, nil
}

func notIdentified(reason string) SessionParameter {
	return SessionParameter{Status: ParameterNotIdentified, Reason: reason}
}

func seconds(value float64) time.Duration {
	return time.Duration(math.Round(value * float64(time.Second)))
}
