package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestLoadSchedulerHydratesEvidenceThroughTick(t *testing.T) {
	settings, snapshot := hydrationTickFixture()
	name := writeSchedulerDump(t, settings, snapshot)
	loaded, decodedSettings, err := loadScheduler(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []*serving.Profile{loaded.Instances[0].Profile, loaded.Options[0].Instance.Profile} {
		if profile.Batches[0].ServiceDistribution == nil || !reflect.DeepEqual(profile.Batches[0].ServiceSamples, snapshot.Instances[0].Profile.Batches[0].ServiceSamples) {
			t.Fatal("load did not hydrate the captured raw pairs unchanged")
		}
	}
	decision, err := serving.EvaluateScheduler(context.Background(), decodedSettings, loaded)
	if err != nil || decision.Target == nil || len(decision.Keep.Feedback) != 1 {
		t.Fatalf("loaded snapshot failed direct Tick assessment: decision=%+v err=%v", decision, err)
	}
	score := decision.Keep.Feedback[0]
	if score.Population != 1 || score.OnTime != 1 || !score.Satisfied {
		t.Fatalf("decoded service evidence never reached native replay: %+v", score)
	}
	if err := tick(decodedSettings, loaded, 0, 1); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSchedulerPropagatesInvalidPairedEvidence(t *testing.T) {
	for _, location := range []string{"instance", "option"} {
		t.Run(location, func(t *testing.T) {
			settings, snapshot := hydrationTickFixture()
			invalid := *snapshot.Instances[0].Profile
			invalid.Batches = []serving.BatchProfile{{Batch: 1, ServiceSamples: []firstfloor.ServiceSample{{TrialID: "invalid-capture", ServiceDurationNanos: -1}}}}
			if location == "instance" {
				snapshot.Instances[0].Profile = &invalid
			} else {
				snapshot.Options[0].Instance.Profile = &invalid
			}
			if _, _, err := loadScheduler(writeSchedulerDump(t, settings, snapshot)); err == nil {
				t.Fatal("invalid captured raw evidence crossed the load boundary")
			}
		})
	}
}

func writeSchedulerDump(t *testing.T, settings serving.SchedulerSettings, snapshot serving.SchedulerSnapshot) string {
	t.Helper()
	dump := struct {
		Settings serving.SchedulerSettings `json:"settings"`
		State    struct {
			Snapshot serving.SchedulerSnapshot `json:"snapshot"`
		} `json:"state"`
	}{Settings: settings}
	dump.State.Snapshot = snapshot
	raw, err := json.Marshal(dump)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "scheduler.json")
	if err := os.WriteFile(name, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return name
}

func hydrationTickFixture() (serving.SchedulerSettings, serving.SchedulerSnapshot) {
	at := time.Unix(100, 0)
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		MaxReplicas: 1, MaxInstances: 1, AutonomousWorkloads: []string{"w"}, MinimumDemand: map[string]float64{"w": 1},
		CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1,
		CapacityGridSteps: 1, CapacityBisectionWidth: .1, CapacityTailSamples: 1,
		PlanSearchWorkers: 1, PlanSearchStrategy: "v2", PlanSearchEvaluations: 1, PlanExactFinalists: 1,
		DefaultOperationCost: map[string]serving.OperationCostValue{"prepare": {Seconds: 1}, "remove": {Seconds: 1}, "power": {Seconds: 1}},
		DefaultIdlePower:     &serving.IdlePowerDefault{NodeWatts: 1, PerInstanceWatts: 1},
		Controls:             serving.SchedulerControls{Routing: true},
	}
	node := serving.SchedulerNode{Node: serving.NodeReference{Name: "n", UID: "n-uid"}, PowerMode: "fixed", Ready: true, MemoryBytes: 1024, ObservedAt: at}
	definition := &serving.Definition{UID: "w", Model: serving.Model{Name: "model", UID: "model-uid", Paths: []serving.ModelPath{{Name: "cpu", Artifact: "model", Files: []string{"/model"}, RuntimeFamily: "firstfloor", Backend: "ort-cpu"}}}, SLO: serving.SLO{Percentile: 100, TargetLatency: time.Second}, Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024}}
	profile := &serving.Profile{
		ID: "captured-profile", NodeName: node.Node.Name,
		Condition: profilederivation.CurvePlanSpec{
			LogicalModel: "model", NodeUID: node.Node.UID, PowerMode: node.PowerMode, RuntimeFamily: "firstfloor", Backend: "ort-cpu",
			Artifact: profilederivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Files: []string{"/model"}},
		},
		Batches: []serving.BatchProfile{{
			Batch: 1, MemoryBytes: 1, EnergyPerInputJoules: 1,
			ServiceSamples: []firstfloor.ServiceSample{
				fixtures.ServiceSample("captured-trial", 0, int64(time.Millisecond)),
				fixtures.ServiceSample("captured-trial", 1, int64(time.Millisecond)),
			},
		}},
	}
	instance := serving.SchedulerInstance{ID: "i", Node: node.Node, Profile: profile, WorkloadUID: definition.UID, Allocation: serving.SchedulerAllocation{MemoryBytes: 1}, Ready: true, Routable: map[string]bool{definition.UID: true}, Capacity: 1, Weight: 1000, MaxBatch: 1, ObservedAt: at, ArrivalAgesMS: map[string][]float64{}}
	return settings, serving.SchedulerSnapshot{At: at, ObservationEnd: at, ObservedSince: at.Add(-time.Second), Workloads: []*serving.Definition{definition}, Nodes: []serving.SchedulerNode{node}, Instances: []serving.SchedulerInstance{instance}, Options: []serving.SchedulerOption{{Instance: instance, CompatibleUIDs: []string{definition.UID}}}, Routing: map[string]map[string]int32{definition.UID: {instance.ID: 1000}}}
}
