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

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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
			failing, slow := api.failing, api.slowNode
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

// d9: each source refresh read the Service and the Pods of every instance
// through the shared client, so its cost grew with the instance count.
func TestObserverRefreshReadsAFixedNumberOfObjects(t *testing.T) {
	const instances = 6
	api := newObserverAPI(t, instances)
	api.refresh(t) // the first refresh also opens the Pod watch and log follows
	time.Sleep(100 * time.Millisecond)
	reads := api.refresh(t)
	t.Logf("shared-client reads per refresh with %d instances: %d %v", instances, reads, api.reads)
	if api.ready() != instances {
		t.Fatalf("ready %d of %d: %+v", api.ready(), instances, api.observer.Issues())
	}
	if reads > 4 {
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

// d9: per-instance reads that failed in the throttled client published every
// instance not Ready, so no accepted request was dispatched.
func TestObserverKeepsVerifiedReadinessWhenAReadFails(t *testing.T) {
	for _, failing := range []string{"/services?", "/pods?"} {
		t.Run(failing, func(t *testing.T) {
			const instances = 6
			api := newObserverAPI(t, instances)
			api.refresh(t)
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
			if api.ready() != instances || issues != instances {
				t.Fatalf("a failed read changed verified readiness: ready %d of %d, issues %+v", api.ready(), instances, api.observer.Issues())
			}
		})
	}
}
