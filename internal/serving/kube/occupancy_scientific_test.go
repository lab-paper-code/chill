package kube

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const timeoutOutcome = "timeout"

func TestBurstPairThresholdRetainsStatistics(t *testing.T) {
	for _, pairs := range []int{0, 3, 4} {
		statistics := &serving.OccupancyStatistics{Mean: -12, Median: -10}
		if pairs == 0 {
			statistics = nil
		}
		record := BurstOccupancyRecord{SchemaVersion: BurstOccupancySchema, Estimate: &serving.OccupancyEstimate{Pairs: pairs, Statistics: statistics}}
		judgeOccupancy(&record, edge.DispatchOccupancyMeasurementSpec{MinimumPairs: 4})
		if record.Estimate.Statistics != statistics {
			t.Fatal("threshold changed observed statistics")
		}
		if pairs < 4 {
			if record.Verdict != servingops.MeasurementInsufficient || record.Reason != "TooFewPairs" {
				t.Fatalf("missing pair verdict: %+v", record)
			}
		} else if record.Verdict != "Observed" {
			t.Fatalf("observation must not imply scheduler admission: %+v", record)
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "perRequestNanos") || strings.Contains(string(raw), "idleFraction") {
			t.Fatalf("legacy scalar leaked into burst: %s", raw)
		}
		if pairs == 0 && !strings.Contains(string(raw), `"statistics":null`) {
			t.Fatalf("missing statistics must be null: %s", raw)
		}
	}
	if BurstOccupancyPrefix == DispatchOccupancyPrefix || BurstOccupancySchema == DispatchOccupancySchema {
		t.Fatal("burst overwrites scheduler evidence contract")
	}
}

func TestBurstOperationalFailuresRetainEstimate(t *testing.T) {
	record := BurstOccupancyRecord{Estimate: &serving.OccupancyEstimate{Pairs: 20, Statistics: &serving.OccupancyStatistics{Mean: 42}}}
	occupancyReason(&record, "PhysicalDrainUnconfirmed")
	occupancyReason(&record, "PhysicalDrainUnconfirmed")
	judgeOccupancy(&record, edge.DispatchOccupancyMeasurementSpec{MinimumPairs: 4})
	if record.Verdict != servingops.MeasurementInsufficient || len(record.Reasons) != 1 || record.Estimate.Statistics.Mean != 42 {
		t.Fatalf("lost evidence: %+v", record)
	}
}

func TestBurstConfiguredJobAndElapsedBudget(t *testing.T) {
	m := occupancyRoot("burst", time.Now(), 1)
	if err := m.Spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	plan := occupancyPlan{workload: &edge.Workload{}, dataset: &edge.Dataset{}, fields: []string{"x"}}
	for _, seed := range []uint64{0, 12345} {
		m.Spec.Seed = seed
		job := loadJob(m, testNode, plan, LoadGeneratorConfig{})
		args := job.Spec.Template.Spec.Containers[0].Args
		seedAt := slices.Index(args, "--seed")
		if seedAt < 0 || args[seedAt+1] != strconv.FormatUint(seed, 10) {
			t.Fatalf("configured seed missing: %v", args)
		}
		at := slices.Index(args, "--duration")
		if at < 0 || args[at+1] != "5.010" {
			t.Fatalf("supply extension missing: %v", args)
		}
		at = slices.Index(args, "--timeout")
		if at < 0 || args[at+1] != "2.011" {
			t.Fatalf("timeout must exceed lifetime and delivery: %v", args)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--arrivals closed-loop") || strings.Contains(joined, "--rate") {
			t.Fatalf("not a closed-loop burst: %v", args)
		}
		if *job.Spec.ActiveDeadlineSeconds != 9 {
			t.Fatalf("deadline %d", *job.Spec.ActiveDeadlineSeconds)
		}
	}
	for _, margin := range []int32{1, 1000, 1001} {
		bounded := m.Spec
		bounded.DeliveryMarginMillis = margin
		bounded.DrainSeconds = 5
		extension := bounded.RequestLifetimeSeconds + (margin+999)/1000
		bounded.PlannedElapsedSeconds = bounded.PreparationSeconds + bounded.JobSetupSeconds + bounded.CollectionSeconds + bounded.WarmupSeconds + bounded.DurationSeconds + extension + bounded.DrainSeconds
		if err := bounded.ValidateConditions(); err != nil {
			t.Fatal(err)
		}
		job := loadJob(&edge.DispatchOccupancyMeasurement{Spec: bounded}, testNode, plan, LoadGeneratorConfig{})
		if int64(bounded.PreparationSeconds)+*job.Spec.ActiveDeadlineSeconds != int64(bounded.PlannedElapsedSeconds) {
			t.Fatal("job and elapsed budget diverge")
		}
		bounded.PlannedElapsedSeconds--
		if bounded.ValidateConditions() == nil {
			t.Fatal("accepted elapsed budget missing one second")
		}
	}
}

func TestBurstFrozenProfileIdentity(t *testing.T) {
	m := occupancyRoot("burst", time.Now(), 1)
	prep := measuredCPUPreparationFixture()
	p := prep.Instance.Profile
	p.ID = m.Spec.Conditions.ProfileID
	w := &edge.Workload{}
	w.Name = "plc"
	if err := validateOccupancyProfile(m.Spec, p, w, prep); err != nil {
		t.Fatal(err)
	}
	w.Generation++
	if validateOccupancyProfile(m.Spec, p, w, prep) == nil {
		t.Fatal("accepted stale workload")
	}
}

func TestBurstWarmupOriginJoinsLateNativeReceiptCohort(t *testing.T) {
	ctx := context.Background()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: managerArchive{t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	now := time.Now().UTC()
	started, collected := now.Add(-10*time.Second), now.Add(time.Second)
	profile := measuredCPUPreparationFixture().Instance.Profile
	workload := &edge.Workload{ObjectMeta: meta.ObjectMeta{Name: "plc", UID: "warmup-workload"}}
	m := occupancyRootWithInputs("warmup", started, 1, profile, workload)
	firstStart := started.Add(100 * time.Millisecond)
	progress := func(id string, batch uint64, at time.Time) serving.RequestEvent {
		return serving.RequestEvent{RequestID: id, WorkloadUID: string(workload.UID), Kind: "backend_running",
			At: now, NativeInstance: "warmup-instance",
			NativeProgress: &serving.BackendProgress{RequestID: id, WorkloadUID: string(workload.UID),
				Phase: "running", At: at, BatchID: batch, BatchSize: 1, ProfileID: profile.ID}}
	}
	for index, request := range []struct {
		id                string
		received, running time.Time
		late              bool
	}{
		// An earlier native start from outside this Job must not set its origin.
		{"previous-job", started.Add(-time.Second), started.Add(50 * time.Millisecond), false},
		{"earliest", started.Add(10 * time.Millisecond), firstStart, true},
		{"later", started.Add(20 * time.Millisecond), started.Add(700 * time.Millisecond), false},
	} {
		events := []serving.RequestEvent{
			{RequestID: request.id, WorkloadUID: string(workload.UID), Kind: "received", At: request.received, ReceivedAt: request.received},
			{RequestID: request.id, WorkloadUID: string(workload.UID), Kind: "assigned", At: request.received,
				Assignment: &serving.Assignment{Node: testNode, Instance: "warmup-instance", Profile: profile.ID, DispatchedAt: request.received}},
		}
		if !request.late {
			events = append(events, progress(request.id, uint64(index+1), request.running))
		}
		events = append(events, serving.RequestEvent{RequestID: request.id, Kind: "response_finished",
			At: now, Outcome: timeoutOutcome})
		for _, event := range events {
			if err := log.RequestEvent(event); err != nil {
				t.Fatal(err)
			}
		}
		if err := log.RequestClosed(request.id); err != nil {
			t.Fatal(err)
		}
	}
	// The earliest final lacks running; its later supplement has ReceivedAt zero.
	if err := log.RecordNativeSupplement(progress("earliest", 2, firstStart)); err != nil {
		t.Fatal(err)
	}
	controller := &SchedulerController{reservationState: &servingstate.ReservationState{}, observationLog: log}
	record, err := controller.collectOccupancy(ctx, m, testNode, occupancyPlan{profile: profile, workload: workload},
		started, collected, &servingstate.OperationState{})
	if err != nil {
		t.Fatal(err)
	}
	wantStart := firstStart.Add(time.Duration(m.Spec.WarmupSeconds) * time.Second)
	wantEnd := wantStart.Add(time.Duration(m.Spec.DurationSeconds) * time.Second)
	if record.Estimate == nil || !record.Estimate.Window.Start.Equal(wantStart) || !record.Estimate.Window.End.Equal(wantEnd) {
		t.Fatalf("warmup origin did not join late native evidence: estimate=%+v want=[%s,%s)", record.Estimate, wantStart, wantEnd)
	}
	if slices.Contains(record.Reasons, "MissingPhysicalEvidence") {
		t.Fatalf("joined native origin reported missing: %v", record.Reasons)
	}
}
