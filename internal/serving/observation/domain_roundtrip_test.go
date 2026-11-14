package observation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestCapacityArchiveCutoverDoesNotLoadControlledExecutionRows(t *testing.T) {
	settings, snapshot := capacityFixture()
	input := serving.NewCapacityInput(settings, snapshot.DispatchOccupancy, snapshot.Options[0], snapshot.Workloads[0])
	// Before the shared-execution cutover the input hash had no namespace.
	payload, _ := json.Marshal(input)
	sum := sha256.Sum256(payload)
	oldKey := hex.EncodeToString(sum[:12])
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	old := serving.CapacityRow{Key: oldKey, Status: serving.CapacityRejected, Reason: "capacity replay: MemoryBudgetInfeasible:n1"}
	if err := observationLog.ManagementEvent(serving.ManagementEvent{WorkloadUID: "w1", Kind: serving.CapacityRowBuiltEvent, At: observationTestHour, Capacity: &old}); err != nil {
		t.Fatal(err)
	}
	rows, err := observationLog.CapacityRecords(context.Background(), []string{input.Key()})
	if err != nil || len(rows) != 0 {
		t.Fatalf("controlled execution row crossed archive cutover: %+v %v", rows, err)
	}
	table := serving.NewCapacityTable(1, nil, observationLog.CapacityRecords)
	table.Ensure(context.Background(), settings, snapshot)
	table.Wait()
	if row := table.Rows()[0]; row.Status != serving.CapacityBuilt || row.Previous {
		t.Fatalf("old archived rejection survived fresh build: %+v", row)
	}
}

func TestCapacityRecordsReturnLatestRowPerKey(t *testing.T) {
	observationLog, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	for _, row := range []serving.CapacityRow{{Key: "a", Mu: 1}, {Key: "b", Mu: 2}, {Key: "a", Mu: 3}} {
		if err := observationLog.ManagementEvent(serving.ManagementEvent{WorkloadUID: "w1", Kind: serving.CapacityRowBuiltEvent, At: observationTestHour, Capacity: &row}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := observationLog.CapacityRecords(context.Background(), []string{"a", "c"})
	if err != nil || len(rows) != 1 || rows["a"].Mu != 3 {
		t.Fatalf("latest recorded row per asked key: %+v %v", rows, err)
	}
}

func TestDecisionRecordIncludesSelectedDOMAndDefaultOccupancy(t *testing.T) {
	for _, source := range []string{"dom", "default"} {
		t.Run(source, func(t *testing.T) {
			settings, snapshot := capacityFixture()
			settings.DefaultDispatchOccupancyNanos = int64(2 * time.Millisecond)
			expected := int64(time.Millisecond)
			snapshot.DispatchOccupancy[0].PerRequest = time.Millisecond
			if source == "default" {
				snapshot.DispatchOccupancy = nil
				expected = settings.DefaultDispatchOccupancyNanos
			}
			// Cover deduplication across the current instance and matching option.
			snapshot.At = observationTestHour
			log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
			decision := serving.SchedulerDecision{At: observationTestHour, Settings: settings}
			if err := log.SchedulerRecord(SchedulerObservation{At: observationTestHour, Decision: &decision, Snapshot: &snapshot}); err != nil {
				t.Fatal(err)
			}
			page, _, _, _, err := log.SchedulerObservationPage(context.Background(), observationTestHour, observationTestHour.Add(time.Second), 1, 0, SchedulerObservationSelection{})
			if err != nil || len(page) != 1 || page[0].Decision == nil || page[0].Snapshot == nil || len(page[0].Snapshot.DispatchOccupancy) != 1 {
				t.Fatalf("missing recorded decision occupancy: %+v %v", page, err)
			}
			d := page[0].Snapshot.DispatchOccupancy[0]
			if d.Provenance == nil || d.Provenance.Source != source || d.Provenance.Value != expected || int64(d.PerRequest) != expected {
				t.Fatalf("recorded decision lost selected source/value: %+v", d)
			}
			recorded := page[0].Snapshot
			option := recorded.Options[0]
			option.Instance.Profile = snapshot.Options[0].Instance.Profile
			roundtrip := serving.NewCapacityInput(page[0].Decision.Settings, recorded.DispatchOccupancy, option, snapshot.Workloads[0])
			if !reflect.DeepEqual(*roundtrip.DispatchOccupancy, d) || roundtrip.DispatchOccupancy.Provenance == d.Provenance {
				t.Fatalf("record/read/capacity roundtrip lost source/value or aliased provenance: %+v want %+v", roundtrip.DispatchOccupancy, d)
			}
			if source == "dom" && snapshot.DispatchOccupancy[0].Provenance != nil || source == "default" && snapshot.DispatchOccupancy != nil {
				t.Fatal("decision recording mutated incoming evidence")
			}
			// Empty pools still record the default selected by their option.
			snapshot.Instances = nil
			if got := serving.SchedulerSelectedOccupancy(settings, snapshot); len(got) != 1 || got[0].Provenance.Source != source {
				t.Fatalf("option-only decision lost provenance: %+v", got)
			}
		})
	}
}

func TestSelectedDispatchOccupancyProvenanceSurvivesDecisionRecord(t *testing.T) {
	settings, snapshot := capacityFixture()
	settings.DefaultDispatchOccupancyNanos = int64(2 * time.Millisecond)
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	instance := snapshot.Options[0].Instance
	dom := snapshot.DispatchOccupancy[0]
	measured := *serving.ResolveDispatchOccupancy(settings, []serving.DispatchOccupancy{dom}, instance)
	window := serving.OccupancyWindow{Start: observationTestHour.Add(-time.Minute), End: observationTestHour}
	measured.PerRequest = 500000
	measured.Source = "rolling"
	measured.Provenance = &serving.DispatchOccupancyProvenance{Source: "measured", Pairs: 1000, Window: &window, UpdatedAt: observationTestHour, Value: 500000}
	for n, tc := range []struct {
		source   string
		incoming []serving.DispatchOccupancy
	}{
		{"measured", []serving.DispatchOccupancy{dom, measured}},
		{"dom", []serving.DispatchOccupancy{dom}},
		{"default", nil},
	} {
		at := observationTestHour.Add(time.Duration(n) * time.Millisecond)
		selected := serving.ResolveDispatchOccupancy(settings, tc.incoming, instance)
		recordedSnapshot := serving.SchedulerSnapshot{At: at, DispatchOccupancy: []serving.DispatchOccupancy{*selected}}
		decision := serving.SchedulerDecision{At: at, Settings: settings}
		if err := log.SchedulerRecord(SchedulerObservation{At: at, Decision: &decision, Snapshot: &recordedSnapshot}); err != nil {
			t.Fatal(err)
		}
		page, _, _, _, err := log.SchedulerObservationPage(context.Background(), at, at.Add(time.Millisecond), 1, 0, SchedulerObservationSelection{})
		if err != nil || len(page) != 1 || page[0].Snapshot == nil || len(page[0].Snapshot.DispatchOccupancy) != 1 {
			t.Fatalf("missing %s decision snapshot: %+v %v", tc.source, page, err)
		}
		got := page[0].Snapshot.DispatchOccupancy[0]
		if got.Provenance.Source != tc.source || !reflect.DeepEqual(got, *selected) {
			t.Fatalf("%s selection lost provenance during recording: %+v want %+v", tc.source, got, selected)
		}
	}
}

func capacityFixture() (serving.SchedulerSettings, serving.SchedulerSnapshot) {
	s, snap := schedulerFixture()
	s.CapacityWindow, s.CapacityWindows, s.CapacityPassingWindows, s.CapacityMinimumRequests = time.Second, 3, 2, 200
	s.CompletionMarginMS = 0
	c := snap.Instances[0].Profile.Condition
	snap.DispatchOccupancy = []serving.DispatchOccupancy{{
		Node: snap.Nodes[0].Node, PowerMode: c.PowerMode, RuntimeImage: c.RuntimeImage, Model: c.LogicalModel,
		ProfileID: snap.Instances[0].Profile.ID, ExecutionPath: c.Artifact.ExecutionPath, Batch: snap.Instances[0].MaxBatch,
		Source: "fixture",
	}}
	p := *snap.Instances[0].Profile
	p.Batches = []serving.BatchProfile{{Batch: 1, ServiceSamples: []firstfloor.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 8e5), fixtures.ServiceSample("physical-trial", 1, 1e6), fixtures.ServiceSample("physical-trial", 2, 1.2e6)}, InputSlopeJoules: observationTestSlope(`{"status":"Identified","value":0.01}`), MemoryBytes: 100}}
	distribution, err := firstfloor.NewServiceDistribution(p.Batches[0].ServiceSamples)
	if err != nil {
		panic(err)
	}
	p.Batches[0].ServiceDistribution = distribution
	snap.Instances[0].Profile = &p
	snap.Options[0].Instance = snap.Instances[0]
	snap.Workloads[0].SLO = serving.SLO{Percentile: 95, TargetLatency: 20 * time.Millisecond}
	return s, snap
}

func schedulerFixture() (serving.SchedulerSettings, serving.SchedulerSnapshot) {
	at := time.Unix(100, 0)
	s := serving.SchedulerSettings{ObservationWindow: 100 * time.Millisecond, ControlInterval: time.Second, ComparisonPeriod: 100 * time.Millisecond, MaxReplicas: 2, MaxInstances: 4, AutonomousWorkloads: []string{"w1"}, CapacityWindow: time.Second, CapacityWindows: 3, CapacityPassingWindows: 2, CapacityMinimumRequests: 50, PlanSearchWorkers: 2, Controls: serving.SchedulerControls{Placement: true, Resources: true, Replicas: true, Routing: true, Batching: true, Power: true, Membership: true}}
	estimates := schedulerEstimateSettingsForTest()
	s.DefaultOperationCost, s.DefaultIdlePower = estimates.DefaultOperationCost, estimates.DefaultIdlePower
	s.CapacityGridSteps, s.CapacityBisectionWidth, s.CapacityTailSamples = 20, .005, 100
	node := serving.SchedulerNode{Node: serving.NodeReference{Name: "node1", UID: "n1"}, PowerMode: "fixed", Ready: true, MemoryBytes: 10000, ObservedAt: at}
	image := "registry/model@sha256:" + strings.Repeat("a", 64)
	settings := &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 100}
	path := serving.ModelPath{Name: "cpu", Artifact: "model", Files: []string{"/model"}, Format: "onnx", Image: image, RuntimeFamily: "firstfloor", Backend: "ort-cpu", Firstfloor: settings}
	d := &serving.Definition{Name: "workload", UID: "w1", Generation: 1, Admission: serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024}, Model: serving.Model{Name: "model", UID: "m", Generation: 1, Paths: []serving.ModelPath{path}}, Preprocessor: serving.Preprocessor{Name: "pre", Input: []serving.RawField{{Name: "x", Format: "UTF8", MaxBytes: 16}}, Output: []serving.RawField{{Name: "y", Format: "UTF8"}}}, Dataset: &serving.DatasetReference{Name: "data", UID: "data-uid"}, SLO: serving.SLO{Percentile: 95, TargetLatency: 100 * time.Millisecond}}
	p := &serving.Profile{ID: "profile", NodeName: node.Node.Name, Condition: derivation.CurvePlanSpec{LogicalModel: "model", NodeUID: node.Node.UID, DeviceClass: "cpu", RuntimeFamily: "firstfloor", Backend: "ort-cpu", RuntimeImage: image, Firstfloor: settings, PowerMode: "fixed", PlannedBatchSizes: []int{1}, Preprocessor: derivation.CurvePreprocessorContract{Name: "pre", Inputs: []string{"x"}, Outputs: []string{"y"}}, ActiveProtocol: derivation.CurveProtocolContract{Dataset: &derivation.CurveDatasetContract{Name: "data", UID: "data-uid"}}, Artifact: derivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Files: []string{"/model"}, Image: image}}, IdleBaselineWatts: 12, MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "curve", Size: 1}, Evidence: []evidencestore.ObjectIdentity{{Store: "raw", Key: "profile", Size: 1}}, Batches: []serving.BatchProfile{{Batch: 1, ServiceSamples: fixtures.ServiceSamples("physical-trial", int64(10*time.Millisecond)), InputSlopeJoules: observationTestSlope(`{"status":"Identified","value":1}`), MemoryBytes: 100}}}
	distribution, err := firstfloor.NewServiceDistribution(p.Batches[0].ServiceSamples)
	if err != nil {
		panic(err)
	}
	p.Batches[0].ServiceDistribution = distribution
	p.RuntimeMetadata = &firstfloor.RuntimeMetadata{Runtime: "onnxruntime", RuntimeVersion: "1.19.0", ModelFiles: []string{"/model/model"}, Inputs: []string{"x"}, Outputs: []string{"y"}, Providers: []string{"CPUExecutionProvider"}, Device: "cpu", CPUAffinity: []int{0}, IntraOpThreads: 1, InterOpThreads: 1, InitializedBatches: []int{1}, WarmupIterations: 1}
	i := serving.SchedulerInstance{ID: "i1", Node: node.Node, Profile: p, WorkloadUID: d.UID, Allocation: serving.SchedulerAllocation{MemoryBytes: 1000}, Ready: true, Routable: map[string]bool{d.UID: true}, Capacity: 100, Weight: 1, MaxBatch: 1, ObservedAt: at, IdleMeter: "wall-n1", IdleScope: "node-exclusive-wall"}
	b := serving.NodeBaseline{ID: "unloaded-n1", Node: node.Node, PowerMode: "fixed", Watts: 10, Meter: "wall-n1", Scope: "node-exclusive-wall", MeasuredAt: at, Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "idle"}}
	b.Reference.Size = 1
	p.Evidence[0].Size = 1
	snap := serving.SchedulerSnapshot{At: at, ObservationEnd: at, Workloads: []*serving.Definition{d}, Nodes: []serving.SchedulerNode{node}, Instances: []serving.SchedulerInstance{i}, Options: []serving.SchedulerOption{{Instance: i, CompatibleUIDs: []string{d.UID}}}, Baselines: []serving.NodeBaseline{b}}
	return s, snap
}

// A ready actor was warmed, so it always reports its last service end.
func fixtureAdmissionNative(profile *serving.Profile) *firstfloor.State {
	return &firstfloor.State{Ready: true, LastServiceEndAgeNanos: new(int64), Metadata: firstfloor.Metadata{ProfileID: profile.ID,
		Policy: firstfloor.Policy{ProfileID: profile.ID, MaxBatchSize: 1}}}
}

func directFixture(batch int) (*serving.Profile, map[string]firstfloor.Metadata, time.Time) {
	at := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	p := &serving.Profile{ID: "profile", NodeName: "node", Batches: []serving.BatchProfile{{Batch: batch, ServiceSamples: []firstfloor.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 100*int64(batch)), fixtures.ServiceSample("physical-trial", 1, 100*int64(batch))}}}}
	distribution, err := firstfloor.NewServiceDistribution(p.Batches[0].ServiceSamples)
	if err != nil {
		panic(err)
	}
	p.Batches[0].ServiceDistribution = distribution
	p.Condition.NodeUID = "node-uid"
	p.Condition.LogicalModel = "model"
	p.Condition.PowerMode = "fixture-fixed-mode"
	p.Condition.RuntimeImage = "image"
	p.Condition.Artifact.ExecutionPath = "cpu"
	policies := map[string]firstfloor.Metadata{"instance": {ProfileID: p.ID, DecisionID: "decision", AppliedAt: at.Add(-time.Second), Policy: firstfloor.Policy{FixedBatchSize: batch}}}
	return p, policies, at
}

func occupancyScanRow(at time.Time, id uint64) serving.RequestRow {
	q, s, f := at, at, at.Add(20)
	// Receipt anchors incomplete-request facts; worker timestamps anchor execution.
	return serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: fmt.Sprintf("%d/0", id), ReceivedAt: at, Node: &serving.NodeReference{Name: "node", UID: "node-uid"}, Instance: "instance", WorkloadUID: "workload", ProfileID: "profile", DecisionID: "decision", DefinitionID: "definition", BatchID: id, BatchSize: 1, Outcome: "completed", ExecutionComplete: true, NativeOutcome: "completed", NativeExecutionComplete: true, NativeQueuedAt: &q, NativeRunningAt: &s, NativeFinishedAt: &f}
}

func observationTestSlope(wire string) *derivation.SessionParameter {
	var p derivation.SessionParameter
	if err := json.Unmarshal([]byte(wire), &p); err != nil {
		panic(err)
	}
	return &p
}
