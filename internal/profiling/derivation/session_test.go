package profilederivation

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gonum.org/v1/gonum/mat"
	"gonum.org/v1/gonum/stat"
)

const synthMeter = `{"kind":"synthetic","endpoint":"http://meter","metric":"node_watts"}`

type synthLevel struct {
	kind            string
	period, service time.Duration
	count           int
}

type synthBatch struct {
	batch  int
	levels []synthLevel
}

type powerModel func(batch int, rate, awake float64) float64

// synthSession lays out executions per level and power samples every second:
// each level's watts follow model at the level's own-clock awake fraction for
// hold, and the post-load samples after load stopped follow postLoad. Without
// random the executions are periodic and noise watts alternate in sign every
// sample; with random each idle gap is exponential with the level's mean
// (period less service) and every power sample gets Gaussian noise of
// standard deviation noise.
func synthSession(batches []synthBatch, hold float64, model powerModel, postLoad func(offset float64) float64, noise float64, random *rand.Rand) SessionInput {
	settings := SessionSettings{MeterLagSeconds: 1, MinimumPowerWindowSeconds: 5, PostLoadWindowSeconds: 20, MinimumSamples: 59, ConfidenceLevel: 0.95}
	origin := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	cursor := origin.Add(10 * time.Second)
	input := SessionInput{Settings: settings, MaxResultBytes: 64 << 20, MeterSource: json.RawMessage(synthMeter)}
	var id uint64
	var finish time.Time
	type span struct {
		start, end time.Time
		watts      float64
	}
	var spans []span
	for _, b := range batches {
		batchInput := SessionBatchInput{Batch: b.batch, PeakMemoryBytes: 1 << 30, MemoryKind: "peak"}
		for index, l := range b.levels {
			level := SessionLevelInput{Row: DriverLevelRow{
				Kind: l.kind, ID: fmt.Sprintf("s/b%d/%d", b.batch, index), Start: cursor, CompletedInputs: l.count * b.batch, CompletedBatches: l.count,
			}}
			start := cursor
			for range l.count {
				id++
				row := NativeBatchRow{BatchID: id, BatchSize: b.batch, StartedAt: start, FinishedAt: start.Add(l.service), ServiceDurationNanos: l.service.Nanoseconds(), LatestEnqueuedAt: start}
				if !finish.IsZero() {
					idle := start.Sub(finish).Nanoseconds()
					row.PrecedingIdleNanos = &idle
				}
				finish = row.FinishedAt
				level.Native = append(level.Native, row)
				gap := l.period - l.service
				if random != nil {
					gap = time.Duration(random.ExpFloat64() * float64(gap))
				}
				start = finish.Add(gap)
			}
			level.Row.End = start
			window, _, _ := levelAwakeWindow(level.Native, time.Second)
			rate := float64(level.Row.CompletedInputs) / level.Row.End.Sub(level.Row.Start).Seconds()
			spans = append(spans, span{level.Row.Start, level.Row.End, model(b.batch, rate, window.fraction(hold))})
			batchInput.Levels = append(batchInput.Levels, level)
			cursor = level.Row.End
		}
		input.Batches = append(input.Batches, batchInput)
	}
	input.PostLoadStart, input.PostLoadEnd = cursor, cursor.Add(seconds(settings.PostLoadWindowSeconds))
	for at, sign := origin, 1.0; !at.After(input.PostLoadEnd); at, sign = at.Add(time.Second), -sign {
		watts := model(0, 0, 0)
		if at.After(input.PostLoadStart) {
			watts = postLoad(at.Sub(input.PostLoadStart).Seconds())
		}
		for _, s := range spans {
			if !at.Before(s.start) && !at.After(s.end) {
				watts = s.watts + sign*noise
				if random != nil {
					watts = s.watts + random.NormFloat64()*noise
				}
				break
			}
		}
		input.Power = append(input.Power, Sample{ObservedAt: at, Watts: watts})
	}
	return input
}

func gpuLikeBatches() []synthBatch {
	ms := time.Millisecond
	return []synthBatch{
		{1, []synthLevel{{LevelSaturation, 10 * ms, 10 * ms, 1000}, {LevelMid, 40 * ms, 10 * ms, 250}, {LevelSparse, 10 * time.Second, 100 * ms, 3}}},
		{2, []synthLevel{{LevelSaturation, 15 * ms, 15 * ms, 800}, {LevelMid, 60 * ms, 15 * ms, 200}}},
	}
}

func gpuModel(slope map[int]float64) powerModel {
	return func(batch int, rate, awake float64) float64 { return 60 + 50*awake + slope[batch]*rate }
}

func flat(float64) float64 { return 60 }

// decayAt keeps the awake level for hold plus the one-second meter lag.
func decayAt(hold float64) func(float64) float64 {
	return func(offset float64) float64 {
		if offset < hold+1 {
			return 110
		}
		return 60
	}
}

func derive(t *testing.T, input SessionInput) SessionResult {
	t.Helper()
	result, err := DeriveSession(input)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// expectIdentified checks an Identified value and that its range is within
// width of it on both sides.
func expectIdentified(t *testing.T, name string, p SessionParameter, want, width float64) {
	t.Helper()
	value, ok := p.Value()
	bounds, _ := p.Range()
	if p.Status() != ParameterIdentified || !ok || math.Abs(value-want) > width || bounds[0] < want-width || bounds[1] > want+width {
		t.Errorf("%s = %s %g %v (%s), want Identified %g within %g", name, p.Status(), value, bounds, p.Reason(), want, width)
	}
}

func expectNotIdentified(t *testing.T, name string, p SessionParameter) {
	t.Helper()
	if _, ok := p.Value(); ok || p.Status() != ParameterNotIdentified {
		t.Errorf("%s = %s, want NotIdentified without a value", name, p.Status())
	}
}

func TestDeriveSessionFailsWithNativeRowCount(t *testing.T) {
	input := synthSession(gpuLikeBatches(), 0, gpuModel(nil), flat, 0, nil)
	level := &input.Batches[1].Levels[0]
	level.Native = level.Native[:len(level.Native)-1]
	_, err := DeriveSession(input)
	if err == nil || err.Error() != "level s/b2/0: native rows 799 of 800" {
		t.Fatalf("error = %v", err)
	}
}

func TestDeriveSessionCountsRepeatedNativeRowsOnce(t *testing.T) {
	input := synthSession(gpuLikeBatches(), 0, gpuModel(nil), flat, 0, nil)
	level := &input.Batches[0].Levels[1]
	level.Native = append(level.Native, level.Native[:50]...)
	result := derive(t, input)
	// One prepare-first record has no preceding idle gap.
	if got := len(result.Batches[0].ServiceNanos); got != 1000+250+3-1 {
		t.Fatalf("service records = %d", got)
	}
	repeated := input.Batches[0].Levels[1].Native[0]
	repeated.ServiceDurationNanos++
	level.Native = append(level.Native, repeated)
	if _, err := DeriveSession(input); err == nil || !strings.Contains(err.Error(), "repeats with different values") {
		t.Fatalf("conflicting repeat error = %v", err)
	}
}

func TestDeriveSessionValidatesSettingsAndSize(t *testing.T) {
	for _, confidence := range []float64{0.4, 1} {
		input := synthSession(gpuLikeBatches(), 0, gpuModel(nil), flat, 0, nil)
		input.Settings.ConfidenceLevel = confidence
		if _, err := DeriveSession(input); err == nil || !strings.Contains(err.Error(), "confidence level in [0.5, 1)") {
			t.Errorf("confidence %g: error = %v", confidence, err)
		}
	}
	input := synthSession(gpuLikeBatches(), 0, gpuModel(nil), flat, 0, nil)
	input.MaxResultBytes = 1000
	if _, err := DeriveSession(input); err == nil || !strings.Contains(err.Error(), "bytes, above the 1000 byte read limit") {
		t.Errorf("size error = %v", err)
	}
}

// The decay ends too late to leave a minimum power window of settled power.
// A 30 s hold keeps every level awake, so the loaded level is 110 W.
func TestDeriveSessionReportsLateSettleAsNotIdentified(t *testing.T) {
	result := derive(t, synthSession(gpuLikeBatches(), 30, gpuModel(nil), decayAt(16), 0, nil))
	power := result.Power
	for name, p := range map[string]SessionParameter{"idle": power.IdleWatts, "step": power.AwakeStepWatts, "hold": power.HoldSeconds, "slope": result.Batches[0].JoulesPerInput} {
		expectNotIdentified(t, name, p)
	}
	if !strings.HasPrefix(power.IdleWatts.Reason(), "post-load power settles at 18.0 s") {
		t.Fatalf("idle reason = %q", power.IdleWatts.Reason())
	}
}

// The residual minimum lies between two observed gaps: a search over the gaps
// alone published hold 6.9 s with a wrong step and slopes.
func TestDeriveSessionFindsHoldBetweenGaps(t *testing.T) {
	ms := time.Millisecond
	batches := []synthBatch{
		{1, []synthLevel{{LevelSaturation, 10 * ms, 10 * ms, 1000}, {LevelMid, 40 * ms, 10 * ms, 250}, {LevelSparse, 3 * time.Second, 100 * ms, 6}, {LevelSparse, 7 * time.Second, 100 * ms, 3}, {LevelSparse, 12 * time.Second, 100 * ms, 2}}},
		{2, []synthLevel{{LevelSaturation, 15 * ms, 15 * ms, 800}, {LevelMid, 60 * ms, 15 * ms, 200}, {LevelMid, 100 * ms, 15 * ms, 100}}},
	}
	input := synthSession(batches, 5, gpuModel(map[int]float64{1: 0.02, 2: 0.015}), flat, 0, nil)
	result := derive(t, input)
	expectIdentified(t, "hold", result.Power.HoldSeconds, 5, 1e-6)
	expectIdentified(t, "step", result.Power.AwakeStepWatts, 50, 1e-6)
	expectIdentified(t, "b1", result.Batches[0].JoulesPerInput, 0.02, 1e-8)
	expectIdentified(t, "b2", result.Batches[1].JoulesPerInput, 0.015, 1e-8)

	// The awake fraction reads native time differences only.
	for b := range input.Batches {
		for l := range input.Batches[b].Levels {
			for r := range input.Batches[b].Levels[l].Native {
				row := &input.Batches[b].Levels[l].Native[r]
				row.StartedAt, row.FinishedAt = row.StartedAt.Add(-2*time.Second), row.FinishedAt.Add(-2*time.Second)
			}
		}
	}
	shifted := derive(t, input)
	if !reflect.DeepEqual(shifted.Power, result.Power) {
		t.Errorf("shifted native clock power = %+v, want %+v", shifted.Power, result.Power)
	}
	for i := range result.Batches {
		if !reflect.DeepEqual(shifted.Batches[i].JoulesPerInput, result.Batches[i].JoulesPerInput) {
			t.Errorf("shifted native clock b%d slope differs", result.Batches[i].Batch)
		}
		if offset := shifted.Batches[i].Levels[0].NativeStartOffsetNanos; offset == nil || *offset != -2e9 {
			t.Errorf("b%d native start offset = %v", result.Batches[i].Batch, offset)
		}
	}
}

// On a CPU path the hold is shorter than every gap, so the awake fraction is
// the busy fraction plus hold times the batch rate, close to proportional to
// the input rate: within the level noise the step trades against the slopes,
// its range reaches zero and so every hold fits.
func TestDeriveSessionReportsInseparableStepAndSlope(t *testing.T) {
	ms := time.Millisecond
	batches := []synthBatch{
		{1, []synthLevel{{LevelSaturation, 20 * ms, 20 * ms, 400}, {LevelMid, 80 * ms, 20 * ms, 100}, {LevelSparse, 400 * ms, 20 * ms, 20}}},
		{2, []synthLevel{{LevelSaturation, 30 * ms, 30 * ms, 300}, {LevelMid, 120 * ms, 30 * ms, 70}}},
	}
	slope := map[int]float64{1: 0.2, 2: 0.15}
	result := derive(t, synthSession(batches, 0.005, func(batch int, rate, awake float64) float64 { return 9 + 3*awake + slope[batch]*rate }, func(float64) float64 { return 9 }, 2, nil))
	if bounds, _ := result.Power.AwakeStepWatts.Range(); result.Power.AwakeStepWatts.Status() != ParameterIdentified || bounds[0] != 0 {
		t.Errorf("step = %s", describe(result.Power.AwakeStepWatts))
	}
	expectNotIdentified(t, "hold", result.Power.HoldSeconds)
}

// A slope estimated at zero is a value with its range.
func TestDeriveSessionReportsSlopeAtZero(t *testing.T) {
	result := derive(t, synthSession(gpuLikeBatches(), 5, gpuModel(map[int]float64{1: 0.02, 2: -0.01}), decayAt(5), 0, nil))
	expectIdentified(t, "hold", result.Power.HoldSeconds, 5, 1)
	if b2, ok := result.Batches[1].JoulesPerInput.Value(); !ok || b2 != 0 {
		t.Errorf("b2 slope = %s", describe(result.Batches[1].JoulesPerInput))
	}
	if value, ok := result.Batches[0].JoulesPerInput.Value(); !ok || value <= 0 {
		t.Errorf("b1 slope = %g %v", value, ok)
	}
}

// Every level awake throughout and no visible decay: the hold has only a lower
// bound, but the step and slopes are determined exactly.
func TestDeriveSessionIdentifiesAlwaysAwakeStepAndSlopes(t *testing.T) {
	ms := time.Millisecond
	batches := []synthBatch{
		{1, []synthLevel{{LevelSaturation, 10 * ms, 10 * ms, 1000}, {LevelMid, 40 * ms, 10 * ms, 250}, {LevelSparse, 2 * time.Second, 100 * ms, 6}}},
		{2, []synthLevel{{LevelSaturation, 15 * ms, 15 * ms, 800}, {LevelMid, 60 * ms, 15 * ms, 200}}},
	}
	result := derive(t, synthSession(batches, 30, gpuModel(map[int]float64{1: 0.02, 2: 0.015}), flat, 0, nil))
	expectIdentified(t, "step", result.Power.AwakeStepWatts, 50, 1e-6)
	expectIdentified(t, "b1", result.Batches[0].JoulesPerInput, 0.02, 1e-8)
	expectIdentified(t, "b2", result.Batches[1].JoulesPerInput, 0.015, 1e-8)
	expectNotIdentified(t, "hold", result.Power.HoldSeconds)
}

func TestDeriveSessionIdentifiesOneBatchWithVisibleDecay(t *testing.T) {
	ms := time.Millisecond
	batches := []synthBatch{{1, []synthLevel{{LevelSaturation, 10 * ms, 10 * ms, 1000}, {LevelMid, 40 * ms, 10 * ms, 250}, {LevelSparse, 10 * time.Second, 100 * ms, 3}}}}
	result := derive(t, synthSession(batches, 5, gpuModel(map[int]float64{1: 0.02}), decayAt(5), 0, nil))
	expectIdentified(t, "hold", result.Power.HoldSeconds, 5, 1)
	expectIdentified(t, "step", result.Power.AwakeStepWatts, 50, 1e-6)
	expectIdentified(t, "b1", result.Batches[0].JoulesPerInput, 0.02, 1e-8)
	if interval := result.Power.MeterIntervalSeconds; interval != 1 {
		t.Errorf("meter interval = %g", interval)
	}
}

// bruteNNLS is the independent reference: every set of free columns solved by
// QR or LQ, skipping the sets the solver finds singular, negative solutions
// dropped. A non-negative optimum always lies on a set of independent columns.
func bruteNNLS(a *mat.Dense, b *mat.VecDense) float64 {
	rows, columns := a.Dims()
	best := math.Inf(1)
	for mask := range 1 << columns {
		x := make([]float64, columns)
		var free []int
		for j := range columns {
			if mask&(1<<j) != 0 {
				free = append(free, j)
			}
		}
		if len(free) > 0 {
			sub := mat.NewDense(rows, len(free), nil)
			for k, j := range free {
				sub.SetCol(k, mat.Col(nil, j, a))
			}
			var z mat.VecDense
			if err := z.SolveVec(sub, b); err != nil {
				continue
			}
			feasible := true
			for k, j := range free {
				feasible = feasible && z.AtVec(k) >= -1e-12
				x[j] = math.Max(0, z.AtVec(k))
			}
			if !feasible {
				continue
			}
		}
		var r mat.VecDense
		r.MulVec(a, mat.NewVecDense(columns, x))
		r.SubVec(b, &r)
		best = math.Min(best, mat.Dot(&r, &r))
	}
	return best
}

func TestNonNegativeLeastSquaresAgreesWithBruteForce(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	for i := range 400 {
		rows, columns := 1+random.IntN(10), 1+random.IntN(6)
		a := mat.NewDense(rows, columns, nil)
		for r := range rows {
			for c := range columns {
				a.Set(r, c, random.NormFloat64()*math.Pow(10, float64(random.IntN(5)-2)))
			}
		}
		switch kind := i % 4; {
		case kind == 1 && columns > 1: // collinear column
			column := mat.Col(nil, 0, a)
			for r := range column {
				column[r] *= 2.5
			}
			a.SetCol(columns-1, column)
		case kind == 2: // zero column
			a.SetCol(random.IntN(columns), make([]float64, rows))
		case kind == 3: // power fit shape: awake fraction and one rate per row
			for r := range rows {
				a.Set(r, 0, random.Float64())
				for c := 1; c < columns; c++ {
					a.Set(r, c, 0)
				}
				if columns > 1 {
					a.Set(r, 1+random.IntN(columns-1), 300*random.Float64())
				}
			}
		}
		b := mat.NewVecDense(rows, nil)
		for r := range rows {
			b.SetVec(r, random.NormFloat64()*50)
		}
		x, residual := nonNegativeLeastSquares(a, b)
		for _, value := range x {
			if value < 0 {
				t.Fatalf("case %d: negative solution %v", i, x)
			}
		}
		if brute := bruteNNLS(a, b); math.Abs(residual-brute) > 1e-8*(1+brute) {
			t.Errorf("case %d (%dx%d): residual %g, brute force %g", i, rows, columns, residual, brute)
		}
	}
}

// The settled idle of a clean 1 W, 30 s cycle is identified at every end of
// the post-load window, and its interval from the batch means standard error
// covers the cycle's mean.
func TestPostLoadIdleToleratesCycle(t *testing.T) {
	start := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	var power []Sample
	for at := -20; at <= 300; at++ {
		watts := 100.0
		if at >= 3 {
			watts = 66 + math.Sin(2*math.Pi*float64(at)/30)
		}
		power = append(power, Sample{ObservedAt: start.Add(time.Duration(at) * time.Second), Watts: watts})
	}
	tests := newSessionTests(0.95)
	for end := 60; end <= 300; end++ {
		observation, err := analysePostLoad(power, start, start.Add(time.Duration(end)*time.Second), time.Second, 15*time.Second, tests)
		if err != nil {
			t.Fatal(err)
		}
		bounds, _ := observation.idle.Range()
		if _, ok := observation.idle.Value(); !ok || bounds[0] > 66 || bounds[1] < 66 {
			t.Errorf("end %d s: idle %s %s", end, describe(observation.idle), observation.idle.Reason())
		}
	}
	watts := make([]float64, 0, 298)
	for _, sample := range power[23:] {
		watts = append(watts, sample.Watts)
	}
	if independent, batched := math.Sqrt(stat.Variance(watts, nil)/float64(len(watts))), standardErrorOfMean(watts); batched <= independent {
		t.Errorf("batch means standard error %g, independent %g", batched, independent)
	}
}

func TestSessionParameterCarriesNoValueWhenNotIdentified(t *testing.T) {
	raw, err := json.Marshal(notIdentifiedWithin("why", [2]float64{0, 2}))
	if err != nil || string(raw) != `{"status":"NotIdentified","range":[0,2],"reason":"why"}` {
		t.Fatalf("encoded %s %v", raw, err)
	}
	var p SessionParameter
	if err := json.Unmarshal([]byte(`{"status":"NotIdentified","value":1}`), &p); err == nil {
		t.Fatal("decoded a NotIdentified value")
	}
}

// A fast model reaches 150,000 service records per batch size. The catalog
// reads at most --max-evidence-bytes (64 MiB by default) per object.
func TestSessionResultEncodesLargeBatchCompactly(t *testing.T) {
	const records = 150_000
	batch := SessionBatch{Batch: 1, ServiceNanos: make([]int64, records), PrecedingIdleNanos: make([]int64, records), DispatchOccupancyNanos: make([]float64, records)}
	for i := range records {
		// One second of service after ten seconds of idle: the widest values a
		// session keeps; occupancy with a full-precision fraction.
		batch.ServiceNanos[i], batch.PrecedingIdleNanos[i], batch.DispatchOccupancyNanos[i] = 999_999_999, 9_999_999_999, 1e9/3
	}
	unknown := notIdentified("not measured")
	batch.JoulesPerInput = unknown
	raw, err := json.Marshal(SessionResult{
		SchemaVersion: SessionResultSchemaVersion, Batches: []SessionBatch{batch}, MeterSource: json.RawMessage(synthMeter),
		Power: SessionPower{IdleWatts: unknown, AwakeStepWatts: unknown, HoldSeconds: unknown},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d records encode to %d bytes", records, len(raw))
	// 21 bytes for a service record, 18 for an occupancy sample.
	if len(raw) > records*(21+18)+4096 {
		t.Fatalf("encoded size %d exceeds 39 bytes per record", len(raw))
	}
	decoded, err := DecodeSessionResult(raw)
	if err != nil || len(decoded.Batches[0].ServiceNanos) != records {
		t.Fatalf("decode: %v", err)
	}
}

// refitLevels rebuilds the fitted levels of a derived synthetic session.
func refitLevels(input SessionInput, result SessionResult) []fitLevel {
	var levels []fitLevel
	for b, batch := range result.Batches {
		for l, level := range batch.Levels {
			if level.MeanWatts == nil {
				continue
			}
			window, _, _ := levelAwakeWindow(input.Batches[b].Levels[l].Native, seconds(input.Settings.MeterLagSeconds))
			levels = append(levels, fitLevel{
				at: [2]int{b, l}, rate: level.InputRate, watts: *level.MeanWatts,
				variance: *level.MeanWattsStandardError * *level.MeanWattsStandardError, awake: window,
			})
		}
	}
	return levels
}

// cpuPoisson is a CPU-like path with exponential idle gaps: the given number
// of saturation and mid executions per batch, and sparse executions at batch 1.
func cpuPoisson(sizes []int, saturation, mid int) []synthBatch {
	batches := make([]synthBatch, 0, len(sizes))
	for _, b := range sizes {
		service := 2*time.Millisecond + time.Duration(b)*time.Millisecond/8
		levels := []synthLevel{{LevelSaturation, service + 200*time.Microsecond, service, saturation}, {LevelMid, 4 * service, service, mid}}
		if b == 1 {
			levels = append(levels, synthLevel{LevelSparse, 2 * time.Second, service, 8})
		}
		batches = append(batches, synthBatch{b, levels})
	}
	return batches
}

func cpuModel(batch int, rate, awake float64) float64 {
	if batch == 0 {
		return 9
	}
	return 9 + 3*awake + 0.02/float64(batch)*rate
}

// The merged search agrees with the search over every breakpoint: every
// level's predicted watts differ by at most the resolution.
func TestHoldSearchMergesWithinResolution(t *testing.T) {
	input := synthSession(cpuPoisson([]int{1, 2}, 5000, 1500), 0.004, cpuModel, func(float64) float64 { return 9 }, 0.05, rand.New(rand.NewPCG(3, 4)))
	merged := derive(t, input)
	idle, _ := merged.Power.IdleWatts.Value()
	exhaustive := merged
	exhaustive.Batches = slices.Clone(merged.Batches)
	for i := range exhaustive.Batches {
		exhaustive.Batches[i].Levels = slices.Clone(merged.Batches[i].Levels)
	}
	fitPower(&exhaustive, refitLevels(input, merged), idle, nil, newSessionTests(input.Settings.ConfidenceLevel), 0)
	resolution := merged.Power.HoldResolutionWatts
	t.Logf("merged: %d segments at %.4g W, hold %s, step %s; every breakpoint: %d segments, hold %s, step %s",
		merged.Power.HoldSegmentsSearched, resolution, describe(merged.Power.HoldSeconds), describe(merged.Power.AwakeStepWatts),
		exhaustive.Power.HoldSegmentsSearched, describe(exhaustive.Power.HoldSeconds), describe(exhaustive.Power.AwakeStepWatts))
	if merged.Power.HoldSegmentsSearched >= exhaustive.Power.HoldSegmentsSearched {
		t.Errorf("merged search solved %d segments, the full one %d", merged.Power.HoldSegmentsSearched, exhaustive.Power.HoldSegmentsSearched)
	}
	predict := func(result SessionResult, b, l int) float64 {
		step, _ := result.Power.AwakeStepWatts.Value()
		slope, _ := result.Batches[b].JoulesPerInput.Value()
		level := result.Batches[b].Levels[l]
		return idle + step**level.AwakeFraction + slope*level.InputRate
	}
	for b, batch := range merged.Batches {
		for l, level := range batch.Levels {
			if level.AwakeFraction == nil {
				continue
			}
			if difference := math.Abs(predict(merged, b, l) - predict(exhaustive, b, l)); difference > resolution {
				t.Errorf("%s: predictions differ by %.4g W, above the %.4g W resolution", level.ID, difference, resolution)
			}
		}
	}
}

// 150,000 records over eight batch sizes, every gap distinct. The merge rule
// bounds the segments: every segment but the last, together with the next
// breakpoint step, moves some level's fraction by more than r = resolution /
// scale, so disjoint segment pairs each take more than r of the total change
// D = Σ (awake at the top - awake at zero), and segments <= 2D/r + 1.
func TestHoldSearchBoundsLargeSession(t *testing.T) {
	input := synthSession(cpuPoisson([]int{1, 2, 4, 8, 16, 32, 64, 128}, 12500, 6250), 0.01, cpuModel, func(float64) float64 { return 9 }, 0.3, rand.New(rand.NewPCG(5, 6)))
	began := time.Now()
	result := derive(t, input)
	elapsed := time.Since(began)
	idle, _ := result.Power.IdleWatts.Value()
	var change, scale float64
	records := 0
	for _, level := range refitLevels(input, result) {
		top := slices.Max(append(level.awake.breakpoints(), 0))
		change += level.awake.fraction(top) - level.awake.fraction(0)
		scale = math.Max(scale, level.watts-idle)
	}
	for _, batch := range input.Batches {
		for _, level := range batch.Levels {
			records += len(level.Native)
		}
	}
	bound := 2*change*scale/result.Power.HoldResolutionWatts + 1
	t.Logf("%d records: %d segments searched (bound %.0f) at %.4g W in %v; hold %s, step %s", records, result.Power.HoldSegmentsSearched, bound,
		result.Power.HoldResolutionWatts, elapsed, describe(result.Power.HoldSeconds), describe(result.Power.AwakeStepWatts))
	if float64(result.Power.HoldSegmentsSearched) > bound {
		t.Errorf("%d segments searched, above the bound %.0f", result.Power.HoldSegmentsSearched, bound)
	}
}
