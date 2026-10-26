package kube

import (
	"context"
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
	m.Spec.PlannedElapsedSeconds = 546
	m.Spec.Conditions = edge.DispatchOccupancyConditions{ProfileID: "profile", InputsJSON: `{"computed":{"tailBatchSeconds":0.1,"fastBatchSeconds":0.02,"refillSeconds":0.1,"prefixSeconds":0.1,"framedDecodedRequestUpperBytes":32,"queueRequestCap":256,"queueBytesCap":67108864,"qUpper":256},"coefficients":{"relativeXPlanningPrecision":0.01,"planningConfidence":0.95,"timeMargin":1.25,"maximumIdleBaseFraction":0.01,"idleDErrorSecondsPerRequest":0.001,"windowFloorSeconds":360,"loadPreparationFloorSeconds":60,"plannedElapsedCapSeconds":3600},"profile":{"profileID":"profile","runtimeImage":"image","workload":{"name":"workload","uid":"workload-uid","generation":1,"slo":{"percentile":99,"targetLatencyNanos":1000000000}}}}`, WaitingBatches: 5, MinimumCycles: 119, MaximumIdleNanosPerRequest: 1000000}
	if err := m.Spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	t.Run("gateway cap rejected before grant", func(t *testing.T) {
		exceedsCap := m.DeepCopy()
		exceedsCap.Spec.RequestLifetimeSeconds, exceedsCap.Spec.DrainSeconds = 130, 131
		exceedsCap.Spec.WarmupSeconds, exceedsCap.Spec.PlannedElapsedSeconds = 164, 906
		if err := exceedsCap.Spec.ValidateConditions(); err != nil {
			t.Fatalf("fixture must satisfy the frozen conditions before gateway admission: %v", err)
		}
		controller := &SchedulerController{}
		_, reason, detail, wait := controller.occupancyRefusal(context.Background(), exceedsCap, nil,
			admissionInputs{config: SchedulerControllerConfig{GatewayRequestTimeout: 129 * time.Second}})
		if reason != occupancyConditionsInfeasible || wait || !strings.Contains(detail, "gateway cap") {
			t.Fatalf("impossible lifetime reached grant prerequisites: reason=%s detail=%s wait=%t", reason, detail, wait)
		}
	})
	raw, err := json.Marshal(m.Spec)
	if err != nil {
		t.Fatal(err)
	}
	var spec edge.DispatchOccupancyMeasurementSpec
	if err = json.Unmarshal(raw, &spec); err != nil || spec != m.Spec {
		t.Fatalf("frozen roundtrip: %+v %v", spec, err)
	}
	t.Run("supply extension reserved inside elapsed cap", func(t *testing.T) {
		for _, margin := range []int32{1, 1000, 1001} {
			bounded := spec
			bounded.DeliveryMarginMillis, bounded.DrainSeconds = margin, 5
			bounded.PlannedElapsedSeconds = 3600
			extension := bounded.RequestLifetimeSeconds + (margin+999)/1000
			bounded.CollectionSeconds = bounded.PlannedElapsedSeconds - bounded.PreparationSeconds -
				bounded.JobSetupSeconds - bounded.WarmupSeconds - bounded.DurationSeconds - bounded.DrainSeconds - extension
			if err := bounded.ValidateConditions(); err != nil {
				t.Fatalf("margin=%d: exact cap including extension: %v", margin, err)
			}
			job := loadJob(&edge.DispatchOccupancyMeasurement{Spec: bounded}, testNode,
				occupancyPlan{workload: &edge.Workload{}, dataset: &edge.Dataset{}}, 0, LoadGeneratorConfig{})
			if int64(bounded.PreparationSeconds)+*job.Spec.ActiveDeadlineSeconds != int64(bounded.PlannedElapsedSeconds) {
				t.Fatalf("margin=%d: load deadline and elapsed budget disagree", margin)
			}
			bounded.CollectionSeconds++
			if bounded.ValidateConditions() == nil {
				t.Fatalf("margin=%d: accepted one second beyond the elapsed cap", margin)
			}
			bounded.CollectionSeconds += extension - 1
			if bounded.ValidateConditions() == nil {
				t.Fatalf("margin=%d: accepted the old budget without supply extension", margin)
			}
		}
	})
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
	// The frozen configuration is authoritative; these values are a fixture,
	// not an alternate measurement condition emitted to the campaign.
	configured := spec
	configured.WarmupSeconds, configured.DurationSeconds, configured.PlannedElapsedSeconds = 30, 30, 4500
	configured.Conditions.InputsJSON = strings.NewReplacer(
		`"windowFloorSeconds":360`, `"windowFloorSeconds":30`,
		`"loadPreparationFloorSeconds":60`, `"loadPreparationFloorSeconds":30`,
		`"plannedElapsedCapSeconds":3600`, `"plannedElapsedCapSeconds":5000`,
	).Replace(spec.Conditions.InputsJSON)
	if err := configured.ValidateConditions(); err != nil {
		t.Fatalf("configured planning floors: %v", err)
	}
	if err := validateOccupancyProfile(configured, profile, w, prep); err != nil {
		t.Fatalf("controller ignored configured window floor: %v", err)
	}
	legacy := configured
	legacy.SupplyMode, legacy.RateFactor = "poisson", "1.5"
	if legacy.ValidateConditions() == nil {
		t.Fatal("legacy open-loop duration lost its independent 60-second floor")
	}
	legacy.DurationSeconds = 60
	if err := legacy.ValidateConditions(); err != nil {
		t.Fatalf("legacy open-loop contract changed: %v", err)
	}
	higherFloor := spec
	higherFloor.Conditions.InputsJSON = strings.Replace(spec.Conditions.InputsJSON, `"windowFloorSeconds":360`, `"windowFloorSeconds":480`, 1)
	if higherFloor.ValidateConditions() == nil || validateOccupancyProfile(higherFloor, profile, w, prep) == nil {
		t.Fatal("API or controller ignored the frozen higher window floor")
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
	if !strings.Contains(args, "--arrivals closed-loop") || !strings.Contains(args, "--request-lifetime 2") || !strings.Contains(args, "--delivery-margin 0.010") || strings.Contains(args, "--rate") || *job.Spec.ActiveDeadlineSeconds != 486 {
		t.Fatalf("closed loop Job: %s deadline=%d", args, *job.Spec.ActiveDeadlineSeconds)
	}
	for _, mutate := range []func(*edge.DispatchOccupancyMeasurementSpec){
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.Workers = 5 },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.Conditions.InputsJSON = `[]` },
		func(s *edge.DispatchOccupancyMeasurementSpec) {
			s.Conditions.InputsJSON = strings.Replace(s.Conditions.InputsJSON, `"loadPreparationFloorSeconds":60`, `"loadPreparationFloorSeconds":90`, 1)
		},
		func(s *edge.DispatchOccupancyMeasurementSpec) {
			s.Conditions.InputsJSON = strings.Replace(s.Conditions.InputsJSON, `"plannedElapsedCapSeconds":3600`, `"plannedElapsedCapSeconds":0`, 1)
		},
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

func TestOccupancyLoadSupplyCoversPhysicalWindow(t *testing.T) {
	m := occupancyRoot("supply-window", time.Now(), 1)
	m.Spec.SupplyMode = occupancyClosedLoop
	m.Spec.WarmupSeconds, m.Spec.DurationSeconds = 60, 360
	m.Spec.RequestLifetimeSeconds, m.Spec.DeliveryMarginMillis = 2, 10
	plan := occupancyPlan{workload: &edge.Workload{}, dataset: &edge.Dataset{}}
	job := loadJob(m, testNode, plan, 0, LoadGeneratorConfig{})
	args := job.Spec.Template.Spec.Containers[0].Args
	at := slices.Index(args, "--duration")
	if at < 0 || args[at+1] != "422.010" {
		t.Fatalf("supply must exceed warmup+window by lifetime+delivery: %v", args)
	}
	wantDeadline := int64(m.Spec.JobSetupSeconds) + 423 + int64(m.Spec.DrainSeconds) + int64(m.Spec.CollectionSeconds)
	if *job.Spec.ActiveDeadlineSeconds != wantDeadline {
		t.Fatalf("deadline=%d, want %d", *job.Spec.ActiveDeadlineSeconds, wantDeadline)
	}
}
