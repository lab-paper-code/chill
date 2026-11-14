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
	// chiSquare bounds a residual increase in units of the level variance.
	chiSquare float64
	// oneSided bounds a drop, twoSided a trend, in standard errors.
	oneSided, twoSided float64
}

// powerOptimum is the best power fit whatever the identification of its
// parameters; the stored result publishes only identified values.
type powerOptimum struct {
	fitted      bool
	idle, step  float64
	hold        float64
	slopeByBatch map[int]float64
}

// DeriveSession computes the stored result of one session.
func DeriveSession(input SessionInput) (SessionResult, error) {
	result, _, err := deriveSession(input)
	return result, err
}

func deriveSession(input SessionInput) (SessionResult, powerOptimum, error) {
	settings := input.Settings
	if settings.MeterLagSeconds < 0 || settings.MinimumPowerWindowSeconds <= 0 || settings.ConfidenceLevel < 0.5 || settings.ConfidenceLevel >= 1 {
		return SessionResult{}, powerOptimum{}, errors.New("session settings need a nonnegative meter lag, a positive minimum power window and a confidence level in [0.5, 1)")
	}
	if input.MaxResultBytes < 1 {
		return SessionResult{}, powerOptimum{}, errors.New("session result byte limit must be positive")
	}
	if len(input.Batches) == 0 {
		return SessionResult{}, powerOptimum{}, errors.New("session has no measured batch")
	}
	lag, minimum := seconds(settings.MeterLagSeconds), seconds(settings.MinimumPowerWindowSeconds)
	power := slices.Clone(input.Power)
	sort.Slice(power, func(i, j int) bool { return power[i].ObservedAt.Before(power[j].ObservedAt) })
	tests := sessionTests{
		chiSquare: distuv.ChiSquared{K: 1}.Quantile(settings.ConfidenceLevel),
		oneSided:  distuv.UnitNormal.Quantile(settings.ConfidenceLevel),
		twoSided:  distuv.UnitNormal.Quantile((1 + settings.ConfidenceLevel) / 2),
	}

	result := SessionResult{SchemaVersion: SessionResultSchemaVersion, Condition: input.Condition, Settings: settings, Stop: input.Stop}
	var levels []fitLevel
	for index, batchInput := range input.Batches {
		if batchInput.Batch < 1 || (index > 0 && batchInput.Batch <= input.Batches[index-1].Batch) {
			return SessionResult{}, powerOptimum{}, errors.New("session batch sizes must be positive and increasing")
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
				return SessionResult{}, powerOptimum{}, fmt.Errorf("level %s has unknown kind %q", levelInput.Row.ID, levelInput.Row.Kind)
			}
			rows, err := distinctNativeRows(levelInput, batchInput.Batch)
			if err != nil {
				return SessionResult{}, powerOptimum{}, err
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
				return SessionResult{}, powerOptimum{}, err
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
		return SessionResult{}, powerOptimum{}, err
	}
	result.Power.IdleWatts, result.Power.PostLoad = postLoad.idle, postLoad.series
	result.Power.MeterIntervalSeconds, result.Power.SettledAtSeconds = postLoad.interval, postLoad.settledAt
	var optimum powerOptimum
	if idle, ok := postLoad.idle.Value(); ok {
		optimum = fitPower(&result, levels, idle, postLoad.hold, tests)
	} else {
		reason := "settled idle watts not identified"
		result.Power.AwakeStepWatts, result.Power.HoldSeconds = notIdentified(reason), notIdentified(reason)
		for index := range result.Batches {
			result.Batches[index].JoulesPerInput = notIdentified(reason)
		}
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return SessionResult{}, powerOptimum{}, err
	}
	if int64(len(encoded)) > input.MaxResultBytes {
		return SessionResult{}, powerOptimum{}, fmt.Errorf("session result encodes to %d bytes, above the %d byte read limit", len(encoded), input.MaxResultBytes)
	}
	return result, optimum, nil
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
	window, ok := newAwakeWindow(rows, lag)
	offset := window.origin.Sub(row.Start).Nanoseconds()
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
	inside := samplesWithin(power, start, end)
	return joules / end.Sub(start).Seconds(), standardErrorOfMean(inside), len(inside), nil
}

func samplesWithin(power []Sample, start, end time.Time) []float64 {
	var inside []float64
	for _, sample := range power {
		if !sample.ObservedAt.Before(start) && !sample.ObservedAt.After(end) {
			inside = append(inside, sample.Watts)
		}
	}
	return inside
}

// standardErrorOfMean scales the sample variance by the integrated
// autocorrelation time, so a meter that smooths or a device that cycles does
// not pass for independent samples.
func standardErrorOfMean(values []float64) float64 {
	return math.Sqrt(stat.Variance(values, nil) * integratedAutocorrelationTime(values) / float64(len(values)))
}

// integratedAutocorrelationTime is Geyer's initial positive sequence
// estimator (Geyer 1992, Practical Markov Chain Monte Carlo, Statistical
// Science 7(4)): one plus twice the sum of autocorrelations, summed in
// adjacent pairs while a pair stays positive.
func integratedAutocorrelationTime(values []float64) float64 {
	mean := stat.Mean(values, nil)
	autocovariance := func(lag int) float64 {
		var sum float64
		for i := 0; i+lag < len(values); i++ {
			sum += (values[i] - mean) * (values[i+lag] - mean)
		}
		return sum / float64(len(values))
	}
	variance := autocovariance(0)
	if variance == 0 {
		return 1
	}
	sum := -variance
	for m := 0; 2*m+1 < len(values); m++ {
		pair := autocovariance(2*m) + autocovariance(2*m+1)
		if pair <= 0 {
			break
		}
		sum += 2 * pair
	}
	return sum / variance
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
// The drop's midpoint crossing t50 lies between the loaded level, the mean
// over the minimum power window before load stopped, and the settled level;
// the series has settled one meter lag after t50, and the settled level is the
// mean from there to the end. Settle point and level are refined together
// until the settle point stops moving. The hold is t50 less the meter lag,
// identified when that exceeds the meter's sampling interval. Idle is not
// identified only when the settled series still trends.
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
		if end.Sub(next) < minimum {
			observation.idle = notIdentified(fmt.Sprintf("post-load power crosses the midpoint of its drop at %.1f s, leaving less than the minimum power window after the meter lag", observation.series[index][0]))
			return observation, nil
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
		if idle, _, _, err = windowPower(power, settle, end); err != nil {
			return postLoadObservation{}, fmt.Errorf("post-load power: %w", err)
		}
	}
	observation.settledAt = settle.Sub(start).Seconds()
	if slope, trending := trend(observation.series, observation.settledAt, tests.twoSided); trending {
		observation.idle = notIdentified(fmt.Sprintf("post-load power still trends at %.4f W/s from %.1f s to the end of the window", slope, observation.settledAt))
		return observation, nil
	}
	observation.idle = identifiedParameter(idle, [2]float64{idle, idle})
	if hold := crossing - lag.Seconds(); hold > observation.interval {
		parameter := identifiedParameter(hold, [2]float64{math.Max(beforeCrossing-lag.Seconds(), 0), hold})
		observation.hold = &parameter
	}
	return observation, nil
}

// trend fits a line to the series from the given offset and reports whether
// its slope differs from zero, with the slope's variance scaled by the
// residuals' integrated autocorrelation time.
func trend(series [][2]float64, from, twoSided float64) (float64, bool) {
	var times, watts []float64
	for _, point := range series {
		if point[0] >= from {
			times, watts = append(times, point[0]), append(watts, point[1])
		}
	}
	if len(times) < 3 {
		return 0, false
	}
	intercept, slope := stat.LinearRegression(times, watts, nil, false)
	residuals := make([]float64, len(times))
	meanTime := stat.Mean(times, nil)
	var squares, spread float64
	for i := range times {
		residuals[i] = watts[i] - intercept - slope*times[i]
		squares += residuals[i] * residuals[i]
		spread += (times[i] - meanTime) * (times[i] - meanTime)
	}
	variance := squares / float64(len(times)-2) * integratedAutocorrelationTime(residuals) / spread
	return slope, slope*slope > twoSided*twoSided*variance
}

func seconds(value float64) time.Duration {
	return time.Duration(math.Round(value * float64(time.Second)))
}
