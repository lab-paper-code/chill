package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

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
