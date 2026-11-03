package serving

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func directFixture(batch int) (*Profile, map[string]firstfloor.Metadata, time.Time) {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	p := &Profile{ID: "profile", NodeName: "node", Batches: []BatchProfile{{Batch: batch, DurationNanos: []float64{100 * float64(batch)}}}}
	p.Condition.NodeUID = "node-uid"
	p.Condition.LogicalModel = "model"
	p.Condition.PowerMode = "fixture-fixed-mode"
	p.Condition.RuntimeImage = "image"
	p.Condition.Artifact.ExecutionPath = "cpu"
	policies := map[string]firstfloor.Metadata{"instance": {ProfileID: p.ID, DecisionID: "decision", AppliedAt: at.Add(-time.Second), Policy: firstfloor.Policy{FixedBatchSize: batch}}}
	return p, policies, at
}

func directRow(at time.Time, id uint64, batch, member int, start, finish time.Duration) RequestRow {
	q, s, f := at, at.Add(start), at.Add(finish)
	return RequestRow{Kind: requestRowFinal, RequestID: fmt.Sprintf("%d/%d", id, member), Node: &NodeReference{Name: "node", UID: "node-uid"}, Instance: "instance", WorkloadUID: "workload", ProfileID: "profile", DecisionID: "decision", DefinitionID: "definition", BatchID: id, BatchSize: batch, Outcome: outcomeCompleted, ExecutionComplete: true, NativeOutcome: outcomeCompleted, NativeExecutionComplete: true, NativeQueuedAt: &q, NativeRunningAt: &s, NativeFinishedAt: &f}
}

func TestOccupancyEstimatorPairEligibilityAndLateJoin(t *testing.T) {
	for _, tc := range []struct {
		name   string
		batch  int
		mutate func([]RequestRow, map[string]firstfloor.Metadata)
		pairs  int
		want   float64
	}{
		{name: "backlogged", batch: 1, pairs: 1, want: 50},
		{name: "late supplement", batch: 1, pairs: 1, want: 50, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) {
			rows[0].NativeQueuedAt = nil
			rows[0].NativeRunningAt = nil
			rows[0].NativeFinishedAt = nil
			rows[0].NativeExecutionComplete = false
			rows[0].NativeOutcome = ""
		}},
		{name: "full batch two signed negative", batch: 2, pairs: 1, want: -25},
		{name: "one early member not enough", batch: 2, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) {
			late := rows[0].NativeFinishedAt.Add(time.Nanosecond)
			rows[3].NativeQueuedAt = &late
		}},
		{name: "non backlog", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) {
			late := rows[0].NativeFinishedAt.Add(time.Nanosecond)
			rows[1].NativeQueuedAt = &late
		}},
		{name: "incomplete batch membership", batch: 2, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) { rows[3].BatchID = 3 }},
		{name: "missing native never reconstructed", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) {
			rows[0].NativeRunningAt = nil
			legacy := int64(1)
			rows[0].RunningNanos = &legacy
			rows[0].QueueMS = 1
		}},
		{name: "failed final", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) { rows[0].Outcome = "transport-failed" }},
		{name: "incomplete execution", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) { rows[0].ExecutionComplete = false }},
		{name: "configuration change", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) { rows[1].DecisionID = "changed-decision" }},
		{name: "missing batch cannot bridge", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) { rows[1].BatchID = 3 }},
		{name: "physical instance boundary", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) { rows[1].Instance = "restart" }},
		{name: "policy wait unproven", batch: 1, mutate: func(_ []RequestRow, p map[string]firstfloor.Metadata) {
			m := p["instance"]
			m.Policy.MaxBatchWaitMS = 1
			p["instance"] = m
		}},
		{name: "policy updated after execution", batch: 1, mutate: func(rows []RequestRow, p map[string]firstfloor.Metadata) {
			m := p["instance"]
			m.AppliedAt = rows[1].NativeRunningAt.Add(time.Second)
			p["instance"] = m
		}},
		{name: "worker overlap", batch: 1, mutate: func(rows []RequestRow, _ map[string]firstfloor.Metadata) {
			f := rows[1].NativeRunningAt.Add(time.Nanosecond)
			rows[0].NativeFinishedAt = &f
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, policies, at := directFixture(tc.batch)
			var rows []RequestRow
			for id := uint64(1); id <= 2; id++ {
				for member := range tc.batch {
					start := time.Duration(id-1) * 150
					rows = append(rows, directRow(at, id, tc.batch, member, start, start+50))
				}
			}
			if tc.mutate != nil {
				tc.mutate(rows, policies)
			}
			e := NewOccupancyEstimator(map[string]*Profile{p.ID: p}, policies, at, at.Add(time.Second))
			for _, r := range rows {
				e.Add(r)
			}
			if tc.name == "late supplement" {
				supplement := directRow(at, 1, 1, 0, 0, 50)
				supplement.Kind = RequestRowNative
				e.Add(supplement)
			}
			results := e.Results()
			pairs := 0
			for _, r := range results {
				pairs += r.Pairs
				if r.Pairs > 0 && (r.Statistics == nil || r.Statistics.Mean != tc.want) {
					t.Fatalf("residual=%+v want %v", r.Statistics, tc.want)
				}
			}
			if pairs != tc.pairs {
				t.Fatalf("pairs=%d want %d results=%+v", pairs, tc.pairs, results)
			}
		})
	}
}

func TestOccupancyEstimatorExactQuantilesCompletenessAndWorkerWindow(t *testing.T) {
	p, policies, at := directFixture(1)
	e := NewOccupancyEstimator(map[string]*Profile{p.ID: p}, policies, at, at.Add(time.Second))
	var start time.Duration
	for i, cycle := range []time.Duration{90, 100, 110, 120, 130, 140, 0} {
		r := directRow(at, uint64(i+1), 1, 0, start, start+20)
		// Gateway clocks may precede or follow the worker by an arbitrary offset.
		r.ReceivedAt = at.Add(24 * time.Hour)
		assigned := r
		assigned.Kind = requestRowAssigned
		assigned.BatchID = 0
		assigned.BatchSize = 0
		assigned.NativeQueuedAt = nil
		assigned.NativeRunningAt = nil
		assigned.NativeFinishedAt = nil
		assigned.NativeOutcome = ""
		assigned.NativeExecutionComplete = false
		assigned.ExecutionComplete = false
		assigned.Outcome = ""
		e.Add(assigned)
		e.Add(r)
		start += cycle
	}
	got := e.Results()[0]
	s := got.Statistics
	if got.Pairs != 6 || got.Batches != 7 || got.CompleteBatches != 7 || got.Requests != 7 || got.RawRows != 14 || got.CompleteRawRows != 7 || got.EnrichedCompleteRawRows != 14 || got.FinalRows != 7 || got.CompleteFinalRows != 7 || s == nil {
		t.Fatalf("facts=%+v", got)
	}
	if s.Mean != 15 || s.Median != 10 || s.P25 != 0 || s.P75 != 30 || s.IQR != 30 || s.P10 != -10 || s.P90 != 30 || math.Abs(s.NegativeFraction-1.0/6) > 1e-15 {
		t.Fatalf("exact Python round-index quantiles=%+v", s)
	}
	// Both starts must belong to the half-open worker window.
	limited := NewOccupancyEstimator(map[string]*Profile{p.ID: p}, policies, at, at.Add(150))
	limited.Add(directRow(at, 1, 1, 0, 0, 50))
	limited.Add(directRow(at, 2, 1, 0, 150, 200))
	if r := limited.Results()[0]; r.Pairs != 0 || r.Statistics != nil {
		t.Fatalf("cross-window pair counted: %+v", r)
	}
}

func TestOccupancyEstimatorConflictsAndDuplicateFinals(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		p, policies, at := directFixture(1)
		e := NewOccupancyEstimator(map[string]*Profile{p.ID: p}, policies, at, at.Add(time.Second))
		r := directRow(at, 1, 1, 0, 0, 50)
		e.Add(r)
		if conflict {
			r.Kind = RequestRowNative
			s := at.Add(1)
			r.NativeRunningAt = &s
		}
		e.Add(r)
		e.Add(directRow(at, 2, 1, 0, 150, 200))
		if got := e.Results()[0]; got.Pairs != 0 || got.CompleteBatches != 1 {
			t.Fatalf("invalid join accepted: %+v", got)
		}
	}
}
