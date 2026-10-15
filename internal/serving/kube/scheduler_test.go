package kube

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

type schedulerBlockedArchive struct {
	managerArchive
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *schedulerBlockedArchive) PutImmutable(ctx context.Context, identity evidencestore.ObjectIdentity, input io.Reader) (evidencestore.Receipt, error) {
	a.once.Do(func() {
		close(a.entered)
		<-a.release
	})
	return a.managerArchive.PutImmutable(ctx, identity, input)
}

func schedulerCaptureClockController(t *testing.T, slowLive bool) *SchedulerController {
	t.Helper()
	manager, _ := newManagerHarness(t)
	pod, node, profile, class := observerSourceFixture()
	profile.RuntimeMetadata.RuntimeVersion = "test-runtime"
	profile.RuntimeMetadata.Inputs = []native.InputSpec{}
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
			actual.ModelPath = "/model/model.onnx"
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
	return &SchedulerController{store: manager.store, kube: kube, runtime: manager.runtime, observer: manager.observer, journal: manager.journal,
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
		!instance.ObservedAt.Equal(controller.runtime.Pools()[0].Instances[0].ObservedAt) {
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
	archive := &schedulerBlockedArchive{managerArchive: managerArchive{t.TempDir()}, entered: make(chan struct{}), release: make(chan struct{})}
	history, err := serving.NewJournal(serving.JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: archive})
	if err != nil {
		t.Fatal(err)
	}
	var release sync.Once
	releaseHistory := func() { release.Do(func() { close(archive.release) }) }
	t.Cleanup(func() {
		releaseHistory()
		_ = history.Close(context.Background())
	})
	for index, at := range []time.Time{from.Add(-time.Nanosecond), from, cutoff.Add(-time.Nanosecond), cutoff} {
		if err = history.RequestEvent(serving.RequestEvent{RequestID: strconv.Itoa(index), WorkloadUID: "clock-history", Kind: "received", At: at, ReceivedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	archived := make(chan error, 1)
	go func() { archived <- history.Close(ctx) }()
	<-archive.entered // Close holds the real journal lock while publication waits.
	controller.journal = history
	ticked := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := controller.Tick(ctx, cutoff)
		ticked <- err
	}()
	<-started
	time.Sleep(2 * controller.config.Settings.MaxObservationAge)
	historyReleasedAt := time.Now()
	releaseHistory()
	if err = <-archived; err != nil {
		t.Fatal(err)
	}
	if err = <-ticked; err != nil {
		t.Fatal(err)
	}
	state := schedulerCaptureClockState(t, controller)
	snapshot, decision := state.Snapshot, state.Decision
	if !snapshot.ObservationEnd.Equal(cutoff) || !decision.Search.WindowStart.Equal(from) || !decision.Search.WindowEnd.Equal(cutoff) || decision.Search.Received != 2 {
		t.Fatalf("live reads moved the original half-open history interval: snapshot=%+v search=%+v", snapshot, decision.Search)
	}
	node, instance := snapshot.Nodes[0], snapshot.Instances[0]
	if node.ObservedAt.Before(historyReleasedAt) || instance.ObservedAt.Before(historyReleasedAt) {
		t.Fatalf("live sources were refreshed before delayed history completed: node=%s instance=%s released=%s", node.ObservedAt, instance.ObservedAt, historyReleasedAt)
	}
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

func TestIndependentMembershipMutationsDoNotShareOneNodeLock(t *testing.T) {
	observer := newTestObserver(t)
	nodes := map[string]*core.Node{"a": {ObjectMeta: meta.ObjectMeta{Name: "a", UID: "a-uid", Labels: map[string]string{ServingPoolLabel: "old"}}}, "b": {ObjectMeta: meta.ObjectMeta{Name: "b", UID: "b-uid", Labels: map[string]string{ServingPoolLabel: "old"}}}}
	var mu sync.Mutex
	aStarted := make(chan struct{})
	bStarted := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/pods") {
			if r.URL.Query().Get("watch") == "true" {
				w.WriteHeader(http.StatusOK)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			if err := json.NewEncoder(w).Encode(core.PodList{TypeMeta: meta.TypeMeta{Kind: "PodList", APIVersion: "v1"}}); err != nil {
				t.Error(err)
			}
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/api/v1/nodes/")
		if r.Method == http.MethodPatch {
			if name == "a" {
				close(aStarted)
				<-release
			} else {
				close(bStarted)
			}
			mu.Lock()
			nodes[name].Labels[ServingPoolLabel] = "new"
			mu.Unlock()
		}
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/api/v1/nodes" {
			if err := json.NewEncoder(w).Encode(core.NodeList{TypeMeta: meta.TypeMeta{Kind: "NodeList", APIVersion: "v1"}, Items: []core.Node{*nodes["a"], *nodes["b"]}}); err != nil {
				t.Error(err)
			}
			return
		}
		if node := nodes[name]; node != nil {
			if err := json.NewEncoder(w).Encode(node); err != nil {
				t.Error(err)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
		server.CloseClientConnections()
		server.Close()
	})
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	observer.kube = kube
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 2)
	go func() { result <- observer.MoveNodePool(ctx, serving.NodeReference{Name: "a", UID: "a-uid"}, "new") }()
	select {
	case <-aStarted:
	case <-ctx.Done():
		t.Fatal("first actual Node mutation never started")
	}
	go func() { result <- observer.MoveNodePool(ctx, serving.NodeReference{Name: "b", UID: "b-uid"}, "new") }()
	select {
	case <-bStarted:
		close(release)
	case <-ctx.Done():
		close(release)
		t.Fatal("independent Node mutation blocked behind unrelated Node API call")
	}
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"a", "b"} {
		actual, err := kube.CoreV1().Nodes().Get(ctx, name, meta.GetOptions{})
		if err != nil || actual.Labels[ServingPoolLabel] != "new" {
			t.Fatal("desired membership was not actually read back", name, err)
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
		if err := json.NewEncoder(w).Encode(core.Node{ObjectMeta: meta.ObjectMeta{Name: "a", UID: "node-a", Labels: map[string]string{ServingPoolLabel: "measurement-test"}}}); err != nil {
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
	instance := serving.ReadyInstance{State: serving.InstanceState{ID: "pod/container", TargetID: "slot", Node: node, Pool: "pool", Profile: profile.ID, Ready: true, Native: &native.State{Ready: true}}, Profile: profile, Weight: 1, Capacity: 1, Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
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
		wantReady  bool
		wantCPU    int
		wantMemory bool
	}{
		{name: "fractional requests fit", cpu: "10m", reserved: []int{0}, memory: 1000, wantReady: true, wantCPU: 1},
		{name: "real CPU overbudget", cpu: "110m", reserved: []int{0}, memory: 1000, wantCPU: 2},
		{name: "missing exclusive reservation", cpu: "10m", memory: 1000, wantCPU: 1},
		{name: "real memory overbudget", cpu: "10m", reserved: []int{0}, memory: 99, wantReady: true, wantCPU: 1, wantMemory: true},
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
			if sn.Ready != tc.wantReady || len(sn.NonServing) != 1 || len(snapshot.Instances) != 1 {
				t.Fatal("incorrect readiness or physical classification", sn, snapshot.Instances)
			}
			allocation := sn.NonServing[0]
			if allocation.CPUCount != tc.wantCPU || allocation.MemoryBytes != 100 || allocation.CPUReserved != (len(tc.reserved) > 0) {
				t.Fatal("non-serving requests were not aggregated exactly", allocation)
			}
			snapshot.Nodes = []serving.SchedulerNode{sn}
			settings := serving.SchedulerSettings{Algorithm: serving.SchedulerAlgorithmFixedHorizon, ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second, MaxObservationAge: time.Second, Replays: 1, MaxCandidates: 1, MaxReplicas: 1, MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 2}
			// Isolate the captured non-serving budget in the actual consumer.
			snapshot.Instances = nil
			decision := serving.EvaluateScheduler(settings, snapshot)
			cpuInfeasible, memoryInfeasible, unknownAffinity := false, false, false
			for _, reason := range decision.Keep.Reasons {
				cpuInfeasible = cpuInfeasible || strings.HasPrefix(reason, "CPUBudgetInfeasible:")
				memoryInfeasible = memoryInfeasible || strings.HasPrefix(reason, "MemoryBudgetInfeasible:")
				unknownAffinity = unknownAffinity || strings.HasPrefix(reason, "UnknownExclusiveCPUAffinity:")
			}
			if cpuInfeasible != (len(tc.reserved) > 0 && tc.wantCPU > len(tc.reserved)) || memoryInfeasible != tc.wantMemory || unknownAffinity != (len(tc.reserved) == 0) {
				t.Fatal("consumer miscounted the captured budget", decision.Keep.Reasons)
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
	if sn.Ready {
		t.Fatal("overflowing effective memory requests became an available node")
	}
}

func TestObservedOverheadReusesRemovedExecutorForCompatibleSLO(t *testing.T) {
	manager, _ := newManagerHarness(t)
	d := &serving.Definition{Name: "original", UID: "original-uid", Generation: 1, Model: serving.Model{Name: "model", UID: "model-uid", Generation: 1, Paths: []serving.ModelPath{{Name: "native", Artifact: "onnx", Path: "/model.onnx"}}}, SLO: serving.SLO{Percentile: 95, TargetLatency: time.Second}}
	profile := managerProfile(d)
	profile.NodeName, profile.Condition.NodeUID = "node", "node-uid"
	profile.RuntimeMetadata = &native.RuntimeMetadata{CPUAffinity: []int{0}, IntraOpThreads: 1}
	node := serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}
	physical := serving.ReadyInstance{State: serving.InstanceState{ID: "removed-pod/container", Node: node, Pool: "pool", Profile: profile.ID, Ready: true}, Profile: &profile, Capacity: 4, Weight: 1, Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
		panic("regression only observes physical state")
	}}
	if err := manager.runtime.InstallInstances([]serving.ReadyInstance{physical}); err != nil {
		t.Fatal(err)
	}
	if err := manager.journal.DefinitionRecord(d); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	for _, event := range []serving.RequestEvent{
		{RequestID: "measured", WorkloadUID: d.UID, Definition: d.Identity(), Kind: "backend_exchange_finished", Outcome: "completed", At: at, ReceivedAt: at, Assignment: &serving.Assignment{Instance: physical.State.ID, Node: node, Profile: profile.ID, Artifact: "onnx", ExecutionPath: "native"}, NativeProgress: &serving.BackendProgress{Phase: "finished", ProfileID: profile.ID, ExecutionComplete: true, NativeElapsedMS: 6}, ClientSpanNanos: int64(10 * time.Millisecond), ElapsedNanos: int64(20 * time.Millisecond)},
		{RequestID: "measured", Kind: "response_finished", Outcome: "completed", At: at, ReceivedAt: at, ElapsedNanos: int64(23 * time.Millisecond)},
	} {
		if err := manager.journal.RequestEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.runtime.InstallInstances(nil); err != nil {
		t.Fatal(err)
	}
	for _, pool := range manager.runtime.Pools() {
		if len(pool.Instances) != 0 {
			t.Fatal("measured physical executor remains present", pool)
		}
	}
	overheads, err := manager.journal.ObservedOverhead(at.Add(-time.Second), at.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	other := *d
	other.Name, other.UID, other.SLO = "compatible", "compatible-uid", serving.SLO{Percentile: 99, TargetLatency: 500 * time.Millisecond}
	if !serving.MatchesProfile(&other, &profile) {
		t.Fatal("regression requires a compatible but separately unmeasured SLO UID")
	}
	target := serving.SchedulerInstance{ID: "future-slot", Node: node, Pool: "pool", Profile: &profile, Allocation: serving.SchedulerAllocation{CPUs: []int{0}, Threads: 1}, WorkloadUIDs: []string{d.UID, other.UID}, CompletionOverhead: 99 * time.Millisecond, EnvelopeSource: "configured bootstrap"}
	observeOverhead(&target, overheads)
	if !target.CompletionObserved || target.CompletionOverhead != 17*time.Millisecond || !strings.Contains(target.EnvelopeSource, physical.State.ID) {
		t.Fatalf("removed measured condition was not reused across compatible SLO UIDs: %+v", target)
	}
	for _, name := range []string{"different node", "different profile", "different runtime", "different CPU", "different GPU", "expired window"} {
		t.Run(name, func(t *testing.T) {
			instance := target
			p := profile
			instance.Profile = &p
			observations := overheads
			switch name {
			case "different node":
				instance.Node.UID = "replacement-node-uid"
			case "different profile":
				p.ID = "unmeasured-profile"
			case "different runtime":
				p.Condition.Backend = "different-backend"
			case "different CPU":
				instance.Allocation.CPUs = []int{1}
			case "different GPU":
				instance.Allocation.GPUUUID = "unmeasured-gpu"
			case "expired window":
				observations, err = manager.journal.ObservedOverhead(at.Add(time.Second), at.Add(2*time.Second))
				if err != nil {
					t.Fatal(err)
				}
			}
			observeOverhead(&instance, observations)
			if instance.CompletionObserved {
				t.Fatalf("unmeasured Node/runtime condition reused an observation: %+v", instance)
			}
		})
	}
	// Selected immutable facts and current live facts use the same conservative
	// matcher/reducer output; removing the selector cannot retain the old flag.
	calibration := overheads[physical.State.ID]
	calibration.Completion = 30 * time.Millisecond
	observeOverhead(&target, map[string]serving.ObservedOverhead{"calibration/old-executor": calibration, physical.State.ID: overheads[physical.State.ID]})
	if !target.CompletionMeasurement.Valid() || target.CompletionOverhead != 30*time.Millisecond {
		t.Fatalf("live and selected calibration must combine by maximum: %+v", target)
	}
	observeOverhead(&target, nil)
	if target.CompletionObserved || target.CompletionMeasurement != nil {
		t.Fatalf("removed calibration retained usable facts: %+v", target)
	}
}

func TestNonServingGPUOwnershipQuarantinesOnlyItsNode(t *testing.T) {
	node := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "quarantined-gpu", UID: "gpu-node"}}
	state := serving.SchedulerNode{
		Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: "pool", Ready: true,
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
	if state.Ready || state.Reason == "" || len(snapshot.Reasons) != 0 {
		t.Fatalf("node-local GPU uncertainty became a global evaluation gap: node=%+v reasons=%v", state, snapshot.Reasons)
	}
	if len(state.NonServing) != 1 || state.NonServing[0].CPUCount != 1 ||
		state.NonServing[0].MemoryBytes != 1024 || !state.NonServing[0].CPUReserved {
		t.Fatalf("quarantine discarded actual non-serving resource budget: %+v", state.NonServing)
	}
}
