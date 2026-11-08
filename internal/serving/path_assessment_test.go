package serving

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func assessmentTestRow(snapshot SchedulerSnapshot, id string) *RequestRow {
	row := requestPathRow()
	i := snapshot.Instances[0]
	row.RequestID, row.WorkloadUID, row.DefinitionID = id, snapshot.Workloads[0].UID, snapshot.Workloads[0].Identity()
	row.ReceivedAt = snapshot.At.Add(-50 * time.Millisecond)
	row.Instance, row.ProfileID, row.Node = i.ID, i.Profile.ID, &i.Node
	row.BatchID, row.BatchSize, row.ServiceDurationNanos = 1, 1, int64(10*time.Millisecond)
	idle := int64(0)
	row.PrecedingIdleNanos = &idle
	return &row
}

func assessmentTestCandidate(snapshot SchedulerSnapshot) SchedulerCandidate {
	return SchedulerCandidate{ID: "chosen", Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances, Routing: map[string]map[string]int32{snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: 1}}}
}

func assessForTest(t *testing.T, settings SchedulerSettings, snapshot SchedulerSnapshot, candidate SchedulerCandidate, rows ...*RequestRow) PathAssessment {
	t.Helper()
	report, err := AssessPath(context.Background(), settings, snapshot, candidate, 17, rows)
	if err != nil { t.Fatal(err) }
	return report
}

func TestAssessPathKeepsZeroMissingAndOutcomePopulations(t *testing.T) {
	settings, snapshot := schedulerFixture()
	zero := assessmentTestRow(snapshot, "zero")
	*zero.ElapsedNanos, *zero.AcceptedNanos, *zero.DispatchedNanos, *zero.NativeElapsedMS, zero.QueueMS = 0, 0, 0, 0, 0
	zero.NativeRunningAt, zero.NativeFinishedAt = zero.NativeQueuedAt, zero.NativeQueuedAt
	zero.ServerTiming = RequestTimings{}
	zero.ServerTiming.Record(ServerSenderFreeze, 0)
	margin := 0.0
	zero.CompletionMarginMS = &margin
	missing := assessmentTestRow(snapshot, "missing")
	missing.NativeQueuedAt = nil
	failed := assessmentTestRow(snapshot, "failed")
	failed.Outcome = "canceled"
	incomplete := assessmentTestRow(snapshot, "incomplete")
	incomplete.ExecutionComplete = false
	open := assessmentTestRow(snapshot, "open")
	open.Kind, open.ElapsedNanos, open.Outcome = RequestRowAssigned, nil, ""
	report := assessForTest(t, settings, snapshot, assessmentTestCandidate(snapshot), zero, missing, failed, incomplete, open)
	if report.Requests != 5 || len(report.Observed) != 1 { t.Fatalf("lost received population: %+v", report) }
	o := report.Observed[0]
	if o.Completed != 2 || o.Failed != 1 || o.Incomplete != 2 || o.LatencySamples != 2 || o.PathSamples != 1 {
		t.Fatalf("outcomes became completed path samples: %+v", o)
	}
	if o.TotalMS == nil || *o.TotalMS != 0 || o.PostMS == nil || *o.PostMS != 0 || o.FrozenPostSamples != 1 || o.FrozenPostExceedances != 0 {
		t.Fatalf("observed zero became unknown: %+v", o)
	}
	if o.MissingPath["missing_native_timing"] != 1 || o.MissingPath["failed"] != 1 || o.MissingPath["incomplete"] != 2 || o.SLOUnknown != 1 {
		t.Fatalf("missing/failed/open evidence lost: %+v", o)
	}
}

func TestAssessPathComparesFrozenPOSTAndSeparatesPathFromService(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.CompletionMarginMS = 100 // Current budget is not the budget old calls used.
	base := assessmentTestRow(snapshot, "base")
	frozen := 8.0
	base.CompletionMarginMS = &frozen
	before := assessForTest(t, settings, snapshot, assessmentTestCandidate(snapshot), base).Observed[0]
	slow := assessmentTestRow(snapshot, "slow")
	slow.CompletionMarginMS = &frozen
	*slow.ElapsedNanos += int64(10*time.Millisecond)
	after := assessForTest(t, settings, snapshot, assessmentTestCandidate(snapshot), slow).Observed[0]
	if before.FrozenPostExceedances != 0 || after.FrozenPostExceedances != 1 || after.MaximumFrozenPostExcessMS == nil || *after.MaximumFrozenPostExcessMS != 10 {
		t.Fatalf("POST compared with current rather than frozen budget: before=%+v after=%+v", before, after)
	}
	if *after.TotalMS-*before.TotalMS != 10 || *after.PostMS-*before.PostMS != 10 || !reflect.DeepEqual(before.Physical, after.Physical) {
		t.Fatalf("path deterioration changed physical service: before=%+v after=%+v", before, after)
	}
	slow.CompletionMarginMS = nil
	unknown := assessForTest(t, settings, snapshot, assessmentTestCandidate(snapshot), slow).Observed[0]
	if unknown.MissingFrozenPost != 1 || unknown.FrozenPostSamples != 0 || unknown.FrozenPostExceedances != 0 {
		t.Fatalf("missing frozen POST manufactured mismatch: %+v", unknown)
	}
}

func TestAssessPathDeduplicatesPhysicalBatchesAndUsesActualIdleContext(t *testing.T) {
	settings, snapshot := schedulerFixture()
	zero, long := int64(0), int64(100*time.Millisecond)
	snapshot.Instances[0].Profile.Batches[0] = schedulerTestPairedBatch(snapshot.Instances[0].Profile.Batches[0], []firstfloor.ServiceSample{
		{TrialID: "trial", ServiceDurationNanos: int64(5*time.Millisecond)},
		{TrialID: "trial", PrecedingIdleNanos: &zero, ServiceDurationNanos: int64(10*time.Millisecond)},
		{TrialID: "trial", PrecedingIdleNanos: &long, ServiceDurationNanos: int64(20*time.Millisecond)},
	})
	a, b := assessmentTestRow(snapshot, "a"), assessmentTestRow(snapshot, "b")
	c := assessmentTestRow(snapshot, "c")
	c.BatchID, c.ServiceDurationNanos, c.PrecedingIdleNanos = 2, int64(30*time.Millisecond), &long
	failed := assessmentTestRow(snapshot, "failed")
	failed.BatchID, failed.Outcome, failed.NativeExecutionComplete, failed.ServiceDurationNanos = 3, "canceled", false, 0
	// This is the actual sparse raw native-supplement contract: no Node/ProfileID.
	late := &RequestRow{Kind: RequestRowNative, RequestID: failed.RequestID, WorkloadUID: failed.WorkloadUID, DefinitionID: failed.DefinitionID,
		Instance: failed.Instance, BatchID: 3, BatchSize: 1, NativeExecutionComplete: true, NativeOutcome: outcomeCompleted,
		ServiceDurationNanos: int64(20*time.Millisecond), PrecedingIdleNanos: &long}
	report := assessForTest(t, settings, snapshot, assessmentTestCandidate(snapshot), a, b, c, failed, late)
	if len(report.Observed) != 1 || report.Requests != 4 { t.Fatalf("native supplement became HTTP sample: %+v", report) }
	o := report.Observed[0]
	p := o.Physical
	if o.Failed != 1 || o.PathSamples != 3 || p.Batches != 3 || p.ServiceSamples != 3 || p.IdleSamples != 3 || p.ComparedBatches != 3 || p.ConditionalExceedances != 1 {
		t.Fatalf("physical batch duplication or wrong service context: %+v", o)
	}
	if p.ServiceMS == nil || *p.ServiceMS != 30 || p.IdleMS == nil || *p.IdleMS != 100 || p.Support.Queries != 3 || p.Support.OutsideSupportQueries != 0 {
		t.Fatalf("canonical G/S not retained: %+v", p)
	}
	// A missing actual G queries startup support; it is not a fabricated G=0.
	a.PrecedingIdleNanos = nil
	startup := assessForTest(t, settings, snapshot, assessmentTestCandidate(snapshot), a).Observed[0].Physical
	if startup.StartupOrUnknownIdle != 1 || startup.IdleSamples != 0 || startup.IdleMS != nil || startup.Support.StartupQueries != 1 || *startup.MeanConditionalPercentileMS != 5 {
		t.Fatalf("unknown idle became busy gap: %+v", startup)
	}
}

func TestAssessPathSeparatesMismatchedDefinitionProfileAndTarget(t *testing.T) {
	settings, snapshot := schedulerFixture()
	for _, change := range []struct {
		name string
		apply func(*RequestRow)
		definition, profile bool
	}{
		{"definition", func(r *RequestRow) { r.DefinitionID = "old-definition" }, false, true},
		{"workload", func(r *RequestRow) { r.WorkloadUID = "old-workload" }, false, true},
		{"profile", func(r *RequestRow) { r.ProfileID = "old-profile" }, true, false},
		{"node", func(r *RequestRow) { r.Node = &NodeReference{Name: "other", UID: "other"} }, true, false},
	} {
		t.Run(change.name, func(t *testing.T) {
			row := assessmentTestRow(snapshot, "actual")
			change.apply(row)
			report := assessForTest(t, settings, snapshot, assessmentTestCandidate(snapshot), row)
			o := report.Observed[0]
			if o.DefinitionMatched != change.definition || o.ProfileMatched != change.profile || o.Physical.ComparedBatches != 0 || o.Requests != 1 {
				t.Fatalf("historical configuration compared with current model: %+v", report)
			}
			if !change.definition && (report.Unmatched["definition"] != 1 || o.LatencyMS != nil) { t.Fatalf("definition mismatch hidden: %+v", report) }
			if !change.profile && report.Unmatched["profile"] != 1 { t.Fatalf("profile mismatch hidden: %+v", report) }
		})
	}
	candidate := assessmentTestCandidate(snapshot)
	candidate.Routing = map[string]map[string]int32{snapshot.Workloads[0].UID: {snapshot.Instances[0].ID: 0}}
	report := assessForTest(t, settings, snapshot, candidate, assessmentTestRow(snapshot, "actual"))
	if report.Observed[0].SelectedPlacement != 0 || report.Observed[0].DifferentPlacement != 1 || report.Unmatched["selected_placement"] != 1 {
		t.Fatalf("different selected routing treated as target evidence: %+v", report)
	}
}

func TestAssessPathUsesSharedReplayAndKeepsOpenExceedanceAsLowerBound(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.ObservationWindow = time.Second
	d := snapshot.Workloads[0]
	snapshot.Arrivals = []SchedulerArrival{{ID: "compute", At: snapshot.At.Add(-time.Second), Definition: d, WorkloadUID: d.UID, Accepted: true, Compute: true}}
	candidate := assessmentTestCandidate(snapshot)
	want, err := schedulerReplayEstimate(context.Background(), settings, snapshot, candidate, 17, nil, nil)
	if err != nil { t.Fatal(err) }
	candidate.SLO = []SchedulerEstimate{{UID: "stale", Samples: 900}}
	row := assessmentTestRow(snapshot, "open")
	row.Kind, row.ElapsedNanos, row.ReceivedAt = RequestRowAssigned, nil, snapshot.At.Add(-500*time.Millisecond)
	report := assessForTest(t, settings, snapshot, candidate, row)
	if len(want.SLO) == 0 || want.SLO[0].Samples == 0 || !reflect.DeepEqual(report.PredictedSLO, want.SLO) || report.PredictedStatus != want.Status {
		t.Fatalf("assessment did not replay selected candidate: got=%+v want=%+v", report, want)
	}
	o := report.Observed[0]
	if o.SLOExceedances != 1 || o.SLOLowerBoundExceedances != 1 || o.Completed != 0 || o.LatencyMS != nil || o.TotalMS != nil || o.PostMS != nil {
		t.Fatalf("open elapsed lower bound became completed/path evidence: %+v", o)
	}
}
