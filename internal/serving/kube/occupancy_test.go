package kube

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func occupancyRoot(name string, created time.Time, b int32) *edge.DispatchOccupancyMeasurement {
	profile := measuredCPUPreparationFixture().Instance.Profile
	profile.ID = "chill-system/beo/beo-uid"
	return occupancyRootWithInputs(name, created, b, profile, &edge.Workload{ObjectMeta: meta.ObjectMeta{Name: "plc"}})
}

func occupancyRootWithInputs(name string, created time.Time, b int32, profile *serving.Profile, workload *edge.Workload) *edge.DispatchOccupancyMeasurement {
	inputs, _ := json.Marshal(map[string]any{
		"coefficients": map[string]any{"warmupSeconds": 1, "burstSeconds": 2, "workers": b, "minimumPairs": 1, "plannedElapsedCapSeconds": 60},
		"computed":     map[string]any{"tailBatchSeconds": .03, "framedDecodedRequestUpperBytes": 32, "queueRequestCap": 256, "queueBytesCap": 64 << 20},
		"profile": map[string]any{"profileID": profile.ID, "runtimeImage": profile.Condition.RuntimeImage,
			"workload": map[string]any{"name": workload.Name, "uid": workload.UID, "generation": workload.Generation,
				"slo": map[string]any{"percentile": workload.Spec.SLO.Percentile, "targetLatencyNanos": workload.Spec.SLO.TargetLatency.Duration.Nanoseconds()}}},
	})
	return &edge.DispatchOccupancyMeasurement{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: name, CreationTimestamp: meta.NewTime(created)},
		Spec: edge.DispatchOccupancyMeasurementSpec{Node: "node", BatchEnergyOptimization: "beo", Workload: "plc", Batch: b,
			WarmupSeconds: 1, DurationSeconds: 2, MinimumPairs: 1, Workers: b, SupplyMode: occupancyClosedLoop,
			RequestLifetimeSeconds: 2, DeliveryMarginMillis: 10, DrainSeconds: 3, PreparationSeconds: 1,
			PlannedElapsedSeconds: 12, Conditions: edge.DispatchOccupancyConditions{ProfileID: profile.ID, InputsJSON: string(inputs)}}}
}

// occupancyHarness adds the Workload "plc" with its Dataset and Preprocessor,
// the load generator configuration and a gateway Node "gw".
func occupancyHarness(t *testing.T) *opcostHarness {
	t.Helper()
	o := newOpcostHarness(t)
	gw := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "gw", UID: "gw-uid"}}
	if _, err := o.kube.CoreV1().Nodes().Create(context.Background(), gw, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	o.create(&edge.Dataset{ObjectMeta: meta.ObjectMeta{Name: "vqa"}, Spec: edge.DatasetSpec{Source: edge.DatasetSource{Image: "data:1", Path: "/records"}, Fields: []edge.RawField{{Name: "image"}, {Name: "question"}}}},
		&edge.Preprocessor{ObjectMeta: meta.ObjectMeta{Name: "pre"}, Spec: edge.PreprocessorSpec{Input: edge.RawInputContract{Fields: []edge.RawField{{Name: "image"}}}}},
		&edge.Workload{ObjectMeta: meta.ObjectMeta{Name: "plc"}, Spec: edge.WorkloadSpec{ModelRef: edge.CatalogReference{Name: "m"}, PreprocessorRef: edge.CatalogReference{Name: "pre"}, DatasetRef: &edge.CatalogReference{Name: "vqa"}}})
	o.controller.mu.Lock()
	o.controller.config.GatewayRequestTimeout = 2 * time.Second
	o.controller.config.LoadGenerator = LoadGeneratorConfig{Image: "load:1", ServiceAccount: "chill-load-generator", Namespace: "chill-system",
		Endpoint: "https://chill-serving-entrypoint.chill-system.svc:8080", CASecret: "serving-tls", NodeName: "gw"}
	o.controller.mu.Unlock()
	return o
}

func TestOccupancyRefusesUnmeasuredBatchAndWorkloads(t *testing.T) {
	for _, test := range []struct {
		name, want string
		batch      int32
		workload   string
		setup      func(*opcostHarness)
	}{
		{name: "batch", batch: 3, want: "BatchUnmeasured 3"},
		{name: "workload", batch: 1, workload: "absent", want: "WorkloadMissing absent"},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := occupancyHarness(t)
			if test.setup != nil {
				o.controller.mu.Lock()
				test.setup(o)
				o.controller.mu.Unlock()
			}
			m := occupancyRoot("occ", time.Now(), test.batch)
			if test.workload != "" {
				m.Spec.Workload = test.workload
			}
			o.create(m)
			o.admit()
			if got := o.admission(m); got != "Refused: "+test.want {
				t.Fatalf("admission %q", got)
			}
			if m.Status.Phase != edge.MeasurementPhaseFailed || m.Status.Reason != test.want {
				t.Fatalf("status %+v", m.Status)
			}
		})
	}
}

// Roots on the load Node wait while an occupancy load runs, and an occupancy
// waits while the load Node is measured.
func TestOccupancyIsolatesTheLoadNode(t *testing.T) {
	o := occupancyHarness(t)
	gw, err := o.kube.CoreV1().Nodes().Get(context.Background(), "gw", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	gw.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "busy", Effect: core.TaintEffectNoSchedule}}
	if _, err = o.kube.CoreV1().Nodes().Update(context.Background(), gw, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	// A run holds gw so its taint stays while the occupancy is judged.
	o.controller.startNodeRun(servingstate.Operation{ID: "gw-run", Kind: handBackKind, Node: serving.NodeReference{Name: "gw", UID: "gw-uid"}}, SchedulerControllerConfig{},
		func(context.Context, *servingstate.OperationResult) error { <-release; return nil })
	m := occupancyRoot("occ", time.Now(), 1)
	o.create(m)
	if err = o.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if o.holder() != "" || o.admission(m) != "" {
		t.Fatalf("occupancy granted while the load Node is measured: holder %q", o.holder())
	}
	o.controller.startNodeRun(servingstate.Operation{ID: "load", Kind: occupancyKind, Node: testNode}, SchedulerControllerConfig{},
		func(context.Context, *servingstate.OperationResult) error { <-release; return nil })
	gw.Spec.Taints = nil
	if _, err = o.kube.CoreV1().Nodes().Update(context.Background(), gw, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	onGateway := baselineRoot("gw-idle", time.Now())
	onGateway.Spec.State.TargetNode = "gw"
	o.create(onGateway)
	if err = o.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := o.admission(onGateway); got != "" {
		t.Fatalf("load Node root judged during a load: %q", got)
	}
}

// The hand-back removes the load Jobs of the Node.
func TestHandBackDeletesLoadJobs(t *testing.T) {
	o := occupancyHarness(t)
	job := &batch.Job{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "occupancy-x", Labels: map[string]string{loadNodeLabel: "node-uid"}}}
	other := &batch.Job{ObjectMeta: meta.ObjectMeta{Namespace: "chill-system", Name: "unrelated"}}
	for _, j := range []*batch.Job{job, other} {
		if _, err := o.kube.BatchV1().Jobs("chill-system").Create(context.Background(), j, meta.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	node := o.node.DeepCopy()
	node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "vanished", Effect: core.TaintEffectNoSchedule}}
	if _, err := o.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	o.admit()
	jobs, err := o.kube.BatchV1().Jobs("chill-system").List(context.Background(), meta.ListOptions{})
	if err != nil || len(jobs.Items) != 1 || jobs.Items[0].Name != "unrelated" || o.holder() != "" {
		t.Fatalf("jobs %+v err %v holder %q", jobs.Items, err, o.holder())
	}
}

// The planner reads the latest accepted record per Node, mode, image and
// model; a negative d counts as 0 and keeps its source key.
func TestCostEvidenceReadsLatestAcceptedOccupancy(t *testing.T) {
	at := time.Unix(2000, 0).UTC()
	objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	put := func(name, mode, verdict string, d int64, measuredAt time.Time) string {
		record := DispatchOccupancyRecord{SchemaVersion: DispatchOccupancySchema, Node: testNode, PowerMode: mode, RuntimeImage: "image", Model: "m",
			ProfileID: "profile-" + mode, PerRequestNanos: d, Verdict: verdict, MeasuredAt: measuredAt}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		key := DispatchOccupancyPrefix + "node-uid/" + name + ".json"
		objects.objects[key] = raw
		return key
	}
	put("old", "max", servingstate.MeasurementAccepted, 9_000_000, at)
	newest := put("new", "max", servingstate.MeasurementAccepted, 9_410_000, at.Add(time.Hour))
	put("later-insufficient", "max", servingops.MeasurementInsufficient, 1, at.Add(2*time.Hour))
	negative := put("low", "low", servingstate.MeasurementAccepted, -5, at)
	evidence, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	view, err := evidence.Refresh(context.Background(), nil, nil, []serving.Profile{{ID: "profile-max"}, {ID: "profile-low"}})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]serving.DispatchOccupancy{}
	for _, o := range view.DispatchOccupancy {
		got[o.PowerMode] = o
	}
	if len(got) != 2 || got["max"].Source != newest || got["max"].PerRequest != 9_410_000 || got["max"].Node != testNode ||
		got["low"].PerRequest != 0 || got["low"].Source != negative {
		t.Fatalf("occupancy %+v", view.DispatchOccupancy)
	}
}

func TestCostEvidenceReusesOccupancyAcrossProfileChanges(t *testing.T) {
	at := time.Unix(2000, 0).UTC()
	objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	put := func(name, profileID string, d int64, measuredAt time.Time) string {
		t.Helper()
		record := DispatchOccupancyRecord{SchemaVersion: DispatchOccupancySchema, Node: testNode, PowerMode: "max", RuntimeImage: "image", Model: "m",
			ProfileID: profileID, PerRequestNanos: d, Verdict: servingstate.MeasurementAccepted, MeasuredAt: measuredAt}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		key := DispatchOccupancyPrefix + "node-uid/" + name + ".json"
		objects.objects[key] = raw
		return key
	}
	// Profile IDs and runtime images do not invalidate a measured node cell.
	profile := serving.Profile{ID: "shared-profile"}
	profile.Condition.PowerMode = "max"
	profile.Condition.RuntimeImage = "new-image"
	profile.Condition.LogicalModel = "m"
	option := serving.SchedulerOption{Instance: serving.SchedulerInstance{Node: testNode, Profile: &profile}}
	workload := &serving.Definition{UID: "workload"}
	legacy := put("controlled", "controlled-profile", 9_410_000, at.Add(time.Hour))
	evidence, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	view, err := evidence.Refresh(context.Background(), nil, nil, []serving.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	settings := serving.SchedulerSettings{DefaultDispatchOccupancyNanos: int64(time.Millisecond)}
	input := serving.NewCapacityInput(settings, view.DispatchOccupancy, option, workload)
	if len(view.DispatchOccupancy) != 1 || input.DispatchOccupancy == nil ||
		input.DispatchOccupancy.Provenance.Source != "dom" || input.DispatchOccupancy.Source != legacy ||
		input.DispatchOccupancy.PerRequest != 9_410_000*time.Nanosecond {
		t.Fatalf("historical occupancy was stranded by profile/image: %+v", input.DispatchOccupancy)
	}
	// Adding an older current-profile measurement must not hide the latest cell.
	put("shared", profile.ID, 2_000_000, at)
	view, err = evidence.Refresh(context.Background(), nil, nil, []serving.Profile{profile})
	if err != nil {
		t.Fatal(err)
	}
	input = serving.NewCapacityInput(serving.SchedulerSettings{}, view.DispatchOccupancy, option, workload)
	if len(view.DispatchOccupancy) != 1 || input.DispatchOccupancy == nil ||
		input.DispatchOccupancy.Source != legacy || input.DispatchOccupancy.PerRequest != 9_410_000*time.Nanosecond {
		t.Fatalf("latest stable occupancy cell was not preserved: %+v", input.DispatchOccupancy)
	}
}

// Admit runs before Tick, so the snapshot can still hold the instance of the
// previous occupancy; the serving Pods listed in the same cycle decide.
func TestOccupancyJudgesWorkloadServingByListedPods(t *testing.T) {
	o := occupancyHarness(t)
	var w edge.Workload
	if err := o.controller.store.client.Get(context.Background(), client.ObjectKey{Name: "plc"}, &w); err != nil {
		t.Fatal(err)
	}
	o.controller.mu.Lock()
	o.controller.state.Snapshot.Instances = []serving.SchedulerInstance{{ID: "previous/c", Node: testNode, WorkloadUID: string(w.UID)}}
	o.controller.mu.Unlock()
	// The Node reads back the profile mode, so the run needs no power change.
	profiles := o.controller.observer.Profiles()
	profiles[0].Condition.PowerMode = maxMode
	o.controller.observer.SetProfiles(profiles)
	o.setNode(func(n *core.Node) {
		n.Annotations[chillmeta.PowerActuator] = nvpModel0
	})
	m := occupancyRootWithInputs("occ", time.Now(), 1, &profiles[0], &w)
	o.create(m)
	if err := o.controller.Admit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := o.admission(m); !strings.HasPrefix(got, "Granted") {
		t.Fatalf("stale snapshot refused the occupancy: %q", got)
	}
	o.controller.wg.Wait()

	pod := servingPod("serving-plc")
	pod.Spec.NodeName = "gw"
	pod.Annotations = map[string]string{servingstate.ServingRoutingAnnotation: `{"` + string(w.UID) + `":1}`}
	if _, err := o.kube.CoreV1().Pods("chill-system").Create(context.Background(), pod, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	next := occupancyRootWithInputs("occ-2", time.Now().Add(time.Second), 1, &profiles[0], &w)
	o.create(next)
	o.admit()
	if got := o.admission(next); got != "Refused: WorkloadServing chill-system/serving-plc" {
		t.Fatalf("serving Pod not refused: %q", got)
	}
}
