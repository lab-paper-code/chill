package kube

import (
	"encoding/json"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestOccupancyPhysicalEvidenceAndUnknowns(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]serving.RequestRow) []serving.RequestRow
		reason string
	}{
		{"http-error-after-execution", func(rows []serving.RequestRow) []serving.RequestRow { rows[14].Outcome = "error"; return rows }, ""},
		{"native-error", func(rows []serving.RequestRow) []serving.RequestRow {
			rows[14].NativeOutcome = "batch_error"
			return rows
		}, "NativeExecutionError"},
		{"no-native-completion", func(rows []serving.RequestRow) []serving.RequestRow {
			rows[14].NativeExecutionComplete = false
			return rows
		}, "UnknownPhysicalContinuity"},
		{"missing-native-stage", func(rows []serving.RequestRow) []serving.RequestRow { rows[14].NativeQueuedAt = nil; return rows }, "UnknownPhysicalContinuity"},
		{"missing-batch", func(rows []serving.RequestRow) []serving.RequestRow { return slices.Delete(rows, 14, 15) }, "UnknownPhysicalContinuity"},
		{"unexecuted-error", func(rows []serving.RequestRow) []serving.RequestRow {
			rows[14].BatchID = 0
			rows[14].Outcome = "timeout"
			rows[14].ExecutionComplete = false
			return rows
		}, "UnknownRequestExecution"},
		{"missing-tail", func(rows []serving.RequestRow) []serving.RequestRow { return rows[:16] }, "MissingWindowBoundary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := tc.change(occupancyRows(40, 100*time.Millisecond))
			r := DispatchOccupancyRecord{Batch: 1, ProfileMeanNanos: 27.79e6}
			measureOccupancy(&r, rows, 2*time.Second, 500*time.Millisecond)
			judgeOccupancy(&r, edge.DispatchOccupancyMeasurementSpec{MinimumBatches: 2, MaximumIdleFraction: "1"})
			if tc.reason == "" {
				if r.Verdict != measurementAccepted || math.Abs(r.Throughput-10) > 1e-8 || r.PerRequestNanos != 72_210_000 {
					t.Fatalf("physical full d: %+v", r)
				}
			} else if r.Verdict != measurementInsufficient || !slices.Contains(r.Reasons, tc.reason) || r.IdleIdentified {
				t.Fatalf("missing explicit unknown: %+v", r)
			}
		})
	}
}

func TestOccupancyLateDirectEvidenceRepairsMissingStage(t *testing.T) {
	rows := occupancyRows(40, 100*time.Millisecond)
	late := rows[14]
	rows[14].NativeRunningAt = nil
	var a occupancyAccumulator
	for _, r := range rows {
		a.add(r)
	}
	late.Kind = serving.RequestRowNative
	a.add(late)
	// Native log replay must not add another physical member.
	a.add(late)
	r := DispatchOccupancyRecord{Batch: 1, ProfileMeanNanos: 27.79e6}
	r.Conditions.Conditions.InputsJSON = `{"coefficients":{"planningConfidence":0.95,"relativeXPlanningPrecision":0.01}}`
	a.measure(&r, 2*time.Second, 500*time.Millisecond)
	if len(r.Reasons) > 0 || !r.IdleIdentified || r.Throughput != 10 {
		t.Fatalf("late physical evidence: %+v", r)
	}
	if r.CycleRelativeHalfWidth == nil || *r.CycleRelativeHalfWidth != 0 || r.PrecisionTargetMet == nil || !*r.PrecisionTargetMet {
		t.Fatalf("lost empirical cycle precision: %+v", r)
	}
}

func TestOccupancyBatchReadinessUsesLastMemberAndAllReasons(t *testing.T) {
	rows := make([]serving.RequestRow, 0, 80)
	for _, r := range occupancyRows(40, 100*time.Millisecond) {
		r.BatchSize = 2
		rows = append(rows, r)
		r.RequestID += "/second"
		lastReady := r.NativeRunningAt.Add(-5 * time.Millisecond)
		r.NativeQueuedAt = &lastReady
		rows = append(rows, r)
	}
	r := DispatchOccupancyRecord{Batch: 2, ProfileMeanNanos: 27.79e6}
	measureOccupancy(&r, rows, 2*time.Second, 500*time.Millisecond)
	if !r.IdleIdentified || math.Abs(r.IdleFraction-.6721) > 1e-8 || math.Abs(r.IdleNanosPerRequest-33_605_000) > 1 {
		t.Fatalf("B readiness/full d: %+v", r)
	}
	judgeOccupancy(&r, edge.DispatchOccupancyMeasurementSpec{MinimumBatches: 100, MaximumIdleFraction: "0.01"})
	if !strings.Contains(r.Reason, occupancyTooFewBatches) || !strings.Contains(r.Reason, occupancyNotSaturated) {
		t.Fatalf("lost reason: %+v", r)
	}
}

func TestOccupancyFrozenConditionsAndClosedLoopJob(t *testing.T) {
	m := occupancyRoot("derived", time.Now(), 1)
	m.Spec.SupplyMode = occupancyClosedLoop
	m.Spec.RateFactor = ""
	m.Spec.Workers = 6
	m.Spec.RequestLifetimeSeconds = 2
	m.Spec.DeliveryMarginMillis = 10
	m.Spec.DrainSeconds = 3
	m.Spec.PreparationSeconds = 60
	m.Spec.JobSetupSeconds = 30
	m.Spec.CollectionSeconds = 30
	m.Spec.PlannedElapsedSeconds = 543
	m.Spec.Conditions = edge.DispatchOccupancyConditions{ProfileID: "profile", InputsJSON: `{"computed":{"tailBatchSeconds":0.1,"fastBatchSeconds":0.02,"refillSeconds":0.1,"prefixSeconds":0.1,"framedDecodedRequestUpperBytes":32,"queueRequestCap":256,"queueBytesCap":67108864,"qUpper":256},"coefficients":{"relativeXPlanningPrecision":0.01,"planningConfidence":0.95,"timeMargin":1.25,"maximumIdleBaseFraction":0.01,"idleDErrorSecondsPerRequest":0.001},"profile":{"profileID":"profile","runtimeImage":"image","workload":{"name":"workload","uid":"workload-uid","generation":1,"slo":{"percentile":99,"targetLatencyNanos":1000000000}}}}`, WaitingBatches: 5, MinimumCycles: 119, MaximumIdleNanosPerRequest: 1000000}
	if err := m.Spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(m.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var spec edge.DispatchOccupancyMeasurementSpec
	if err = json.Unmarshal(raw, &spec); err != nil || spec != m.Spec {
		t.Fatalf("frozen roundtrip: %+v %v", spec, err)
	}
	profile := &serving.Profile{ID: "profile", Batches: []serving.BatchProfile{{Batch: 1, DurationNanos: []float64{1e7}}}}
	profile.Condition.RuntimeImage = "image"
	w := &edge.Workload{}
	w.Name, w.UID, w.Generation = "workload", "workload-uid", 1
	w.Spec.SLO.Percentile, w.Spec.SLO.TargetLatency.Duration = 99, time.Second
	prep := PrepareSpec{}
	prep.Config.MaxQueueRequests, prep.Config.MaxQueueBytes = 256, 64<<20
	if err := validateOccupancyProfile(spec, profile, w, prep); err != nil {
		t.Fatal(err)
	}
	w.Generation++
	if validateOccupancyProfile(spec, profile, w, prep) == nil {
		t.Fatal("accepted stale Workload generation")
	}
	w.Generation--
	prep.Config.MaxQueueBytes = 128
	if validateOccupancyProfile(spec, profile, w, prep) == nil {
		t.Fatal("accepted supply exceeding current decoded queue bytes")
	}
	plan := occupancyPlan{workload: &edge.Workload{}, dataset: &edge.Dataset{}, fields: []string{"x"}}
	job := loadJob(m, testNode, plan, 0, LoadGeneratorConfig{})
	args := strings.Join(job.Spec.Template.Spec.Containers[0].Args, " ")
	if !strings.Contains(args, "--arrivals closed-loop") || !strings.Contains(args, "--request-lifetime 2") || !strings.Contains(args, "--delivery-margin 0.010") || strings.Contains(args, "--rate") || *job.Spec.ActiveDeadlineSeconds != 483 {
		t.Fatalf("closed loop Job: %s deadline=%d", args, *job.Spec.ActiveDeadlineSeconds)
	}
	for _, mutate := range []func(*edge.DispatchOccupancyMeasurementSpec){
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.Workers = 5 },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.Conditions.InputsJSON = `[]` },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.PlannedElapsedSeconds = 3601 },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.DeliveryMarginMillis = 0 },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.DrainSeconds = 1 },
	} {
		bad := spec
		mutate(&bad)
		if bad.ValidateConditions() == nil {
			t.Fatalf("accepted infeasible conditions: %+v", bad)
		}
	}
}
