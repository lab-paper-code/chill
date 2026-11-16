package profilederivation

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
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
// each level's watts follow model at the level's awake fraction for hold, and
// the post-load samples after load stopped follow postLoad. Without
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
			rate := float64(level.Row.CompletedInputs) / level.Row.End.Sub(level.Row.Start).Seconds()
			awake := AwakeFraction(rate, rate*l.service.Seconds()/float64(b.batch), hold)
			spans = append(spans, span{level.Row.Start, level.Row.End, model(b.batch, rate, awake)})
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

// expectIdentified checks an Identified value within width of want.
func expectIdentified(t *testing.T, name string, p SessionParameter, want, width float64) {
	t.Helper()
	if value, ok := p.Value(); p.Status() != ParameterIdentified || !ok || math.Abs(value-want) > width {
		t.Errorf("%s = %s %g (%s), want Identified %g within %g", name, p.Status(), value, p.Reason(), want, width)
	}
}

func expectNotIdentified(t *testing.T, name string, p SessionParameter) {
	t.Helper()
	if _, ok := p.Value(); ok || p.Status() != ParameterNotIdentified {
		t.Errorf("%s = %s, want NotIdentified without a value", name, p.Status())
	}
}

func TestDeriveSessionPreservesIndependentMeasurements(t *testing.T) {
	for _, scenario := range []struct {
		corrupt    func(*SessionInput)
		saturation bool
	}{
		{func(input *SessionInput) { input.Batches[1].Levels[0].Native = nil }, false},
		{func(input *SessionInput) { input.Batches[1].Levels[0].Row.CompletedInputs++ }, false},
		{func(input *SessionInput) { input.Power = nil }, true},
		{func(input *SessionInput) { input.PostLoadEnd = input.PostLoadStart }, true},
		{func(input *SessionInput) { input.Batches[1].PeakMemoryBytes = 0 }, true},
		{func(input *SessionInput) { input.Power[0].Watts = math.NaN() }, true},
	} {
		input := synthSession(gpuLikeBatches(), 0, gpuModel(nil), flat, 0, nil)
		scenario.corrupt(&input)
		result := derive(t, input)
		if len(result.Batches[0].ServiceNanos) != 1252 || *result.Batches[0].SaturationInputRate != 100 {
			t.Fatalf("independent service population changed: %+v", result.Batches[0])
		}
		if (result.Batches[1].SaturationInputRate != nil) != scenario.saturation {
			t.Fatalf("unmatched native population published: %+v", result.Batches[1])
		}
		if _, err := json.Marshal(result); err != nil {
			t.Fatal(err)
		}
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
}

func TestDeriveSessionBoundsStoredResult(t *testing.T) {
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
	power := result.LoadPower()
	for name, p := range map[string]SessionParameter{"idle": power.IdleWatts, "step": power.AwakeStepWatts, "hold": power.HoldSeconds,
		"slope": power.Batches[0].InputSlopeJoules, "saturation": power.Batches[0].SaturationJoulesPerInput} {
		expectNotIdentified(t, name, p)
	}
	if !strings.HasPrefix(power.IdleWatts.Reason(), "post-load power settles at 18.0 s") {
		t.Fatalf("idle reason = %q", power.IdleWatts.Reason())
	}
	if interval := result.Power.MeterIntervalSeconds; interval == nil || *interval != 1 {
		t.Errorf("meter interval = %v", interval)
	}
}

// loadPowerResult is a stored result whose level watts follow the scheduler's
// formula: idle 10 W, step 20 W, hold 0.5 s, slopes 0.05 J at batch 1 and
// 0.1 J at batch 2. Batch 1 serves 10 ms per execution and saturates at 100
// inputs/s, batch 2 serves 5 ms per execution of two and saturates at 400.
func loadPowerResult(levels map[int][]float64) SessionResult {
	const idle, step, hold = 10.0, 20.0, 0.5
	slope, service := map[int]float64{1: 0.05, 2: 0.1}, map[int][]int64{1: {8e6, 12e6}, 2: {4e6, 6e6}}
	result := SessionResult{Power: SessionPower{IdleWatts: identifiedParameter(idle, [2]float64{idle - 0.1, idle + 0.1})}}
	for _, batch := range []int{1, 2} {
		stored := SessionBatch{Batch: batch, ServiceNanos: service[batch], PrecedingIdleNanos: []int64{0, 0}}
		mean := float64(service[batch][0]+service[batch][1]) / 2e9
		for k, rate := range levels[batch] {
			watts := idle + step*AwakeFraction(rate, rate*mean/float64(batch), hold) + slope[batch]*rate
			kind := LevelMid
			if k == 0 {
				kind = LevelSaturation
			}
			stored.Levels = append(stored.Levels, SessionLevel{DriverLevelRow: DriverLevelRow{Kind: kind}, InputRate: rate, MeanWatts: &watts})
		}
		result.Batches = append(result.Batches, stored)
	}
	return result
}

// The executor's energy per input is the saturation level's watts above idle
// per input: batch 1 25/100 = 0.25 J, batch 2 60/400 = 0.15 J, so batch 2
// ranks first though its slope is twice batch 1's. The fit needs one level
// more than step and slopes, and recovers the formula's coefficients.
func TestLoadPowerRanksSaturationEnergyAndIdentifiesFit(t *testing.T) {
	power := loadPowerResult(map[int][]float64{1: {100, 25, 1}, 2: {400, 100}}).LoadPower()
	expectIdentified(t, "b1 saturation", power.Batches[0].SaturationJoulesPerInput, 0.25, 1e-12)
	expectIdentified(t, "b2 saturation", power.Batches[1].SaturationJoulesPerInput, 0.15, 1e-12)
	expectIdentified(t, "step", power.AwakeStepWatts, 20, 1e-6)
	expectIdentified(t, "hold", power.HoldSeconds, 0.5, 1e-6)
	expectIdentified(t, "b1 slope", power.Batches[0].InputSlopeJoules, 0.05, 1e-8)
	expectIdentified(t, "b2 slope", power.Batches[1].InputSlopeJoules, 0.1, 1e-8)

	// Three levels for step and two slopes: the saturation energies remain,
	// the fit is not identified.
	power = loadPowerResult(map[int][]float64{1: {100, 25}, 2: {400}}).LoadPower()
	expectIdentified(t, "b2 saturation", power.Batches[1].SaturationJoulesPerInput, 0.15, 1e-12)
	for name, p := range map[string]SessionParameter{"step": power.AwakeStepWatts, "hold": power.HoldSeconds, "slope": power.Batches[0].InputSlopeJoules} {
		expectNotIdentified(t, name, p)
	}
	if reason := power.AwakeStepWatts.Reason(); reason != "3 levels with watts do not determine the awake step, 2 slopes and the hold" {
		t.Errorf("reason = %q", reason)
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
	raw, err := json.Marshal(notIdentified("why"))
	if err != nil || string(raw) != `{"status":"NotIdentified","reason":"why"}` {
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
	raw, err := json.Marshal(SessionResult{
		SchemaVersion: SessionResultSchemaVersion, Batches: []SessionBatch{batch}, MeterSource: json.RawMessage(synthMeter),
		Power: SessionPower{IdleWatts: notIdentified("not measured")},
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

func TestDeriveSessionExcludedLevelRetainsOnlyItsReport(t *testing.T) {
	for index, kind := range []string{LevelSaturation, LevelMid, LevelSparse} {
		t.Run(kind, func(t *testing.T) {
			input := synthSession(gpuLikeBatches(), 0, gpuModel(nil), flat, 0, nil)
			target := &input.Batches[0].Levels[index]
			const detail = "native inference HTTP 408: request lifetime expired"
			target.Row.EndReason, target.Row.Detail = "RequestLifetimeExpired", detail
			raw, err := json.Marshal(target.Row)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			fields["excluded"] = json.RawMessage("true")
			raw, _ = json.Marshal(fields)
			if err := json.Unmarshal(raw, &target.Row); err != nil {
				t.Fatal(err)
			}
			target.Native = append(target.Native, NativeBatchRow{})
			expected := 0
			for i, level := range input.Batches[0].Levels {
				if i != index {
					for _, row := range level.Native {
						if row.PrecedingIdleNanos != nil {
							expected++
						}
					}
				}
			}
			result, err := DeriveSession(input)
			if err != nil {
				t.Fatal(err)
			}
			stored := result.Batches[0].Levels[index]
			raw, _ = json.Marshal(stored)
			if !strings.Contains(string(raw), `"excluded":true`) || stored.Detail != detail ||
				stored.MeanWatts != nil || stored.PowerSamples != 0 ||
				len(result.Batches[0].ServiceNanos) != expected {
				t.Fatalf("excluded level %+v service records %d expected %d", stored, len(result.Batches[0].ServiceNanos), expected)
			}
			if kind == LevelSaturation && len(result.Batches[0].DispatchOccupancyNanos) != 0 {
				t.Fatal("excluded saturation published occupancy")
			}
		})
	}
}
