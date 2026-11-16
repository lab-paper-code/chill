package kube

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	workloadobserver "github.com/lab-paper-code/chill/internal/workloadobserver"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// control402: the deadline that ended a prepare also expired the context of
// its after-state capture (client-go reported "client rate limiter Wait"), so
// the Pod record at the failure was lost.
func TestPrepareDeadlineCapturesAfterStateWithLiveContext(t *testing.T) {
	manager, _ := newManagerHarness(t)
	fixture, node, profile, class := observerSourceFixture()
	profile.Condition.Actor.MemoryRequestBytes = 128 << 20
	profile.Condition.Actor.MemoryLimitBytes = 1 << 30
	profile.Batches[0].MemoryBytes = 256 << 20
	manager.observer.SetProfiles([]serving.Profile{profile})
	kube := kubefake.NewSimpleClientset(node)
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*core.Service).Spec.ClusterIP = "10.96.0.21"
		return false, nil, nil
	})
	// Like client-go's rate limiter, a read on an expired context fails before any request.
	manager.store.client = fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(testExecutionFacts(), &class, node.DeepCopy()).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return c.Get(ctx, key, object, opts...)
		},
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			return c.List(ctx, list, opts...)
		},
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			pod, ok := object.(*core.Pod)
			if ok {
				pod.UID = fixture.UID
				pod.Labels[servingstate.InstancePodUIDLabel] = string(pod.UID)
				pod.Status = fixture.Status
			}
			if err := c.Create(ctx, object, opts...); err != nil || !ok {
				return err
			}
			if err := kube.Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace); err != nil {
				return err
			}
			// The fake log stream ends at once; keep the source subscribed.
			follow, stop := context.WithCancel(manager.observer.ctx)
			manager.observer.mu.Lock()
			manager.observer.sources[string(pod.UID)] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
				uid: pod.UID, containerID: servingstate.FirstfloorStatus(pod).ContainerID, follow: follow, cancel: stop}
			manager.observer.mu.Unlock()
			return nil
		},
	}).Build()
	manager.observer.kube = kube
	// The executor never becomes Ready, so the prepare runs into its deadline.
	unready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer unready.Close()
	proxy, err := url.Parse(unready.URL)
	if err != nil {
		t.Fatal(err)
	}
	manager.observer.native.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
	operations := &servingops.Operations{Cluster: manager.observer.cluster, Store: manager.store, Kube: kube, Runtime: manager.runtime,
		Observer: manager.observer, Native: manager.observer.native, Config: servingops.OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond}}
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	spec := servingstate.PrepareSpec{Name: fixture.Name, MemoryBytes: 1 << 30,
		Instance: serving.SchedulerInstance{ID: "slot", Node: ref, Profile: &profile, Weight: 1, Allocation: serving.SchedulerAllocation{MemoryBytes: 1 << 30}},
		Store:    storekube.Binding{Store: "evidence", Endpoint: "http://evidence", Bucket: "bucket", Region: "region", CredentialsSecretName: "credentials"},
		Bundle:   evidencestore.ObjectIdentity{Store: "evidence", Key: "bundle", Size: 1},
		Config: native.Config{Mode: "serving", Model: profile.Condition.Actor.Model, ProfilePath: "/profile/bundle.json",
			RuntimeImage: profile.Condition.RuntimeImage, MemoryRequestBytes: 128 << 20, MemoryLimitBytes: 1 << 30,
			ArtifactImage: profile.Condition.Artifact.Image, ArtifactFiles: profile.Condition.Artifact.Files,
			PreprocessorImage: profile.Condition.Preprocessor.Image, PreprocessorPath: profile.Condition.Preprocessor.Path,
			Policy: native.Policy{DecisionID: "deadline", ProfileID: profile.ID, MaxBatchSize: 1}},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	result, err := operations.Execute(ctx, servingstate.Operation{ID: "late-prepare", Kind: servingstate.OperationPrepare, Node: ref, Prepare: &spec})
	if !errors.Is(err, context.DeadlineExceeded) || ctx.Err() == nil {
		t.Fatalf("prepare did not run into its deadline: %v", err)
	}
	if result.Preparation == nil || result.Preparation.PodUID != string(fixture.UID) {
		t.Fatalf("creation receipt lost: %+v", result.Preparation)
	}
	if !slices.ContainsFunc(result.Actual.Pods, func(pod workloadobserver.Pod) bool { return pod.UID == string(fixture.UID) }) {
		t.Fatalf("after-state at the deadline was not captured: actual=%+v error=%s", result.Actual, result.Error)
	}
}
