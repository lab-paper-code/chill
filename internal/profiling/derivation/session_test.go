package profilederivation

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

type synthLevel struct {
	kind            string
	period, service time.Duration
	count           int
}

type synthBatch struct {
	batch  int
	levels []synthLevel
}

// synthSession lays out periodic executions per level and power samples every
// second: each level's watts follow model at the level's awake fraction for
// hold, and the post-load samples follow postLoad.
func synthSession(batches []synthBatch, hold float64, model func(batch int, rate, awake float64) float64, postLoad func(offset float64) float64) SessionInput {
	settings := SessionSettings{MeterLagSeconds: 1, MinimumPowerWindowSeconds: 5, PostLoadWindowSeconds: 20, MinimumSamples: 59, Percentile: 95}
	origin := time.Date(2026, 11, 14, 0, 0, 0, 0, time.UTC)
	cursor := origin.Add(10 * time.Second)
	input := SessionInput{Settings: settings}
	var id uint64
	var finish time.Time
	var executions []NativeBatchRow
	type span struct {
		start, end time.Time
		batch      int
		rate       float64
	}
	var spans []span
	for _, b := range batches {
		batchInput := SessionBatchInput{Batch: b.batch, PeakMemoryBytes: 1 << 30, MemoryKind: "peak"}
		for _, l := range b.levels {
			level := SessionLevelInput{Row: DriverLevelRow{
				Kind: l.kind, ID: fmt.Sprintf("s/b%d/%s", b.batch, l.kind), Start: cursor,
				End: cursor.Add(time.Duration(l.count) * l.period), CompletedInputs: l.count * b.batch, CompletedBatches: l.count,
			}}
			for k := range l.count {
				id++
				start := cursor.Add(time.Duration(k) * l.period)
				row := NativeBatchRow{BatchID: id, BatchSize: b.batch, StartedAt: start, FinishedAt: start.Add(l.service), ServiceDurationNanos: l.service.Nanoseconds(), LatestEnqueuedAt: start}
				if !finish.IsZero() {
					idle := start.Sub(finish).Nanoseconds()
					row.PrecedingIdleNanos = &idle
				}
				finish = row.FinishedAt
				level.Native = append(level.Native, row)
			}
			executions = append(executions, level.Native...)
			spans = append(spans, span{level.Row.Start, level.Row.End, b.batch, float64(level.Row.CompletedInputs) / level.Row.End.Sub(level.Row.Start).Seconds()})
			batchInput.Levels = append(batchInput.Levels, level)
			cursor = level.Row.End
		}
		input.Batches = append(input.Batches, batchInput)
	}
	input.PostLoadStart, input.PostLoadEnd = cursor, cursor.Add(seconds(settings.PostLoadWindowSeconds))
	for at := origin; !at.After(input.PostLoadEnd); at = at.Add(time.Second) {
		watts := model(0, 0, 0)
		for _, s := range spans {
			if !at.Before(s.start) && at.Before(s.end) {
				watts = model(s.batch, s.rate, newAwakeWindow(executions, s.start.Add(time.Second), s.end).fraction(hold))
			}
		}
		if !at.Before(input.PostLoadStart) {
			watts = postLoad(at.Sub(input.PostLoadStart).Seconds())
		}
		input.Power = append(input.Power, Sample{ObservedAt: at, Watts: watts})
	}
	return input
}

func gpuLikeBatches() []synthBatch {
	ms := time.Millisecond
	return []synthBatch{
		{1, []synthLevel{{"saturation", 10 * ms, 10 * ms, 1000}, {"mid", 40 * ms, 10 * ms, 250}, {"sparse", 10 * time.Second, 100 * ms, 3}}},
		{2, []synthLevel{{"saturation", 15 * ms, 15 * ms, 800}, {"mid", 60 * ms, 15 * ms, 200}}},
	}
}

func TestDeriveSessionFailsWithNativeRowCount(t *testing.T) {
	input := synthSession(gpuLikeBatches(), 0, func(int, float64, float64) float64 { return 10 }, func(float64) float64 { return 10 })
	level := &input.Batches[1].Levels[0]
	level.Native = level.Native[:len(level.Native)-1]
	_, err := DeriveSession(input)
	if err == nil || err.Error() != "level s/b2/saturation: native rows 799 of 800" {
		t.Fatalf("error = %v", err)
	}
}

func TestDeriveSessionCountsRepeatedNativeRowsOnce(t *testing.T) {
	input := synthSession(gpuLikeBatches(), 0, func(int, float64, float64) float64 { return 10 }, func(float64) float64 { return 10 })
	level := &input.Batches[0].Levels[1]
	level.Native = append(level.Native, level.Native[:50]...)
	result, err := DeriveSession(input)
	if err != nil {
		t.Fatal(err)
	}
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

func TestDeriveSessionReportsUnsettledPostLoadAsNotIdentified(t *testing.T) {
	input := synthSession(gpuLikeBatches(), 0, func(int, float64, float64) float64 { return 30 }, func(offset float64) float64 { return 30 - offset })
	result, err := DeriveSession(input)
	if err != nil {
		t.Fatal(err)
	}
	power := result.Power
	for name, p := range map[string]SessionParameter{"idle": power.IdleWatts, "step": power.AwakeStepWatts, "hold": power.HoldSeconds, "slope": result.Batches[0].JoulesPerInput} {
		if p.Status != ParameterNotIdentified || p.Value != nil {
			t.Fatalf("%s = %+v", name, p)
		}
	}
	if !strings.HasPrefix(power.IdleWatts.Reason, "post-load power has not settled") {
		t.Fatalf("idle reason = %q", power.IdleWatts.Reason)
	}
}

// On a CPU path the hold is shorter than every gap, so the awake fraction is
// the busy fraction plus hold times the batch rate, proportional to the input
// rate: the step trades against the slopes.
func TestDeriveSessionReportsInseparableStepAndSlope(t *testing.T) {
	ms := time.Millisecond
	batches := []synthBatch{
		{1, []synthLevel{{"saturation", 20 * ms, 20 * ms, 400}, {"mid", 80 * ms, 20 * ms, 100}, {"sparse", 400 * ms, 20 * ms, 20}}},
		{2, []synthLevel{{"saturation", 30 * ms, 30 * ms, 300}, {"mid", 120 * ms, 30 * ms, 70}}},
	}
	slope := map[int]float64{1: 0.2, 2: 0.15}
	input := synthSession(batches, 0, func(batch int, rate, awake float64) float64 { return 9 + 3*awake + slope[batch]*rate }, func(float64) float64 { return 9 })
	result, err := DeriveSession(input)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]SessionParameter{"step": result.Power.AwakeStepWatts, "hold": result.Power.HoldSeconds, "b1": result.Batches[0].JoulesPerInput, "b2": result.Batches[1].JoulesPerInput} {
		if p.Status != ParameterNotIdentified || p.Value == nil {
			t.Fatalf("%s = %+v", name, p)
		}
	}
}

func TestDeriveSessionReportsSlopeAtBound(t *testing.T) {
	slope := map[int]float64{1: 0.02, 2: -0.01}
	input := synthSession(gpuLikeBatches(), 5, func(batch int, rate, awake float64) float64 { return 60 + 50*awake + slope[batch]*rate },
		func(offset float64) float64 {
			if offset < 6 {
				return 110
			}
			return 60
		})
	result, err := DeriveSession(input)
	if err != nil {
		t.Fatal(err)
	}
	if hold := result.Power.HoldSeconds; hold.Status != ParameterIdentified || *hold.Value != 5 {
		t.Fatalf("hold = %+v", hold)
	}
	if b2 := result.Batches[1].JoulesPerInput; b2.Status != ParameterAtBound || *b2.Value != 0 {
		t.Fatalf("b2 slope = %+v", b2)
	}
	if b1 := result.Batches[0].JoulesPerInput; b1.Status != ParameterIdentified || *b1.Value <= 0 {
		t.Fatalf("b1 slope = %+v", b1)
	}
}

// A fast model reaches 150,000 service records per batch size. The catalog
// reads at most --max-evidence-bytes (64 MiB by default) per object.
func TestSessionResultEncodesLargeBatchCompactly(t *testing.T) {
	const records = 150_000
	batch := SessionBatch{Batch: 1, ServiceNanos: make([]int64, records), PrecedingIdleNanos: make([]int64, records)}
	for i := range records {
		// One second of service after ten seconds of idle: the widest values a session keeps.
		batch.ServiceNanos[i], batch.PrecedingIdleNanos[i] = 999_999_999, 9_999_999_999
	}
	raw, err := json.Marshal(SessionResult{SchemaVersion: SessionResultSchemaVersion, Batches: []SessionBatch{batch}})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d records encode to %d bytes", records, len(raw))
	if len(raw) > records*21+4096 {
		t.Fatalf("encoded size %d exceeds 21 bytes per record", len(raw))
	}
	decoded, err := DecodeSessionResult(raw)
	if err != nil || len(decoded.Batches[0].ServiceNanos) != records {
		t.Fatalf("decode: %v", err)
	}
}
