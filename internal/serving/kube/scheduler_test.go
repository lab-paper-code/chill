package kube

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// schedulerCaptureClockController serves one Node and one instance; onLiveNodes
// runs when capture lists the Nodes, after its observer refresh.
func schedulerCaptureClockController(t *testing.T, onLiveNodes func(*Observer)) *SchedulerController {
	t.Helper()
	manager, _ := newManagerHarness(t)
	pod, node, profile, class := observerSourceFixture()
	profile.RuntimeMetadata.RuntimeVersion = "test-runtime"
	profile.RuntimeMetadata.Inputs = profile.Condition.Preprocessor.Inputs
	profile.RuntimeMetadata.Outputs = profile.Condition.Preprocessor.Outputs
	profile.RuntimeMetadata.InitializedBatches = []int{1}
	if err := manager.store.client.Create(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	manager.observer.SetProfiles([]serving.Profile{profile})
	service := observerServiceFixture(pod)
	maxAge := time.Second
	var nodeReads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("watch") == "true" || strings.HasSuffix(r.URL.Path, "/log") {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		var response any
		switch {
		case strings.HasSuffix(r.URL.Path, "/state"):
			actual := *profile.RuntimeMetadata
			actual.ModelFiles = deliveredModelFiles(profile.Condition.Artifact.Files)
			response = native.State{At: time.Now().UTC(), Ready: true, Metadata: native.Metadata{
				Model: "chill_model", Mode: "serving", ProfileID: profile.ID, AppliedAt: time.Now().UTC(),
				Runtime: actual, MaxQueueRequests: 4, MaxBatchSize: 1, ModelInstanceCount: 1,
			}}
		case r.URL.Path == "/api/v1/nodes":
			if nodeReads.Add(1) == 2 && onLiveNodes != nil {
				onLiveNodes(manager.observer)
			}
			response = core.NodeList{TypeMeta: meta.TypeMeta{Kind: "NodeList", APIVersion: "v1"}, Items: []core.Node{*node}}
		case strings.HasSuffix(r.URL.Path, "/pods"):
			response = core.PodList{TypeMeta: meta.TypeMeta{Kind: "PodList", APIVersion: "v1"}, Items: []core.Pod{*pod}}
		case strings.HasSuffix(r.URL.Path, "/pods/"+pod.Name):
			response = pod
		case strings.HasSuffix(r.URL.Path, "/services"):
			response = core.ServiceList{TypeMeta: meta.TypeMeta{Kind: "ServiceList", APIVersion: "v1"}, Items: []core.Service{*service}}
		case strings.Contains(r.URL.Path, "/services/"):
			response = service
		default:
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		_ = manager.observer.Close()
		server.CloseClientConnections()
		server.Close()
	})
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	manager.observer.kube = kube
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	manager.observer.native.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	return &SchedulerController{capacity: serving.NewCapacityTable(1, nil, nil), store: manager.store, kube: kube, runtime: manager.runtime, observer: manager.observer, observationLog: manager.observationLog,
		runs: map[string]*actionRun{}, config: SchedulerControllerConfig{
			Settings: serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second, MaxObservationAge: maxAge, MaxReplicas: 1, MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1, MaxCPUs: 4},
		}}
}

func schedulerCaptureClockState(t *testing.T, controller *SchedulerController) SchedulerControllerState {
	t.Helper()
	state := controller.State()
	snapshot := state.Snapshot
	if len(snapshot.Nodes) != 1 || len(snapshot.Instances) != 1 || !snapshot.Instances[0].Ready {
		t.Fatalf("missing authoritative physical sources: %+v; issues=%+v", snapshot, controller.observer.Issues())
	}
	instance := snapshot.Instances[0]
	if !snapshot.Nodes[0].ObservedAt.Before(snapshot.At) || !instance.ObservedAt.Before(snapshot.At) ||
		!instance.ObservedAt.Equal(controller.runtime.Instances()[0].ObservedAt) {
		t.Fatalf("capture restamped source observations: node=%s instance=%s at=%s", snapshot.Nodes[0].ObservedAt, instance.ObservedAt, snapshot.At)
	}
	return state
}

func TestSchedulerCaptureKeepsHistoricalCutoffSeparateFromLiveSourceAge(t *testing.T) {
	controller := schedulerCaptureClockController(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	history, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: managerArchive{t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := history.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	// The window lies after the log started observing (ObservedSince) and the
	// cutoff is older than the live sources' maximum age when the Tick runs.
	from := history.Started().Add(time.Millisecond)
	cutoff := from.Add(controller.config.Settings.ObservationWindow)
	time.Sleep(time.Until(cutoff.Add(controller.config.Settings.MaxObservationAge + 100*time.Millisecond)))
	for index, at := range []time.Time{from.Add(-time.Nanosecond), from, cutoff.Add(-time.Nanosecond), cutoff} {
		if err = history.RequestEvent(serving.RequestEvent{RequestID: strconv.Itoa(index), WorkloadUID: "clock-history", Kind: "received", At: at, ReceivedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	if err = history.Close(ctx); err != nil {
		t.Fatal(err)
	}
	controller.observationLog = history
	if _, err = controller.Tick(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	state := schedulerCaptureClockState(t, controller)
	snapshot, decision := state.Snapshot, state.Decision
	if !snapshot.ObservationEnd.Equal(cutoff) || !decision.Search.WindowStart.Equal(from) || !decision.Search.WindowEnd.Equal(cutoff) || decision.Search.Received != 2 || decision.EvaluationElapsed <= 0 {
		t.Fatalf("live reads moved the original half-open history interval: snapshot=%+v search=%+v", snapshot, decision.Search)
	}
	node, instance := snapshot.Nodes[0], snapshot.Instances[0]
	if slices.Contains(decision.Keep.Reasons, "UnknownOrStaleNode:"+node.Node.Name) ||
		slices.Contains(decision.Keep.Reasons, "UnknownOrStaleInstance:"+instance.ID) {
		t.Fatalf("consumer falsely aged new authoritative sources: %+v", decision.Keep)
	}
}

func TestSchedulerCaptureSlowLiveReadRemainsStale(t *testing.T) {
	controller := schedulerCaptureClockController(t, func(*Observer) { time.Sleep(2 * time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := controller.Tick(ctx, time.Now().Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	state := schedulerCaptureClockState(t, controller)
	snapshot, decision := state.Snapshot, state.Decision
	node, instance := snapshot.Nodes[0], snapshot.Instances[0]
	maxAge := controller.config.Settings.MaxObservationAge
	if snapshot.At.Sub(node.ObservedAt) <= maxAge || snapshot.At.Sub(instance.ObservedAt) <= maxAge {
		t.Fatalf("slow live HTTP latency disappeared from source age: node=%s instance=%s at=%s", node.ObservedAt, instance.ObservedAt, snapshot.At)
	}
	if decision.Keep.Status != "Pending" || !slices.Contains(decision.Keep.Reasons, "UnknownOrStaleNode:"+node.Node.Name) ||
		!slices.Contains(decision.Keep.Reasons, "UnknownOrStaleInstance:"+instance.ID) {
		t.Fatalf("consumer accepted genuinely stale live observations: %+v", decision.Keep)
	}
}

// m4 r7: during prepares, decisions took 11-28 s and every Node was
// UnknownOrStaleNode about 11.5 s after its list. Each running prepare polls
// a full observer refresh every second, and the server's source ticker
// refreshes too; capture read the observer's profiles under that lock after
// listing the Nodes, so it waited behind those refreshes. Capture reads its
// observer state before the Node list.
func TestSchedulerCaptureDoesNotAgeNodesBehindObserverRefresh(t *testing.T) {
	release := make(chan struct{})
	controller := schedulerCaptureClockController(t, func(o *Observer) {
		held := make(chan struct{})
		go func() {
			o.mu.Lock()
			close(held)
			<-release
			o.mu.Unlock()
		}()
		<-held
		time.AfterFunc(2*time.Second, func() { close(release) })
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := controller.Tick(ctx, time.Now().Add(-10*time.Second)); err != nil {
		t.Fatal(err)
	}
	state := controller.State()
	if slices.ContainsFunc(state.Decision.Keep.Reasons, func(r string) bool { return strings.HasPrefix(r, "UnknownOrStaleNode:") }) {
		t.Fatalf("a concurrent observer refresh aged the Node list: %v", state.Decision.Keep.Reasons)
	}
}

func schedulerEmptyPoolController(t *testing.T) *SchedulerController {
	t.Helper()
	manager, _ := newManagerHarness(t)
	_, node, _, class := observerSourceFixture()
	if err := manager.store.client.Create(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	kube := kubefake.NewSimpleClientset(node)
	manager.observer.kube = kube
	operations, err := NewOperations(manager.store, kube, manager.runtime, manager.observer, manager.observer.native,
		OperationConfig{Namespace: "chill-system", PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := NewSchedulerController(manager.store, kube, manager.runtime, manager.observer, operations, manager.observationLog,
		SchedulerControllerConfig{
			Settings: serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second, MaxObservationAge: time.Second,
				MaxReplicas: 1, MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1, MaxCPUs: 4,
				Controls: serving.SchedulerControls{Placement: true, Resources: true, Replicas: true,
					Routing: true, Batching: true, Power: true}},
			Baselines: []serving.NodeBaseline{{ID: "empty-pool-idle", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)},
				PowerMode: "fixed", Watts: 10, Meter: "node-wall", Scope: "node-exclusive-wall", MeasuredAt: time.Now(),
				Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "empty-pool-idle", Size: 1}}},
		})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	return controller
}

func TestSchedulerTickDeadlinePreservesPublishedStateAndAllowsControlsOff(t *testing.T) {
	controller := schedulerEmptyPoolController(t)
	first, err := controller.Tick(context.Background(), time.Now().UTC())
	if err != nil || first.Target == nil || first.Keep.ExpectedJoules == nil {
		t.Fatalf("empty physical pool was not evaluable: %+v %v", first, err)
	}
	config := controller.config
	before := controller.State()
	decisions := 0
	controller.config.OnDecision = func(serving.SchedulerDecision) error { decisions++; return nil }
	ctx, cancel := context.WithDeadline(context.Background(), time.Now())
	defer cancel()
	decision, err := controller.Tick(ctx, time.Now().UTC())
	if !errors.Is(err, context.DeadlineExceeded) || decision.Status != "" {
		t.Fatalf("CPU deadline became a publishable decision: %+v %v", decision, err)
	}
	after := controller.State()
	if decisions != 0 || !after.Snapshot.At.Equal(before.Snapshot.At) || after.Decision.At != before.Decision.At ||
		len(after.Actions) != 0 || len(after.Results) != 0 {
		t.Fatal("canceled evaluation published state, committed history or started physical work")
	}
	off := config
	off.Settings.Controls = serving.SchedulerControls{}
	applied := make(chan error, 1)
	go func() { applied <- controller.SetConfiguration(off) }()
	select {
	case err := <-applied:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled evaluation retained the configuration lock")
	}
	if controller.Settings().Controls != (serving.SchedulerControls{}) || len(controller.State().Actions) != 0 {
		t.Fatal("all controls OFF did not replace the canceled ON evaluation")
	}
}

func TestSchedulerTickCanceledAfterDecisionCannotCommitHistoryOrLaunchOldONTarget(t *testing.T) {
	controller := schedulerEmptyPoolController(t)
	if _, err := controller.Tick(context.Background(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	controller.goal = &serving.SchedulerCandidate{ID: "unfinished-route", Nodes: controller.State().Snapshot.Nodes,
		Routing: map[string]map[string]int32{}, Actions: []serving.Action{{ID: "unfinished-route", Kind: operationRoute, Status: "planned"}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var publishedAt time.Time
	controller.config.OnDecision = func(decision serving.SchedulerDecision) error {
		if decision.Target == nil || len(decision.Target.Actions) == 0 {
			t.Fatal("fixture did not produce an actionable ON continuation")
		}
		snapshot := controller.State().Snapshot
		if err := controller.observationLog.SchedulerRecord(serving.SchedulerObservation{At: decision.At, Snapshot: &snapshot, Decision: &decision}); err != nil {
			return err
		}
		publishedAt = decision.At
		cancel()
		return nil
	}
	if decision, err := controller.Tick(ctx, time.Now().UTC()); !errors.Is(err, context.Canceled) || decision.Status != "" {
		t.Fatalf("late cancellation became a successful ON decision: %+v %v", decision, err)
	}
	if len(controller.State().Actions) != 0 {
		t.Fatal("late canceled ON result advanced history or launched its route")
	}
	if publishedAt.IsZero() || !controller.State().Decision.At.Equal(publishedAt) {
		t.Fatal("late cancellation hid an already published and recorded valid decision")
	}
}

func TestSchedulerCaptureClassifiesEntirePool(t *testing.T) {
	controller := schedulerEmptyPoolController(t)
	ctx := context.Background()
	_, base, _, _ := observerSourceFixture()
	config := controller.config
	// A non-Fixed mode with a configured policy is requestable; a Node whose
	// one DeviceClass has only Fixed modes is not.
	class := &edge.DeviceClass{}
	if err := controller.store.client.Get(ctx, client.ObjectKey{Name: "cpu"}, class); err != nil {
		t.Fatal(err)
	}
	eco := edge.PowerModeActuator{Kind: edge.PowerModeActuatorJetsonNVPModel, JetsonNVPModel: &edge.JetsonNVPModelTarget{ID: 1}}
	class.Spec.PowerModes = append(class.Spec.PowerModes, edge.PowerMode{Name: "eco", Actuator: eco})
	if err := controller.store.client.Update(ctx, class); err != nil {
		t.Fatal(err)
	}
	fixedOnly := &edge.DeviceClass{ObjectMeta: meta.ObjectMeta{Name: "fixed-only"}, Spec: edge.DeviceClassSpec{
		NodeSelector: map[string]string{"hardware": "fixed-only"}, Architecture: "amd64",
		PowerModes: []edge.PowerMode{{Name: "fixed", Actuator: edge.PowerModeActuator{Kind: edge.PowerModeActuatorFixed}}}}}
	if err := controller.store.client.Create(ctx, fixedOnly); err != nil {
		t.Fatal(err)
	}
	config.PowerPolicies = []nodeactuator.ActuationPolicySpec{{Name: "cpu-eco", DeviceClass: "cpu", PowerMode: "eco",
		ActuatorKind: nodeactuator.ActuatorKindJetsonNVPModel, SettlingDurationSeconds: 1, MaxActuationDurationSeconds: 30}}
	config.Reserved = map[string]serving.SchedulerAllocation{"reserved-out": {CPUs: []int{7}}}
	want := map[string]struct {
		reasons     []string
		measurement bool
		gpus        []string
	}{
		base.Name:      {},
		"bare":         {reasons: []string{serving.NodePlacementInventoryMissing}},
		"garbled":      {reasons: []string{serving.NodePlacementInventoryMissing}},
		"reserved-out": {reasons: []string{serving.NodePlacementInventoryMissing}},
		"unpowered":    {reasons: []string{serving.NodePowerModeUnavailable}},
		"fixed-only":   {reasons: []string{serving.NodePowerModeUnavailable}},
		"measured":     {measurement: true},
		"gpu":          {gpus: []string{"GPU-a"}},
		"gpu-over":     {reasons: []string{serving.NodePlacementInventoryMissing}, gpus: []string{"GPU-b", "GPU-c"}},
	}
	// GPU inventory is the GPUs the Node's own profiles were measured on.
	measuredOn := func(id, nodeUID, gpu string) serving.Profile {
		return serving.Profile{ID: id, Condition: profilederivation.CurvePlanSpec{NodeUID: nodeUID}, RuntimeMetadata: &native.RuntimeMetadata{GPUUUID: gpu}}
	}
	controller.observer.SetProfiles([]serving.Profile{
		measuredOn("a", "gpu-uid", "GPU-a"), measuredOn("a2", "gpu-uid", "GPU-a"), measuredOn("cpu", "gpu-uid", ""),
		measuredOn("b", "gpu-over-uid", "GPU-c"), measuredOn("c", "gpu-over-uid", "GPU-b"),
	})
	for _, name := range []string{"control-plane", "bare", "garbled", "reserved-out", "unpowered", "fixed-only", "measured", "gpu", "gpu-over"} {
		node := base.DeepCopy()
		node.Name, node.UID = name, types.UID(name+"-uid")
		switch name {
		case "control-plane":
			node.Labels[controlPlaneLabel] = ""
		case "bare":
			delete(node.Annotations, chillmeta.OnlineCPUs)
		case "garbled":
			node.Annotations[chillmeta.OnlineCPUs] = "0-"
		case "unpowered":
			node.Labels["hardware"] = "none"
		case "fixed-only":
			node.Labels["hardware"] = "fixed-only"
		case "measured":
			node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Effect: core.TaintEffectNoSchedule}}
		case "gpu", "gpu-over":
			node.Status.Allocatable["nvidia.com/gpu"] = resource.MustParse("1")
		}
		if _, err := controller.kube.CoreV1().Nodes().Create(ctx, node, meta.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := controller.capture(ctx, time.Now().UTC(), config)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Nodes) != len(want) {
		t.Fatalf("entire POOL = %+v, want %d non-control-plane Nodes", snapshot.Nodes, len(want))
	}
	for _, node := range snapshot.Nodes {
		expected, ok := want[node.Node.Name]
		if !ok || !node.Ready || !slices.Equal(node.Reasons, expected.reasons) || node.Measurement != expected.measurement || !slices.Equal(node.GPUUUIDs, expected.gpus) {
			t.Fatalf("%s: ready=%v reasons=%v measurement=%v gpus=%v", node.Node.Name, node.Ready, node.Reasons, node.Measurement, node.GPUUUIDs)
		}
	}
	if d, ok := controller.powerTargets["gpu-uid/eco"]; !ok || d != 30*time.Second || len(controller.powerTargets) != 7 {
		t.Fatalf("power targets = %+v", controller.powerTargets)
	}
}

// A power action requests its target mode within the mode's policy duration;
// a mode without a policy gets no request.
func TestScheduledPowerRequestNamesTheMode(t *testing.T) {
	node := serving.NodeReference{Name: "jetson", UID: "jetson-uid"}
	power := map[string]time.Duration{"jetson-uid/eco": 30 * time.Second}
	action := serving.Action{ID: "lower", Kind: operationPower, Node: node, To: "eco"}
	if op := scheduledOperation(action, &serving.SchedulerCandidate{}, SchedulerControllerConfig{}, power, nil); op.PowerMode != "eco" || op.MaxActuationDuration != 30*time.Second {
		t.Fatalf("power request %+v", op)
	}
	action.To = "unrequestable"
	if op := scheduledOperation(action, &serving.SchedulerCandidate{}, SchedulerControllerConfig{}, power, nil); op.PowerMode != "" {
		t.Fatal("mode without a policy got a request")
	}
}

// m4 r6: a planner preparation records its Workload as the Pod's owner, so an
// unrouted instance keeps its Workload. It waits for the planner's route: the
// Pod's static routing names no weight, as before.
func TestScheduledPreparationOwnsItsWorkload(t *testing.T) {
	profile := &serving.Profile{ID: "profile", Condition: profilederivation.CurvePlanSpec{Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 8}}}
	wanted := serving.SchedulerInstance{ID: "target-w1-profile-1", WorkloadUID: "w1", Profile: profile, Weight: 1, MaxBatch: 1}
	action := serving.Action{ID: "prepare", Kind: operationPrepare, Instance: wanted.ID}
	config := SchedulerControllerConfig{Preparations: map[string]PrepareSpec{profile.ID: {}}}
	workloads := []*serving.Definition{{Name: "tight", UID: "w1"}}
	op := scheduledOperation(action, &serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{wanted}}, config, nil, workloads)
	if op.Prepare == nil || op.Prepare.Owner == nil || *op.Prepare.Owner != (serving.WorkloadReference{Name: "tight", UID: "w1"}) {
		t.Fatalf("planner preparation lost its Workload owner: %+v", op.Prepare)
	}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{}}}
	(&Operations{}).placeServingPod(pod, op.Prepare.Owner, op.Prepare.Instance)
	if owner, err := resourceWorkloadOwner(pod, nil); err != nil || owner != "w1" || pod.Annotations[ServingRoutingAnnotation] != "{}" {
		t.Fatalf("pod owner %q (%v), routing %s", owner, err, pod.Annotations[ServingRoutingAnnotation])
	}
	pod = &core.Pod{ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{}}}
	(&Operations{}).placeServingPod(pod, op.Prepare.Owner, serving.SchedulerInstance{Weight: 3})
	if pod.Annotations[ServingRoutingAnnotation] != `{"w1":3}` {
		t.Fatalf("a manual preparation must serve its owner at once: %s", pod.Annotations[ServingRoutingAnnotation])
	}
}

func TestSchedulerLaunchTracksGoalOnlyForNonFixedActions(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		controller := &SchedulerController{capacity: serving.NewCapacityTable(1, nil, nil), runs: map[string]*actionRun{}}
		// Completed actions are skipped, so launch starts no operation.
		controller.launch(serving.SchedulerCandidate{ID: "keep", Keep: true, Actions: []serving.Action{
			{ID: "lower", Kind: operationPower, Fixed: fixed, Status: nativeOperationComplete},
		}}, SchedulerControllerConfig{})
		if (controller.goal == nil) != fixed {
			t.Fatalf("fixed=%v: goal=%+v", fixed, controller.goal)
		}
	}
}

func TestPreparedPodRequiresMeasuredGPUEnvironment(t *testing.T) {
	desired := &core.Pod{Spec: core.PodSpec{Containers: []core.Container{{
		Name: "firstfloor", Env: []core.EnvVar{{Name: "CUDA_VISIBLE_DEVICES", Value: "GPU-measured"}},
	}}}}
	if !samePreparedPod(desired.DeepCopy(), desired) {
		t.Fatal("matching measured GPU environment was rejected")
	}
	for _, env := range [][]core.EnvVar{
		nil, {{Name: "CUDA_VISIBLE_DEVICES", Value: "GPU-other"}},
	} {
		actual := desired.DeepCopy()
		actual.Spec.Containers[0].Env = env
		if samePreparedPod(actual, desired) {
			t.Fatal("preparation reused without the measured GPU environment")
		}
	}
}

func TestRemoveRejectsPhysicalInstanceOnDifferentNodeBeforeMutation(t *testing.T) {
	observer := newTestObserver(t)
	mutated := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			mutated = true
			http.Error(w, "unexpected mutation", 500)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/pods") {
			if err := json.NewEncoder(w).Encode(core.PodList{
				TypeMeta: meta.TypeMeta{Kind: "PodList", APIVersion: "v1"},
				Items: []core.Pod{{
					ObjectMeta: meta.ObjectMeta{Name: "other", Namespace: observer.config.Namespace,
						UID: "pod-b", Labels: map[string]string{"app.kubernetes.io/component": servingInstanceComponent}},
					Spec: core.PodSpec{NodeName: "b"},
					Status: core.PodStatus{ContainerStatuses: []core.ContainerStatus{{
						Name: "firstfloor", ContainerID: "containerd://b",
					}}},
				}},
			}); err != nil {
				t.Error(err)
			}
			return
		}
		if err := json.NewEncoder(w).Encode(core.Node{ObjectMeta: meta.ObjectMeta{Name: "a", UID: "node-a"}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	operations := &Operations{kube: kube, runtime: observer.runtime, config: OperationConfig{Namespace: observer.config.Namespace}}
	if err = operations.RemoveInstance(context.Background(), serving.NodeReference{Name: "a", UID: "node-a"}, "pod-b/containerd://b"); err == nil || mutated {
		t.Fatal("cross-node physical removal was not rejected before mutation", err, mutated)
	}
}
func TestPreparedSlotCompletionExpiresWhenActualInstanceRemoved(t *testing.T) {
	manager, _ := newManagerHarness(t)
	observer := manager.observer
	controller := &SchedulerController{capacity: serving.NewCapacityTable(1, nil, nil), runtime: observer.runtime}
	profile := &serving.Profile{ID: "profile"}
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	run := &actionRun{action: serving.Action{Kind: "prepare", Instance: "slot", Node: node}, result: &OperationResult{Status: "complete", Operation: Operation{Prepare: &PrepareSpec{Instance: serving.SchedulerInstance{ID: "slot", Node: node, Profile: profile}}}}}
	if controller.actionEffectPresent(serving.SchedulerSnapshot{}, run) {
		t.Fatal("history treated as current prepared effect")
	}
	instance := serving.ReadyInstance{State: serving.InstanceState{ID: "pod/container", TargetID: "slot", Node: node, Profile: profile.ID, Ready: true, Native: &native.State{Ready: true}}, Profile: profile, Capacity: 1, Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
		panic("regression only observes physical state")
	}}
	if err := observer.runtime.InstallInstances([]serving.ReadyInstance{instance}); err != nil {
		t.Fatal(err)
	}
	if !controller.actionEffectPresent(serving.SchedulerSnapshot{}, run) {
		t.Fatal("actual prepared slot not recognized")
	}
	if err := observer.runtime.InstallInstances(nil); err != nil {
		t.Fatal(err)
	}
	if controller.actionEffectPresent(serving.SchedulerSnapshot{}, run) {
		t.Fatal("removed deterministic slot falsely reused old completion")
	}
}
func TestPodBudgetAccountsForInitSidecarsAndOverhead(t *testing.T) {
	always := core.ContainerRestartPolicyAlways
	resources := func(cpu, memory string) core.ResourceRequirements {
		return core.ResourceRequirements{Requests: core.ResourceList{core.ResourceCPU: resource.MustParse(cpu), core.ResourceMemory: resource.MustParse(memory)}}
	}
	pod := &core.Pod{Spec: core.PodSpec{Containers: []core.Container{{Resources: resources("500m", "100")}}, InitContainers: []core.Container{{Resources: resources("200m", "20"), RestartPolicy: &always}, {Resources: resources("2", "300")}}, Overhead: core.ResourceList{core.ResourceCPU: resource.MustParse("100m"), core.ResourceMemory: resource.MustParse("10")}}}
	cpu, memory, gpu := podBudget(pod)
	if cpu != 2300 || memory != 330 || gpu {
		t.Fatal("Kubernetes effective request budget miscounted", cpu, memory, gpu)
	}
}

func TestCaptureNodePodsAggregatesNonServingBudget(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cpu        string
		reserved   []int
		memory     uint64
		gpu        bool
		available  bool
		wantCPU    int
		wantMemory bool
	}{
		{name: "fractional requests fit", cpu: "10m", reserved: []int{0}, memory: 1000, available: true, wantCPU: 1},
		{name: "real CPU overbudget", cpu: "110m", reserved: []int{0}, memory: 1000, wantCPU: 2},
		{name: "missing exclusive reservation", cpu: "10m", memory: 1000, wantCPU: 1},
		{name: "real memory overbudget", cpu: "10m", reserved: []int{0}, memory: 99, available: true, wantCPU: 1, wantMemory: true},
		{name: "unknown GPU ownership", cpu: "10m", reserved: []int{0}, memory: 1000, gpu: true, wantCPU: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at := time.Unix(100, 0)
			node := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}
			sn := serving.SchedulerNode{Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Ready: true, CPUs: []int{0, 1}, Reserved: serving.SchedulerAllocation{CPUs: tc.reserved}, MemoryBytes: tc.memory, ObservedAt: at}
			pods := &core.PodList{}
			for range 10 {
				requests := core.ResourceList{core.ResourceCPU: resource.MustParse(tc.cpu), core.ResourceMemory: resource.MustParse("10")}
				if tc.gpu {
					requests[core.ResourceName("nvidia.com/gpu")] = resource.MustParse("1")
				}
				pods.Items = append(pods.Items, core.Pod{Spec: core.PodSpec{NodeName: node.Name, Containers: []core.Container{{Resources: core.ResourceRequirements{Requests: requests}}}}})
			}
			// An actual native instance is not part of the non-serving sum.
			pods.Items = append(pods.Items, core.Pod{ObjectMeta: meta.ObjectMeta{UID: "native-pod", Labels: map[string]string{"app.kubernetes.io/component": servingInstanceComponent}}, Spec: core.PodSpec{NodeName: node.Name, Containers: []core.Container{{Name: firstfloorContainerName, Resources: core.ResourceRequirements{Requests: core.ResourceList{core.ResourceCPU: resource.MustParse("2"), core.ResourceMemory: resource.MustParse("500")}}}}}, Status: core.PodStatus{ContainerStatuses: []core.ContainerStatus{{Name: firstfloorContainerName, ContainerID: "container"}}}})
			physical := map[string]serving.InstanceState{"native-pod/container": {ID: "native-pod/container", Node: sn.Node, Profile: "profile", Native: &native.State{}}}
			snapshot := serving.SchedulerSnapshot{At: at}
			captureNodePods(&snapshot, &sn, node, pods, physical, map[string]*serving.Profile{"profile": {ID: "profile"}})
			if !sn.Ready || slices.Contains(sn.Reasons, serving.NodePlacementBudgetUnavailable) == tc.available || len(sn.NonServing) != 1 || len(snapshot.Instances) != 1 {
				t.Fatal("incorrect readiness or physical classification", sn, snapshot.Instances)
			}
			allocation := sn.NonServing[0]
			if allocation.CPUCount != tc.wantCPU || allocation.MemoryBytes != 100 || allocation.CPUReserved != (len(tc.reserved) > 0) {
				t.Fatal("non-serving requests were not aggregated exactly", allocation)
			}
			// Isolate the captured non-serving budget in the actual consumer.
			reasons := serving.ValidateNodeBudget(sn, sn.NonServing, nil)
			cpuInfeasible, memoryInfeasible, unknownAffinity := false, false, false
			for _, reason := range reasons {
				cpuInfeasible = cpuInfeasible || strings.HasPrefix(reason, "CPUBudgetInfeasible:")
				memoryInfeasible = memoryInfeasible || strings.HasPrefix(reason, "MemoryBudgetInfeasible:")
				unknownAffinity = unknownAffinity || strings.HasPrefix(reason, "UnknownExclusiveCPUAffinity:")
			}
			if cpuInfeasible != (len(tc.reserved) > 0 && tc.wantCPU > len(tc.reserved)) || memoryInfeasible != tc.wantMemory || unknownAffinity != (len(tc.reserved) == 0) {
				t.Fatal("consumer miscounted the captured budget", reasons)
			}
		})
	}
}

func TestCaptureNodePodsRejectsMemoryOverflow(t *testing.T) {
	node := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}
	sn := serving.SchedulerNode{Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Ready: true, MemoryBytes: math.MaxUint64}
	pods := &core.PodList{}
	for range 3 {
		pods.Items = append(pods.Items, core.Pod{Spec: core.PodSpec{NodeName: node.Name, Containers: []core.Container{{Resources: core.ResourceRequirements{Requests: core.ResourceList{core.ResourceMemory: *resource.NewQuantity(math.MaxInt64, resource.BinarySI)}}}}}})
	}
	snapshot := serving.SchedulerSnapshot{}
	captureNodePods(&snapshot, &sn, node, pods, nil, nil)
	if !slices.Contains(sn.Reasons, serving.NodePlacementBudgetUnavailable) {
		t.Fatal("overflowing effective memory requests became an available node")
	}
}

func TestNonServingGPUOwnershipQuarantinesOnlyItsNode(t *testing.T) {
	node := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "quarantined-gpu", UID: "gpu-node"}}
	state := serving.SchedulerNode{
		Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Ready: true,
		CPUs: []int{0, 1}, Reserved: serving.SchedulerAllocation{CPUs: []int{0}}, MemoryBytes: 4096,
	}
	pods := &core.PodList{Items: []core.Pod{{
		ObjectMeta: meta.ObjectMeta{Name: "foreign-gpu"},
		Spec: core.PodSpec{NodeName: node.Name, Containers: []core.Container{{
			Name: "foreign", Resources: core.ResourceRequirements{Requests: core.ResourceList{
				core.ResourceCPU: resource.MustParse("100m"), core.ResourceMemory: resource.MustParse("1Ki"),
				core.ResourceName("nvidia.com/gpu"): resource.MustParse("1"),
			}},
		}}},
		Status: core.PodStatus{Phase: core.PodRunning},
	}}}
	snapshot := serving.SchedulerSnapshot{}
	captureNodePods(&snapshot, &state, node, pods, nil, nil)
	if !state.Ready || !slices.Contains(state.Reasons, serving.NodePlacementBudgetUnavailable) || len(snapshot.Reasons) != 0 {
		t.Fatalf("node-local GPU uncertainty became a global evaluation gap: node=%+v reasons=%v", state, snapshot.Reasons)
	}
	if len(state.NonServing) != 1 || state.NonServing[0].CPUCount != 1 ||
		state.NonServing[0].MemoryBytes != 1024 || !state.NonServing[0].CPUReserved {
		t.Fatalf("quarantine discarded actual non-serving resource budget: %+v", state.NonServing)
	}
}

func TestBindProfileMeterUsesWallQuantityIdentity(t *testing.T) {
	node := serving.NodeReference{Name: "gpu-node", UID: "gpu-node-uid"}
	source := metricobserver.SourceIdentity{
		Kind: "edge-metrics", NodeName: node.Name, Endpoint: "http://power/metrics",
		Metric: "wall_watts", MetricLabels: map[string]string{"hostname": node.Name},
	}
	for _, test := range []struct {
		name   string
		mutate func(*MeterDeclaration)
		binds  bool
	}{
		{name: "same wall series without thermal metadata", binds: true},
		{name: "different power endpoint", mutate: func(m *MeterDeclaration) { m.Source.Endpoint = "http://other/metrics" }},
		{name: "different power labels", mutate: func(m *MeterDeclaration) { m.Source.MetricLabels = map[string]string{"hostname": "another-host"} }},
		{name: "different power metric", mutate: func(m *MeterDeclaration) { m.Source.Metric = "device_watts" }},
		{name: "different source kind", mutate: func(m *MeterDeclaration) { m.Source.Kind = "another-power-source" }},
		{name: "different source node", mutate: func(m *MeterDeclaration) { m.Source.NodeName = "another-node" }},
		{name: "different physical node", mutate: func(m *MeterDeclaration) { m.Node.UID = "unrelated-node-uid" }},
		{name: "different scope", mutate: func(m *MeterDeclaration) { m.Scope = "device" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			profileSource := source
			profileSource.ThermalEndpoint = "http://thermal/metrics"
			profileSource.ThermalMetrics = &metricobserver.ThermalMetricIdentity{Active: "thermal_active"}
			instance := serving.SchedulerInstance{
				Node: node, Profile: &serving.Profile{MetricSource: profileSource},
			}
			meter := MeterDeclaration{ID: "wall-meter", Node: node, Scope: measurementWallScope, Source: source}
			if test.mutate != nil {
				test.mutate(&meter)
			}
			bindProfileMeter(&instance, []MeterDeclaration{meter})
			if got := instance.IdleMeter == meter.ID && instance.IdleScope == measurementWallScope; got != test.binds {
				t.Fatalf("wall quantity binding = %v, want %v: %+v", got, test.binds, instance)
			}
			if instance.Profile.MetricSource.ThermalEndpoint != "http://thermal/metrics" ||
				instance.Profile.MetricSource.ThermalMetrics.Active != "thermal_active" {
				t.Fatal("meter binding changed thermal observation identity")
			}
		})
	}
}

// The controller records when each instance lost its route weight and holds
// an instance prepared while keep was in Overload for one window after Ready.
func TestSchedulerControllerAnnotatesUnroutedAndHeldInstances(t *testing.T) {
	c := &SchedulerController{runs: map[string]*actionRun{}}
	t0, window := time.Unix(1000, 0).UTC(), time.Minute
	c.markOverloadPrepares(serving.SchedulerDecision{Keep: serving.SchedulerCandidate{Status: serving.SchedulerStatusOverload},
		Target: &serving.SchedulerCandidate{Actions: []serving.Action{{Kind: operationPrepare, Instance: "slot-b"}}}})
	snapshot := func(at time.Time, routed ...string) serving.SchedulerSnapshot {
		weights := map[string]int32{}
		for _, id := range routed {
			weights[id] = 1
		}
		return serving.SchedulerSnapshot{At: at, Routing: map[string]map[string]int32{"w": weights}, Instances: []serving.SchedulerInstance{
			{ID: "a", Ready: true}, {ID: "b", TargetID: "slot-b", Ready: true},
		}}
	}
	s := snapshot(t0, "a")
	c.annotateHistory(&s, window)
	a, b := s.Instances[0], s.Instances[1]
	if a.UnroutedSince != nil || b.UnroutedSince == nil || !b.UnroutedSince.Equal(t0) || b.HeldUntil == nil || !b.HeldUntil.Equal(t0.Add(window)) {
		t.Fatalf("first capture: a %+v b %+v", a, b)
	}
	s = snapshot(t0.Add(window), "a")
	c.annotateHistory(&s, window)
	if b = s.Instances[1]; !b.UnroutedSince.Equal(t0) || b.HeldUntil != nil {
		t.Fatalf("after the window: b %+v", b)
	}
	s = snapshot(t0.Add(2*window), "b")
	c.annotateHistory(&s, window)
	if s.Instances[1].UnroutedSince != nil || s.Instances[0].UnroutedSince == nil || !s.Instances[0].UnroutedSince.Equal(t0.Add(2*window)) {
		t.Fatalf("routing change: %+v", s.Instances)
	}
}

// Exercise the controller's actual dependency consumer with a resumed goal.
// The unused Node lowers in parallel; the used Node retains its whole chain.
func TestSchedulerRouteIgnoresUnusedCommonLowering(t *testing.T) {
	at := time.Unix(1000, 0).UTC()
	used := serving.NodeReference{Name: "used", UID: "used"}
	idle := serving.NodeReference{Name: "idle", UID: "idle"}
	settings := serving.SchedulerSettings{ObservationWindow: time.Minute, ControlInterval: 10 * time.Second,
		ComparisonPeriod: time.Minute, MaxObservationAge: time.Hour, MaxReplicas: 1, MaxInstances: 2, MaxCPUs: 4,
		CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1,
		Controls: serving.SchedulerControls{Power: true}}
	instance := serving.SchedulerInstance{ID: "slot", Node: used, WorkloadUID: "w",
		Profile: &serving.Profile{ID: "profile", Condition: profilederivation.CurvePlanSpec{PowerMode: "fixed"}}}
	goal := serving.SchedulerCandidate{ID: "goal",
		Nodes:     []serving.SchedulerNode{{Node: used, PowerMode: "fixed"}, {Node: idle, PowerMode: "eco"}},
		Instances: []serving.SchedulerInstance{instance}, Routing: map[string]map[string]int32{"w": {"slot": 1000}},
		Actions: []serving.Action{{ID: "route-goal", Kind: operationRoute, Status: "planned"}}}
	snapshot := serving.SchedulerSnapshot{At: at, ObservationEnd: at, InProgressTarget: &goal,
		Nodes:     []serving.SchedulerNode{{Node: used, Ready: true, ObservedAt: at}, {Node: idle, Ready: true, ObservedAt: at}},
		Baselines: []serving.NodeBaseline{{Node: used, PowerMode: "eco", Watts: 1}, {Node: idle, PowerMode: "eco", Watts: 1}},
		Actions: []serving.Action{
			{ID: "used-lowering", Node: used, Kind: operationPower, From: "boost", To: "eco", Fixed: true, Status: nativeOperationRunning},
			{ID: "idle-lowering", Node: idle, Kind: operationPower, From: "boost", To: "eco", Fixed: true, Status: nativeOperationRunning},
		}}
	decision, err := serving.EvaluateScheduler(context.Background(), settings, snapshot, nil)
	if err != nil || decision.Target == nil {
		t.Fatalf("resume goal: %v %+v", err, decision)
	}
	target := *decision.Target
	controller := &SchedulerController{runs: map[string]*actionRun{}}
	var raise, prepare, route serving.Action
	for _, a := range target.Actions {
		controller.runs[a.ID] = &actionRun{action: a}
		switch {
		case a.Kind == operationPower && !a.Fixed && a.Node == used:
			raise = a
		case a.Kind == operationPrepare:
			prepare = a
		case a.Kind == operationRoute:
			route = a
		}
	}
	if raise.ID == "" || prepare.ID == "" || route.ID == "" || controller.runs["idle-lowering"] == nil {
		t.Fatalf("missing continuing power/prepare/route actions: %+v", target.Actions)
	}
	if controller.dependenciesReady(raise, target) || controller.dependenciesReady(prepare, target) || controller.dependenciesReady(route, target) {
		t.Fatal("used Node's unfinished lowering/raise/prepare chain was bypassed")
	}
	controller.runs["used-lowering"].action.Status = nativeOperationComplete
	if !controller.dependenciesReady(raise, target) || controller.dependenciesReady(prepare, target) || controller.dependenciesReady(route, target) {
		t.Fatal("lowering must unlock only the used Node's raise")
	}
	controller.runs[raise.ID].action.Status = nativeOperationComplete
	if !controller.dependenciesReady(prepare, target) || controller.dependenciesReady(route, target) {
		t.Fatal("raise must unlock preparation, not routing")
	}
	controller.runs[prepare.ID].action.Status = nativeOperationComplete
	if !controller.dependenciesReady(route, target) || controller.runs["idle-lowering"].action.Status != nativeOperationRunning {
		t.Fatal("unused lowering blocked a prepared target's route or stopped running")
	}
}
