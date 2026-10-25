package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

const observerTestRuntime = "test-runtime"

// observerAPI serves S ready native instances on one Node and counts the
// Kubernetes reads one observer refresh makes through the shared clientset.
type observerAPI struct {
	observer *Observer
	mu       sync.Mutex
	reads    map[string]int
	failing  string // suffix of "path?query" whose reads fail
	slowNode time.Duration
	hang     chan struct{} // closed when native state reads may answer
	owner    types.UID     // Workload that owns every listed Pod
}

func newObserverAPI(t *testing.T, instances int) *observerAPI {
	t.Helper()
	manager, _ := newManagerHarness(t)
	base, node, profile, class := observerSourceFixture()
	profile.RuntimeMetadata.RuntimeVersion = observerTestRuntime
	profile.RuntimeMetadata.Inputs = profile.Condition.Preprocessor.Inputs
	profile.RuntimeMetadata.Outputs = profile.Condition.Preprocessor.Outputs
	profile.RuntimeMetadata.InitializedBatches = []int{1}
	if err := manager.store.client.Create(context.Background(), &class); err != nil {
		t.Fatal(err)
	}
	manager.observer.SetProfiles([]serving.Profile{profile})
	pods := map[string]*core.Pod{}
	services := map[string]*core.Service{}
	var podList core.PodList
	var serviceList core.ServiceList
	for i := range instances {
		pod := base.DeepCopy()
		pod.Name = "instance-" + string(rune('a'+i))
		pod.UID = types.UID("pod-uid-" + string(rune('a'+i)))
		pod.Labels[instancePodUIDLabel] = string(pod.UID)
		service := observerServiceFixture(pod)
		pods[pod.Name], services[service.Name] = pod, service
		podList.Items = append(podList.Items, *pod)
		serviceList.Items = append(serviceList.Items, *service)
	}
	api := &observerAPI{observer: manager.observer, reads: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("watch") == "true" || strings.HasSuffix(r.URL.Path, "/log") {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		var response any
		if strings.HasSuffix(r.URL.Path, "/state") {
			api.mu.Lock()
			hang := api.hang
			api.mu.Unlock()
			if hang != nil {
				select {
				case <-hang:
				case <-r.Context().Done():
					return
				}
			}
			actual := *profile.RuntimeMetadata
			actual.ModelFiles = deliveredModelFiles(profile.Condition.Artifact.Files)
			response = native.State{At: time.Now().UTC(), Ready: true, Metadata: native.Metadata{
				Model: "chill_model", Mode: "serving", ProfileID: profile.ID, AppliedAt: time.Now().UTC(),
				Runtime: actual, MaxQueueRequests: 4, MaxBatchSize: 1, ModelInstanceCount: 1,
			}}
		} else {
			api.mu.Lock()
			api.reads[r.Method+" "+r.URL.Path]++
			failing, slow, owner := api.failing, api.slowNode, api.owner
			api.mu.Unlock()
			if failing != "" && strings.HasSuffix(r.URL.Path+"?"+r.URL.RawQuery, failing) {
				http.Error(w, "unavailable", http.StatusServiceUnavailable)
				return
			}
			name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			switch {
			case r.URL.Path == "/api/v1/nodes":
				time.Sleep(slow)
				response = core.NodeList{TypeMeta: meta.TypeMeta{Kind: "NodeList", APIVersion: "v1"}, Items: []core.Node{*node}}
			case strings.HasSuffix(r.URL.Path, "/pods"):
				list := core.PodList{TypeMeta: meta.TypeMeta{Kind: "PodList", APIVersion: "v1"}}
				selector := strings.TrimPrefix(r.URL.Query().Get("labelSelector"), instancePodUIDLabel+"=")
				for _, pod := range podList.Items {
					if owner != "" {
						pod.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "Workload", Name: "w", UID: owner}}
					}
					if r.URL.Query().Get("labelSelector") == "" || string(pod.UID) == selector {
						list.Items = append(list.Items, pod)
					}
				}
				response = list
			case strings.HasSuffix(r.URL.Path, "/services"):
				response = core.ServiceList{TypeMeta: meta.TypeMeta{Kind: "ServiceList", APIVersion: "v1"}, Items: serviceList.Items}
			case pods[name] != nil && strings.Contains(r.URL.Path, "/pods/"):
				response = pods[name]
			case services[name] != nil:
				response = services[name]
			default:
				http.NotFound(w, r)
				return
			}
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
	kube, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, QPS: 1000, Burst: 1000})
	if err != nil {
		t.Fatal(err)
	}
	manager.observer.kube = kube
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	manager.observer.native.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	return api
}

func (a *observerAPI) refresh(t *testing.T) int {
	t.Helper()
	a.mu.Lock()
	clear(a.reads)
	a.mu.Unlock()
	if err := a.observer.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	total := 0
	for _, n := range a.reads {
		total += n
	}
	return total
}

func (a *observerAPI) ready() int {
	ready := 0
	for _, instance := range a.observer.runtime.Instances() {
		if instance.Ready {
			ready++
		}
	}
	return ready
}

// Independent source publication verifies each completed probe without waiting
// for another executor's native HTTP response.
func TestObserverRefreshVerifiesEachCompletedSource(t *testing.T) {
	const instances = 6
	api := newObserverAPI(t, instances)
	api.refresh(t) // the first refresh also opens the Pod watch and log follows
	time.Sleep(100 * time.Millisecond)
	reads := api.refresh(t)
	t.Logf("shared-client reads per refresh with %d instances: %d %v", instances, reads, api.reads)
	if api.ready() != instances {
		t.Fatalf("ready %d of %d: %+v", api.ready(), instances, api.observer.Issues())
	}
	if reads > instances+3 {
		t.Fatalf("one refresh made %d shared-client reads for %d instances", reads, instances)
	}
}

// A native state read that never answers held the source lock until the
// refresh context ended.
func TestObserverRefreshBoundsNativeProbe(t *testing.T) {
	api := newObserverAPI(t, 1)
	hang := make(chan struct{})
	defer close(hang)
	api.mu.Lock()
	api.hang = hang
	api.mu.Unlock()
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := api.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > nativeProbeTimeout+time.Second || api.ready() != 0 {
		t.Fatalf("hung probe held the refresh %v, ready %d", took, api.ready())
	}
}

// SCH-50: failed reads close dispatch but retain the physical inventory.
func TestObserverPreservesInventoryWhenAReadFails(t *testing.T) {
	for _, failing := range []string{"/services?", "/pods?"} {
		t.Run(failing, func(t *testing.T) {
			const instances = 6
			api := newObserverAPI(t, instances)
			api.refresh(t)
			before := api.observer.runtime.Instances()
			if api.ready() != instances {
				t.Fatalf("ready %d of %d", api.ready(), instances)
			}
			api.mu.Lock()
			api.failing = failing
			api.mu.Unlock()
			api.refresh(t)
			issues := 0
			for _, issue := range api.observer.Issues() {
				if strings.HasPrefix(issue.Source, "chill-system/instance-") {
					issues++
				}
			}
			after := api.observer.runtime.Instances()
			if api.ready() != 0 || len(after) != instances || issues != instances {
				t.Fatalf("failed read changed inventory or allowed dispatch: ready %d, instances %d, issues %+v", api.ready(), len(after), api.observer.Issues())
			}
			for i, state := range after {
				if state.ID != before[i].ID || state.PodUID != before[i].PodUID || state.OwnerWorkloadUID != before[i].OwnerWorkloadUID ||
					!state.ObservedAt.Equal(before[i].ObservedAt) || state.AttemptedAt.Before(before[i].AttemptedAt) {
					t.Fatalf("failed read rewrote identity/owner/success time: before %+v after %+v", before[i], state)
				}
			}
		})
	}
}

// Read failures never retain dispatch eligibility, even on the first failure.
func TestObserverClosesDispatchAfterReadFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		owner types.UID
		ready []int // after each refresh with a failing Service list
	}{
		{"Service list keeps failing", "", []int{0, 0}},
		{"owner changes", "other-workload", []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newObserverAPI(t, 1)
			api.refresh(t)
			api.mu.Lock()
			api.failing, api.owner = "/services?", tc.owner
			api.mu.Unlock()
			for i, want := range tc.ready {
				api.refresh(t)
				if api.ready() != want {
					t.Fatalf("refresh %d with a failing Service list: ready %d, want %d", i+1, api.ready(), want)
				}
			}
		})
	}
}

// r7: a prepare poll that waited for the observer lock behind a refresh
// longer than its poll interval started another full refresh right after it.
func TestObserverPollReusesARefreshThatEndedWhileItWaited(t *testing.T) {
	api := newObserverAPI(t, 1)
	api.refresh(t)
	api.mu.Lock()
	api.slowNode = 1500 * time.Millisecond
	clear(api.reads)
	api.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- api.observer.Refresh(context.Background()) }()
	time.Sleep(100 * time.Millisecond)
	if err := api.observer.RefreshWithin(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	api.mu.Lock()
	defer api.mu.Unlock()
	if nodes := api.reads["GET /api/v1/nodes"]; nodes != 1 {
		t.Fatalf("the poll started its own refresh: %d Node lists", nodes)
	}
}

// Capture read every Pod in the cluster to find those on one Node.
func TestOperationsCaptureListsOnlyTheNodePods(t *testing.T) {
	manager, _ := newManagerHarness(t)
	node := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}
	kube := kubefake.NewSimpleClientset(node)
	operations := &Operations{store: manager.store, kube: kube, runtime: manager.runtime, observer: manager.observer, config: OperationConfig{Namespace: "chill-system"}}
	_, _ = operations.Capture(context.Background(), serving.NodeReference{Name: node.Name, UID: string(node.UID)})
	for _, action := range kube.Actions() {
		if list, ok := action.(ktesting.ListAction); ok && action.GetResource().Resource == string(core.ResourcePods) {
			if fields := list.GetListRestrictions().Fields.String(); fields != "spec.nodeName=node" {
				t.Fatalf("capture listed Pods with field selector %q", fields)
			}
			return
		}
	}
	t.Fatal("capture did not list Pods")
}
