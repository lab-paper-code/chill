package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// capturePodAPI keeps the real client-go HTTP List/decode and native consumer
// paths, while making inventory changes and query counts deterministic.
type capturePodAPI struct {
	mu       sync.Mutex
	pods     []core.Pod
	queries  []string
	failPods bool
}

func newCapturePodAPI(t *testing.T, c *SchedulerController) *capturePodAPI {
	t.Helper()
	api := &capturePodAPI{}
	backend := c.kube.CoreV1().RESTClient().Get().AbsPath("/").URL()
	proxy := httputil.NewSingleHostReverseProxy(backend)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/pods/") && !strings.HasSuffix(r.URL.Path, "/log") {
			api.mu.Lock()
			defer api.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			for _, pod := range api.pods {
				if r.URL.Path == "/api/v1/namespaces/"+pod.Namespace+"/pods/"+pod.Name {
					if err := json.NewEncoder(w).Encode(pod); err != nil {
						t.Error(err)
					}
					return
				}
			}
			http.Error(w, "Pod not found", http.StatusNotFound)
			return
		}
		watch, _ := strconv.ParseBool(r.URL.Query().Get("watch"))
		if !strings.HasSuffix(r.URL.Path, "/pods") || watch {
			proxy.ServeHTTP(w, r)
			return
		}
		api.mu.Lock()
		defer api.mu.Unlock()
		api.queries = append(api.queries, r.URL.Path+"?"+r.URL.RawQuery)
		if api.failPods {
			http.Error(w, "Pod inventory unavailable", http.StatusServiceUnavailable)
			return
		}
		fieldSelector, err := fields.ParseSelector(r.URL.Query().Get("fieldSelector"))
		if err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		labelSelector, err := labels.Parse(r.URL.Query().Get("labelSelector"))
		if err != nil {
			t.Error(err)
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		list := core.PodList{TypeMeta: meta.TypeMeta{Kind: "PodList", APIVersion: "v1"}, ListMeta: meta.ListMeta{ResourceVersion: "1"}}
		for _, pod := range api.pods {
			if r.URL.Path != "/api/v1/pods" && r.URL.Path != "/api/v1/namespaces/"+pod.Namespace+"/pods" {
				continue
			}
			if labelSelector.Matches(labels.Set(pod.Labels)) && fieldSelector.Matches(fields.Set{"status.phase": string(pod.Status.Phase)}) {
				list.Items = append(list.Items, pod)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(list); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		_ = c.observer.Close()
		server.CloseClientConnections()
		server.Close()
	})
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, QPS: 1000, Burst: 1000})
	if err != nil {
		t.Fatal(err)
	}
	c.kube, c.observer.kube = kube, kube
	return api
}

func (a *capturePodAPI) setPods(pods ...*core.Pod) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pods, a.queries = nil, nil
	for _, pod := range pods {
		a.pods = append(a.pods, *pod.DeepCopy())
	}
}

func (a *capturePodAPI) inventoryQueries() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var result []string
	for _, query := range a.queries {
		if !strings.Contains(query, "labelSelector=") {
			result = append(result, query)
		}
	}
	return result
}

func captureForeignPod(memory string) *core.Pod {
	return &core.Pod{ObjectMeta: meta.ObjectMeta{Name: "release-control", Namespace: "release-system", UID: "release-control"},
		Spec:   core.PodSpec{NodeName: "node", Containers: []core.Container{{Name: "control", Resources: core.ResourceRequirements{Requests: core.ResourceList{core.ResourceMemory: resource.MustParse(memory)}}}}},
		Status: core.PodStatus{Phase: core.PodRunning}}
}

func TestSchedulerCaptureSharesOnlyCompleteClusterPodBaseline(t *testing.T) {
	for _, cluster := range []bool{false, true} {
		name := "namespace observer"
		if cluster {
			name = "cluster observer"
		}
		t.Run(name, func(t *testing.T) {
			c := schedulerCaptureClockController(t, nil)
			api := newCapturePodAPI(t, c)
			pod, node, _, _ := observerSourceFixture()
			foreign := captureForeignPod("13")
			foreign.Spec.NodeName = node.Name
			terminal := foreign.DeepCopy()
			terminal.Name, terminal.UID, terminal.Status.Phase = "finished-control", "finished-control", core.PodSucceeded
			var rows []observation.ClusterObservation
			var rowsMu sync.Mutex
			if cluster {
				c.observer.config.SelectClusterPod = func(*core.Pod) bool { return true }
				c.observer.config.OnCluster = func(row observation.ClusterObservation) error {
					rowsMu.Lock()
					defer rowsMu.Unlock()
					rows = append(rows, row)
					return nil
				}
			}
			api.setPods(pod, foreign, terminal)
			snapshot, err := c.capture(context.Background(), time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Instances) != 1 || !snapshot.Instances[0].Ready || len(snapshot.Nodes) != 1 || len(snapshot.Nodes[0].NonServing) != 1 || snapshot.Nodes[0].NonServing[0].MemoryBytes != 13 {
				t.Fatalf("capture lost serving identity or cross-namespace occupancy: %+v", snapshot)
			}
			queries := api.inventoryQueries()
			if cluster {
				if len(queries) != 1 || queries[0] != "/api/v1/pods?" {
					t.Fatalf("cluster capture repeated inventory HTTP/decode: %v", queries)
				}
				seen := map[string]bool{}
				rowsMu.Lock()
				defer rowsMu.Unlock()
				for _, row := range rows {
					if row.Pod != nil {
						seen[row.Pod.UID] = true
					}
				}
				if !seen[string(terminal.UID)] || !seen[string(foreign.UID)] {
					t.Fatalf("scheduler projection removed release/control/terminal observations: %+v", rows)
				}
			} else if len(queries) != 2 || !strings.HasPrefix(queries[0], "/api/v1/namespaces/"+pod.Namespace+"/pods?") || !strings.Contains(queries[1], "fieldSelector=") {
				t.Fatalf("namespace-only baseline replaced required POOL query: %v", queries)
			}
			api.mu.Lock()
			defer api.mu.Unlock()
			if len(api.queries) != len(queries)+1 || !strings.Contains(strings.Join(api.queries, "\n"), "labelSelector=") {
				t.Fatalf("capture skipped post-native exact-executor verification: %v", api.queries)
			}
		})
	}
}

func TestSchedulerSharedCaptureChangesTerminationAbsenceAndFailure(t *testing.T) {
	c := schedulerCaptureClockController(t, nil)
	api := newCapturePodAPI(t, c)
	c.observer.config.SelectClusterPod = func(*core.Pod) bool { return true }
	c.observer.config.OnCluster = func(observation.ClusterObservation) error { return nil }
	pod, node, _, _ := observerSourceFixture()
	foreign := captureForeignPod("13")
	foreign.Spec.NodeName = node.Name
	capture := func() serving.SchedulerSnapshot {
		t.Helper()
		snapshot, err := c.capture(context.Background(), time.Now(), c.config)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	api.setPods(pod, foreign)
	first := capture()
	if len(first.Instances) != 1 || !first.Instances[0].Ready {
		t.Fatalf("missing initial native consumer fact: %+v", first.Instances)
	}
	pod.Annotations[servingstate.ServingWeightAnnotation] = "11"
	foreign.Spec.Containers[0].Resources.Requests[core.ResourceMemory] = resource.MustParse("37")
	api.setPods(pod, foreign)
	changed := capture()
	if changed.Instances[0].Weight != 11 || changed.Nodes[0].NonServing[0].MemoryBytes != 37 || len(api.inventoryQueries()) != 1 {
		t.Fatalf("shared list reused a previous cycle: %+v", changed)
	}
	status := &pod.Status.ContainerStatuses[0]
	status.State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: status.ContainerID}}
	pod.Status.Phase, foreign.Status.Phase = core.PodSucceeded, core.PodFailed
	api.setPods(pod, foreign)
	terminal := capture()
	if len(terminal.Instances) != 1 || !terminal.Instances[0].Terminated || !terminal.Instances[0].Stopped || terminal.Instances[0].Ready || len(terminal.Nodes[0].NonServing) != 0 {
		t.Fatalf("terminal raw facts changed scheduler occupancy or executor proof: %+v", terminal)
	}
	api.setPods()
	missing := capture()
	if len(missing.Instances) != 1 || !missing.Instances[0].Terminated || len(api.inventoryQueries()) != 2 {
		t.Fatalf("missing Pod erased retained terminal proof or skipped reconciliation: %+v queries=%v", missing.Instances, api.inventoryQueries())
	}
	api.mu.Lock()
	api.failPods = true
	api.mu.Unlock()
	if _, err := c.capture(context.Background(), time.Now(), c.config); err == nil {
		t.Fatal("failed authoritative inventory became a successful capture")
	}
	api.mu.Lock()
	api.failPods = false
	api.mu.Unlock()
	pod.Status.Phase = core.PodRunning
	status.ContainerID = "containerd://capture-recovery"
	status.State = core.ContainerState{Running: &core.ContainerStateRunning{}}
	api.setPods(pod)
	recovered := capture()
	var ready, stopped int
	for _, instance := range recovered.Instances {
		if instance.Ready {
			ready++
		}
		if instance.Terminated {
			stopped++
		}
	}
	if ready != 1 || stopped != 1 {
		t.Fatalf("recovery lost old executor proof or new native readiness: %+v", recovered.Instances)
	}
}

func TestObserverCaptureIndependentRefreshAndWatchRestart(t *testing.T) {
	c := schedulerCaptureClockController(t, nil)
	api := newCapturePodAPI(t, c)
	pod, _, _, _ := observerSourceFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	api.setPods(pod)
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	api.setPods(pod)
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if queries := api.inventoryQueries(); len(queries) != 1 || !strings.Contains(queries[0], "fieldSelector=") {
		t.Fatalf("independent refresh lost existing filtered-list semantics: %v", queries)
	}
	c.observer.mu.Lock()
	c.observer.podWatch.Stop()
	c.observer.mu.Unlock()
	for {
		c.observer.mu.Lock()
		ended := c.observer.podWatch == nil
		c.observer.mu.Unlock()
		if ended {
			break
		}
		if err := ctx.Err(); err != nil {
			t.Fatal(err)
		}
		runtime.Gosched()
	}
	api.setPods(pod)
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if queries := api.inventoryQueries(); len(queries) != 1 || strings.Contains(queries[0], "fieldSelector=") {
		t.Fatalf("watch restart failed to restore full terminal baseline: %v", queries)
	}
}
