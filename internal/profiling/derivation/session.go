package profilederivation

import (
	"encoding/json"
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
	// RuntimeMetadata, RuntimeDelivery and MeterSource are stored unchanged;
	// serving reads them from the profile.
	RuntimeMetadata *firstfloor.RuntimeMetadata
	RuntimeDelivery json.RawMessage
	MeterSource     json.RawMessage
	// MaxResultBytes is the byte limit the catalog reads one object with,
	// the ChillServer --max-evidence-bytes value.
	MaxResultBytes int64
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

// sessionTests holds the quantiles every statistical decision uses, all at
// the configured confidence level.
type sessionTests struct {
	// oneSided bounds a drop, twoSided the idle interval, in standard errors.
	oneSided, twoSided float64
	// confidence is the configured level; the trend test reads its Student t
	// quantile and the power fit its chi-square quantile.
	confidence float64
}

func newSessionTests(confidence float64) sessionTests {
	return sessionTests{
		oneSided:   distuv.UnitNormal.Quantile(confidence),
		twoSided:   distuv.UnitNormal.Quantile((1 + confidence) / 2),
		confidence: confidence,
	}
}

// DeriveSession computes the stored result of one session.
func DeriveSession(input SessionInput) (SessionResult, error) {
	settings := input.Settings
	if settings.MeterLagSeconds < 0 || settings.MinimumPowerWindowSeconds <= 0 || settings.ConfidenceLevel < 0.5 || settings.ConfidenceLevel >= 1 {
		return SessionResult{}, errors.New("session settings need a nonnegative meter lag, a positive minimum power window and a confidence level in [0.5, 1)")
	}
	if input.MaxResultBytes < 1 {
		return SessionResult{}, errors.New("session result byte limit must be positive")
	}
	if len(input.Batches) == 0 {
		return SessionResult{}, errors.New("session has no measured batch")
	}
	if len(input.MeterSource) == 0 || !json.Valid(input.MeterSource) || (len(input.RuntimeDelivery) > 0 && !json.Valid(input.RuntimeDelivery)) {
		return SessionResult{}, errors.New("session needs the power meter identity as JSON and a runtime delivery, when present, as JSON")
	}
	lag, minimum := seconds(settings.MeterLagSeconds), seconds(settings.MinimumPowerWindowSeconds)
	power := slices.Clone(input.Power)
	sort.Slice(power, func(i, j int) bool { return power[i].ObservedAt.Before(power[j].ObservedAt) })
	tests := newSessionTests(settings.ConfidenceLevel)

	result := SessionResult{
		SchemaVersion: SessionResultSchemaVersion, Condition: input.Condition, Settings: settings, Stop: input.Stop,
		RuntimeMetadata: input.RuntimeMetadata, RuntimeDelivery: input.RuntimeDelivery, MeterSource: input.MeterSource,
	}
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
			switch levelInput.Row.Kind {
			case LevelSaturation, LevelMid, LevelSparse:
			default:
				return SessionResult{}, fmt.Errorf("level %s has unknown kind %q", levelInput.Row.ID, levelInput.Row.Kind)
			}
			rows, err := distinctNativeRows(levelInput, batchInput.Batch)
			if err != nil {
				return SessionResult{}, err
			}
			for _, row := range rows {
				// The first execution after prepare has no preceding idle gap.
				if row.PrecedingIdleNanos != nil {
					batch.ServiceNanos = append(batch.ServiceNanos, row.ServiceDurationNanos)
					batch.PrecedingIdleNanos = append(batch.PrecedingIdleNanos, *row.PrecedingIdleNanos)
				}
			}
			level, fitted, err := measureLevel(levelInput.Row, rows, power, lag, minimum)
			if err != nil {
				return SessionResult{}, err
			}
			if levelInput.Row.Kind == LevelSaturation {
				batch.DispatchOccupancyNanos = append(batch.DispatchOccupancyNanos, saturationOccupancy(levelInput.Row.ID, rows, batchInput.Batch)...)
				rate := level.InputRate
				batch.SaturationInputRate = &rate
			}
			if fitted != nil {
				fitted.at = [2]int{index, len(batch.Levels)}
				levels = append(levels, *fitted)
			}
			batch.Levels = append(batch.Levels, level)
		}
		result.Batches = append(result.Batches, batch)
	}

	postLoad, err := analysePostLoad(power, input.PostLoadStart, input.PostLoadEnd, lag, minimum, tests)
	if err != nil {
		return SessionResult{}, err
	}
	result.Power.IdleWatts, result.Power.PostLoad = postLoad.idle, postLoad.series
	result.Power.MeterIntervalSeconds, result.Power.SettledAtSeconds = postLoad.interval, postLoad.settledAt
	if idle, ok := postLoad.idle.Value(); ok {
		fitPower(&result, levels, idle, postLoad.hold, tests)
	} else {
		reason := "settled idle watts not identified"
		result.Power.AwakeStepWatts, result.Power.HoldSeconds = notIdentified(reason), notIdentified(reason)
		for index := range result.Batches {
			result.Batches[index].JoulesPerInput = notIdentified(reason)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return SessionResult{}, err
	}
	if int64(len(encoded)) > input.MaxResultBytes {
		return SessionResult{}, fmt.Errorf("session result encodes to %d bytes, above the %d byte read limit", len(encoded), input.MaxResultBytes)
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
	variance float64
	awake    awakeWindow
}

// measureLevel measures power over the driver's window and the awake
// fraction over the level's own native executions: the two clocks are never
// compared, only their offset is recorded.
func measureLevel(row DriverLevelRow, rows []NativeBatchRow, power []Sample, lag, minimum time.Duration) (SessionLevel, *fitLevel, error) {
	duration := row.End.Sub(row.Start)
	if duration <= 0 {
		return SessionLevel{}, nil, fmt.Errorf("level %s has no positive duration", row.ID)
	}
	if (row.PredictedSeconds == nil) != (row.LimitSeconds == nil) {
		return SessionLevel{}, nil, fmt.Errorf("level %s carries one of its predicted duration and limit", row.ID)
	}
	level := SessionLevel{DriverLevelRow: row, InputRate: float64(row.CompletedInputs) / duration.Seconds()}
	if len(rows) == 0 {
		return level, nil, nil
	}
	window, first, ok := levelAwakeWindow(rows, lag)
	offset := first.Sub(row.Start).Nanoseconds()
	level.NativeStartOffsetNanos = &offset
	start := row.Start.Add(lag)
	if !ok || row.End.Sub(start) < minimum {
		return level, nil, nil
	}
	watts, standardError, count, err := windowPower(power, start, row.End)
	if err != nil {
		return SessionLevel{}, nil, fmt.Errorf("level %s: %w", row.ID, err)
	}
	level.MeanWatts, level.MeanWattsStandardError, level.PowerSamples = &watts, &standardError, count
	return level, &fitLevel{rate: level.InputRate, watts: watts, variance: standardError * standardError, awake: window}, nil
}

// windowPower returns the integrated mean watts over [start, end], the
// standard error of the samples' mean and their count.
func windowPower(power []Sample, start, end time.Time) (float64, float64, int, error) {
	joules, err := measurePower(start, end, power)
	if err != nil {
		return 0, 0, 0, err
	}
	var inside []float64
	for _, sample := range power {
		if !sample.ObservedAt.Before(start) && !sample.ObservedAt.After(end) {
			inside = append(inside, sample.Watts)
		}
	}
	if len(inside) < 4 {
		return 0, 0, 0, fmt.Errorf("power window holds %d samples, fewer than the four a standard error needs", len(inside))
	}
	return joules / end.Sub(start).Seconds(), standardErrorOfMean(inside), len(inside), nil
}

// standardErrorOfMean is the non-overlapping batch means estimate with
// batches of floor(sqrt(n)) samples (Flegal and Jones 2010, Batch means and
// spectral variance estimators in Markov chain Monte Carlo, Annals of
// Statistics 38(2)): means of batches that outlast the correlation are close
// to independent, so a meter that smooths or a device that cycles does not
// pass for independent samples. It needs at least four values.
func standardErrorOfMean(values []float64) float64 {
	means := batchMeans(values)
	return math.Sqrt(stat.Variance(means, nil) / float64(len(means)))
}

func batchMeans(values []float64) []float64 {
	size := int(math.Sqrt(float64(len(values))))
	means := make([]float64, len(values)/size)
	for k := range means {
		means[k] = stat.Mean(values[k*size:(k+1)*size], nil)
	}
	return means
}

// postLoadObservation is what the observation after load reads.
type postLoadObservation struct {
	idle      SessionParameter
	interval  float64
	settledAt float64
	// hold is set when the decay identifies the hold time.
	hold   *SessionParameter
	series [][2]float64
}

// analysePostLoad finds where the post-load power settles and its level.
// The drop's midpoint crossing t50 is the first sample at or below the middle
// of the loaded level, the mean over the minimum power window before load
// stopped, and the settled level; the series has settled one meter lag after
// t50, and the settled level is the mean from there to the end. Settle point
// and level are refined together until the settle point stops moving. The
// hold is t50 less the meter lag, identified when that exceeds the meter's
// sampling interval, the median sample spacing. Idle is not identified only
// when the settled series still trends.
func analysePostLoad(power []Sample, start, end time.Time, lag, minimum time.Duration, tests sessionTests) (postLoadObservation, error) {
	var observation postLoadObservation
	var spacing []float64
	var previous time.Time
	observation.series = make([][2]float64, 0)
	for _, sample := range power {
		if sample.ObservedAt.Before(start) || sample.ObservedAt.After(end) {
			continue
		}
		if !previous.IsZero() {
			spacing = append(spacing, sample.ObservedAt.Sub(previous).Seconds())
		}
		previous = sample.ObservedAt
		observation.series = append(observation.series, [2]float64{sample.ObservedAt.Sub(start).Seconds(), sample.Watts})
	}
	if len(spacing) == 0 {
		return postLoadObservation{}, errors.New("post-load window holds fewer than two power samples")
	}
	slices.Sort(spacing)
	observation.interval = stat.Quantile(0.5, stat.Empirical, spacing, nil)
	loaded, loadedError, _, err := windowPower(power, start.Add(-minimum), start)
	if err != nil {
		return postLoadObservation{}, fmt.Errorf("loaded power before post-load: %w", err)
	}

	settle := start
	var crossing, beforeCrossing float64
	for {
		idle, _, _, err := windowPower(power, settle, end)
		if err != nil {
			return postLoadObservation{}, fmt.Errorf("post-load power: %w", err)
		}
		index := slices.IndexFunc(observation.series, func(point [2]float64) bool { return point[1] <= (loaded+idle)/2 })
		if index < 0 {
			break
		}
		next := start.Add(seconds(observation.series[index][0])).Add(lag)
		if !next.After(settle) {
			break
		}
		settle, crossing, beforeCrossing = next, observation.series[index][0], 0
		if index > 0 {
			beforeCrossing = observation.series[index-1][0]
		}
	}
	idle, idleError, _, err := windowPower(power, settle, end)
	if err != nil {
		return postLoadObservation{}, fmt.Errorf("post-load power: %w", err)
	}
	if loaded-idle <= tests.oneSided*math.Hypot(loadedError, idleError) {
		// No drop: there is no decay to wait for or to time.
		settle, crossing = start, 0
		if idle, idleError, _, err = windowPower(power, settle, end); err != nil {
			return postLoadObservation{}, fmt.Errorf("post-load power: %w", err)
		}
	}
	observation.settledAt = settle.Sub(start).Seconds()
	// Still trending at the end: the slope over the last minimum power window
	// of the settled series.
	from := math.Max(observation.settledAt, end.Sub(start).Seconds()-minimum.Seconds())
	if slope, trending := trend(observation.series, from, tests.confidence); trending {
		observation.idle = notIdentified(fmt.Sprintf("post-load power still trends at %.4f W/s from %.1f s to the end of the window", slope, from))
		return observation, nil
	}
	observation.idle = identifiedParameter(idle, [2]float64{idle - tests.twoSided*idleError, idle + tests.twoSided*idleError})
	if hold := crossing - lag.Seconds(); hold > observation.interval {
		parameter := identifiedParameter(hold, [2]float64{math.Max(beforeCrossing-lag.Seconds(), 0), hold})
		observation.hold = &parameter
	}
	return observation, nil
}

// trend fits a line to the batch means of the series from the given offset,
// batched as in standardErrorOfMean, and reports whether its slope differs
// from zero by the two-sided Student t test at the confidence level.
func trend(series [][2]float64, from, confidence float64) (float64, bool) {
	var times, watts []float64
	for _, point := range series {
		if point[0] >= from {
			times, watts = append(times, point[0]), append(watts, point[1])
		}
	}
	if len(times) < 3 {
		return 0, false
	}
	times, watts = batchMeans(times), batchMeans(watts)
	if len(times) < 3 {
		// Fewer than three batches leave no residual to test against.
		return 0, false
	}
	intercept, slope := stat.LinearRegression(times, watts, nil, false)
	meanTime := stat.Mean(times, nil)
	var squares, spread float64
	for i := range times {
		residual := watts[i] - intercept - slope*times[i]
		squares += residual * residual
		spread += (times[i] - meanTime) * (times[i] - meanTime)
	}
	degrees := float64(len(times) - 2)
	quantile := distuv.StudentsT{Mu: 0, Sigma: 1, Nu: degrees}.Quantile((1 + confidence) / 2)
	return slope, math.Abs(slope) > quantile*math.Sqrt(squares/degrees/spread)
}

func seconds(value float64) time.Duration {
	return time.Duration(math.Round(value * float64(time.Second)))
}
