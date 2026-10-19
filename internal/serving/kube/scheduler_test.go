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
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func schedulerCaptureClockController(t *testing.T, slowLive bool) *SchedulerController {
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
			if nodeReads.Add(1) == 2 && slowLive {
				time.Sleep(2 * maxAge)
			}
			response = core.NodeList{TypeMeta: meta.TypeMeta{Kind: "NodeList", APIVersion: "v1"}, Items: []core.Node{*node}}
		case strings.HasSuffix(r.URL.Path, "/pods"):
			response = core.PodList{TypeMeta: meta.TypeMeta{Kind: "PodList", APIVersion: "v1"}, Items: []core.Pod{*pod}}
		case strings.HasSuffix(r.URL.Path, "/pods/"+pod.Name):
			response = pod
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
	return &SchedulerController{store: manager.store, kube: kube, runtime: manager.runtime, observer: manager.observer, observationLog: manager.observationLog,
		runs: map[string]*actionRun{}, config: SchedulerControllerConfig{
			Settings:     serving.SchedulerSettings{Algorithm: serving.SchedulerAlgorithmFixedHorizon, ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second, MaxObservationAge: maxAge, Replays: 1, MaxCandidates: 1, MaxReplicas: 1, MaxInstances: 1, MaxReplayRequests: 10, MaxCPUs: 4},
			CPUInventory: map[string][]int{node.Name: {0, 1, 2, 3}},
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
	controller := schedulerCaptureClockController(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cutoff := time.Now().Add(-10 * time.Second)
	from := cutoff.Add(-controller.config.Settings.ObservationWindow)
	history, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: managerArchive{t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := history.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
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
	if !snapshot.ObservationEnd.Equal(cutoff) || !decision.Search.WindowStart.Equal(from) || !decision.Search.WindowEnd.Equal(cutoff) || decision.Search.Received != 2 {
		t.Fatalf("live reads moved the original half-open history interval: snapshot=%+v search=%+v", snapshot, decision.Search)
	}
	node, instance := snapshot.Nodes[0], snapshot.Instances[0]
	if slices.Contains(decision.Keep.Reasons, "UnknownOrStaleNode:"+node.Node.Name) ||
		slices.Contains(decision.Keep.Reasons, "UnknownOrStaleInstance:"+instance.ID) {
		t.Fatalf("consumer falsely aged new authoritative sources: %+v", decision.Keep)
	}
}

func TestSchedulerCaptureSlowLiveReadRemainsStale(t *testing.T) {
	controller := schedulerCaptureClockController(t, true)
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
			Settings: serving.SchedulerSettings{Algorithm: serving.SchedulerAlgorithmProjectedWorkHistory,
				ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second, MaxObservationAge: time.Second,
				Replays: 1, MaxCandidates: 1, MaxReplicas: 1, MaxInstances: 1, MaxReplayRequests: 10, MaxCPUs: 4,
				Controls: serving.SchedulerControls{Placement: true, Resources: true, Replicas: true,
					Sharing: true, Routing: true, Batching: true, Power: true}},
			CPUInventory: map[string][]int{node.Name: {0, 1, 2, 3}},
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
	config.Settings.Replays = 1_000_000_000
	if err := controller.SetConfiguration(config); err != nil {
		t.Fatal(err)
	}
	before := controller.State()
	decisions := 0
	controller.config.OnDecision = func(serving.SchedulerDecision) error { decisions++; return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	decision, err := controller.Tick(ctx, time.Now().UTC())
	if !errors.Is(err, context.DeadlineExceeded) || decision.Status != "" {
		t.Fatalf("CPU deadline became a publishable decision: %+v %v", decision, err)
	}
	after := controller.State()
	if decisions != 0 || !after.Snapshot.At.Equal(before.Snapshot.At) || after.Decision.At != before.Decision.At ||
		controller.work != nil || len(after.Actions) != 0 || len(after.Results) != 0 {
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
	before := controller.work
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
	if controller.work != before || len(controller.State().Actions) != 0 {
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
	config.CPUInventory = map[string][]int{}
	config.PowerRequests = map[string]*edge.NodeActuation{}
	want := map[string]struct {
		reasons     []string
		measurement bool
	}{
		base.Name:   {},
		"bare":      {reasons: []string{serving.NodePlacementInventoryMissing}},
		"unpowered": {reasons: []string{serving.NodePowerModeUnavailable}},
		"measured":  {measurement: true},
	}
	for _, name := range []string{"control-plane", "bare", "unpowered", "measured"} {
		node := base.DeepCopy()
		node.Name, node.UID = name, types.UID(name+"-uid")
		switch name {
		case "control-plane":
			node.Labels[controlPlaneLabel] = ""
		case "measured":
			node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Effect: core.TaintEffectNoSchedule}}
		}
		if _, err := controller.kube.CoreV1().Nodes().Create(ctx, node, meta.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for name := range want {
		if name != "bare" {
			config.CPUInventory[name] = []int{0, 1, 2, 3}
		}
		if name != "unpowered" { // base Node UID is "node-uid" too
			config.PowerRequests[name+"-uid/fixed"] = &edge.NodeActuation{}
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
		if !ok || !node.Ready || !slices.Equal(node.Reasons, expected.reasons) || node.Measurement != expected.measurement {
			t.Fatalf("%s: ready=%v reasons=%v measurement=%v", node.Node.Name, node.Ready, node.Reasons, node.Measurement)
		}
	}
}

func TestSchedulerLaunchTracksGoalOnlyForNonFixedActions(t *testing.T) {
	for _, fixed := range []bool{true, false} {
		controller := &SchedulerController{runs: map[string]*actionRun{}}
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
	controller := &SchedulerController{runtime: observer.runtime}
	profile := &serving.Profile{ID: "profile"}
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	run := &actionRun{action: serving.Action{Kind: "prepare", Instance: "slot", Node: node}, result: &OperationResult{Status: "complete", Operation: Operation{Prepare: &PrepareSpec{Instance: serving.SchedulerInstance{ID: "slot", Node: node, Profile: profile}}}}}
	if controller.actionEffectPresent(serving.SchedulerSnapshot{}, run) {
		t.Fatal("history treated as current prepared effect")
	}
	instance := serving.ReadyInstance{State: serving.InstanceState{ID: "pod/container", TargetID: "slot", Node: node, Profile: profile.ID, Ready: true, Native: &native.State{Ready: true}}, Profile: profile, Weight: 1, Capacity: 1, Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
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
