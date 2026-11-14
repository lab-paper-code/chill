package kube

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

const measurementProfileID = "chill-system/session/session-uid"

// archiveEvidence writes operation measurements as the evidence store does.
type archiveEvidence struct{ managerArchive }

func (archiveEvidence) List(context.Context, string) ([]evidencestore.ObjectIdentity, error) {
	return nil, nil
}

// measurementInstanceSpec is a profile-less fixed-batch instance: the native
// runtime in profiling mode, labelled with the profile its session publishes.
func measurementInstanceSpec(node *core.Node, profile serving.Profile) servingstate.PrepareSpec {
	return servingstate.PrepareSpec{Name: "session-batch-1", MemoryBytes: 1 << 30,
		Instance: serving.SchedulerInstance{ID: "session-batch-1", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)},
			Allocation: serving.SchedulerAllocation{MemoryBytes: 1 << 30}},
		Config: native.Config{Mode: "profiling", Model: profile.Condition.Actor.Model, BatchSizes: []int{1}, MaxBatchSize: 1,
			RuntimeImage: profile.Condition.RuntimeImage, MemoryRequestBytes: 128 << 20, MemoryLimitBytes: 1 << 30,
			ArtifactImage: profile.Condition.Artifact.Image, ArtifactFiles: profile.Condition.Artifact.Files,
			PreprocessorImage: profile.Condition.Preprocessor.Image, PreprocessorPath: profile.Condition.Preprocessor.Path,
			Policy: native.Policy{DecisionID: "session", ProfileID: measurementProfileID, FixedBatchSize: 1}},
	}
}

// A measurement session prepares its instance with Measure(prepare) on a
// measurement-pool Node, without a catalog profile, and removes it with
// Measure(remove). The instance becomes ready on the native runtime's own
// state read through its Pod-owned Service.
func TestMeasurementInstanceIsMeasuredWithoutProfile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fixture, node, profile, class := observerSourceFixture()
	// The profile only supplies the runner fixture's initialization facts;
	// the catalog holds no profile.
	profile.Condition.Preprocessor.Inputs, profile.Condition.Preprocessor.Outputs = []string{"input"}, []string{"output"}
	profile.RuntimeMetadata.RuntimeVersion = observerTestRuntime
	profile.RuntimeMetadata.Inputs, profile.RuntimeMetadata.Outputs = []string{"input"}, []string{"output"}
	profile.RuntimeMetadata.InitializedBatches = []int{1}
	spec := measurementInstanceSpec(node, profile)
	nativeHTTPClient, _ := preparationNativeHTTPClient(t, ctx, spec.Config, profile)
	manager, kube, measure := measurementOperations(t, ctx, fixture, node, class, spec, nativeHTTPClient)

	prepared := measure(servingstate.Operation{ID: "session.1.prepare", Kind: servingstate.OperationPrepare, Node: spec.Instance.Node, Prepare: &spec})
	pod := &core.Pod{}
	if err := manager.store.client.Get(ctx, types.NamespacedName{Namespace: fixture.Namespace, Name: spec.Name}, pod); err != nil {
		t.Fatal(err)
	}
	assertMeasurementInstance(t, ctx, kube, pod, prepared, 1)

	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
	removed := measure(servingstate.Operation{ID: "session.2.remove", Kind: servingstate.OperationRemove, Node: spec.Instance.Node, Instance: id})
	if _, drained := removed.Result.DrainNative[id]; !drained || len(removed.Result.Actual.Pods) != 0 {
		t.Fatalf("remove left the measurement instance: %+v", removed.Result)
	}
}

// measurementOperations is the operations path a session prepares and removes
// its instances through, on a measurement-pool Node: the Kubernetes fakes
// stand in for the kubelet, nativeClient reaches the instance's runtime, and
// measure runs Measure(operation) and requires an accepted record.
func measurementOperations(t *testing.T, ctx context.Context, fixture *core.Pod, node *core.Node, class edge.DeviceClass,
	spec servingstate.PrepareSpec, nativeClient *http.Client) (*managerHarness, *kubefake.Clientset, func(servingstate.Operation) servingops.OperationMeasurement) {
	t.Helper()
	manager, _ := newManagerHarness(t)
	node.Spec.Taints = []core.Taint{{Key: profilingrun.MeasurementTaintKey, Value: "session-uid", Effect: core.TaintEffectNoSchedule}}
	kube := kubefake.NewSimpleClientset(node)
	kube.PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		action.(ktesting.CreateAction).GetObject().(*core.Service).Spec.ClusterIP = "10.96.0.31"
		return false, nil, nil
	})
	pods := core.SchemeGroupVersion.WithResource("pods")
	running := fixture.Status.DeepCopy()
	running.InitContainerStatuses = running.InitContainerStatuses[:2] // artifact and preprocessor
	// The store and the Kubernetes fake share Pods; the interceptors stand in
	// for the kubelet: a created Pod runs, a deleted Pod terminates.
	manager.store.client = fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(testExecutionFacts(), &class, node.DeepCopy()).WithInterceptorFuncs(interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
			pod, ok := object.(*core.Pod)
			if ok {
				pod.UID = fixture.UID
				pod.Labels[servingstate.InstancePodUIDLabel] = string(pod.UID)
				pod.Status = *running.DeepCopy()
			}
			if err := c.Create(ctx, object, opts...); err != nil || !ok {
				return err
			}
			if err := kube.Tracker().Create(pods, pod.DeepCopy(), pod.Namespace); err != nil {
				return err
			}
			// The fake log stream ends at once; a subscribed source keeps EOF
			// from reading as runner termination.
			follow, stop := context.WithCancel(manager.observer.ctx)
			manager.observer.mu.Lock()
			manager.observer.sources[string(pod.UID)] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
				uid: pod.UID, containerID: servingstate.FirstfloorStatus(pod).ContainerID, follow: follow, cancel: stop}
			manager.observer.mu.Unlock()
			return nil
		},
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if err := c.Patch(ctx, object, patch, opts...); err != nil {
				return err
			}
			pod, ok := object.(*core.Pod)
			if !ok {
				return nil
			}
			if err := c.Get(ctx, client.ObjectKeyFromObject(pod), &core.Pod{}); apierrors.IsNotFound(err) {
				return kube.Tracker().Delete(pods, pod.Namespace, pod.Name)
			}
			return kube.Tracker().Update(pods, pod.DeepCopy(), pod.Namespace)
		},
		Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
			if err := c.Delete(ctx, object, opts...); err != nil {
				return err
			}
			if _, ok := object.(*core.Pod); !ok {
				return nil
			}
			pod := &core.Pod{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(object), pod); err != nil {
				return err
			}
			status := servingstate.FirstfloorStatus(pod)
			status.Ready = false
			status.State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: status.ContainerID}}
			pod.Status.Phase = core.PodSucceeded
			if err := c.Status().Update(ctx, pod); err != nil {
				return err
			}
			return kube.Tracker().Update(pods, pod.DeepCopy(), pod.Namespace)
		},
	}).Build()
	manager.observer.kube = kube
	manager.observer.native.HTTPClient = nativeClient
	operations := &servingops.Operations{Cluster: manager.observer.cluster, Store: manager.store, Kube: kube, Runtime: manager.runtime, Observer: manager.observer, Native: manager.observer.native,
		Config: servingops.OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond, Measurement: true,
			MetricLimits: metricobserver.Limits{MaxResultBytes: 64 << 20, MemoryBudgetBytes: 256 << 20}}}
	controller := &SchedulerController{store: manager.store, observer: manager.observer, measurement: operations}
	source := &measurementTestSource{identity: metricobserver.SourceIdentity{Kind: "fixture", NodeName: node.Name, Endpoint: "http://meter", Metric: "power"}}
	evidence := archiveEvidence{managerArchive{directory: t.TempDir()}}
	config := SchedulerControllerConfig{EvidenceStore: "evidence", Evidence: evidence,
		Meters:      []servingops.MeterDeclaration{{ID: "meter", Node: spec.Instance.Node, Scope: servingstate.MeasurementWallScope, Source: source.Identity()}},
		MeterSource: func(metricobserver.SourceIdentity) (metricobserver.Source, error) { return source, nil }}
	measure := func(operation servingstate.Operation) servingops.OperationMeasurement {
		t.Helper()
		request := servingops.MeasurementRequest{Operation: operation,
			Timing:          &edge.MeasurementTimingSpec{ChangeIntervalP95Seconds: "0.005", MaximumRequestSeconds: "0", MaximumReceiptGapSeconds: "0", Source: "empirical fixture"},
			Sampling:        metricobserver.Request{Interval: 2 * time.Millisecond, RequestTimeout: time.Millisecond, Duration: 8 * time.Second},
			OperationBudget: 3 * time.Second, Coverage: powerwindow.Policy{MaximumGap: 10 * time.Millisecond}}
		record, receipt, err := controller.measureOperation(ctx, config, request)
		if err != nil {
			t.Fatalf("Measure(%s) of a measurement instance: %v", operation.Kind, err)
		}
		assertRetainedOperationMeasurement(t, evidence.directory, receipt, request, record, err)
		if record.Verdict != servingstate.MeasurementAccepted || record.WallJoules == nil {
			t.Fatalf("Measure(%s) record not accepted: %+v", operation.Kind, record)
		}
		return record
	}
	return manager, kube, measure
}

// assertMeasurementInstance requires the prepared measurement instance to be a
// profile-less fixed-batch runtime behind its own Service, ready in profiling
// mode, and returns its native state.
func assertMeasurementInstance(t *testing.T, ctx context.Context, kube *kubefake.Clientset, pod *core.Pod, prepared servingops.OperationMeasurement, batch int) native.State {
	t.Helper()
	for _, container := range pod.Spec.InitContainers {
		if container.Name == profileInitContainerName {
			t.Fatal("measurement instance materializes a profile bundle")
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == profileInitContainerName {
			t.Fatal("measurement instance mounts a profile volume")
		}
	}
	if len(pod.Spec.InitContainers) != 2 || pod.Annotations[servingstate.ServingProfileAnnotation] != measurementProfileID {
		t.Fatalf("measurement Pod init containers %d, profile annotation %q", len(pod.Spec.InitContainers), pod.Annotations[servingstate.ServingProfileAnnotation])
	}
	if _, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, servingstate.InstanceServicePrefix+string(pod.UID), meta.GetOptions{}); err != nil {
		t.Fatal("measurement instance has no Pod-owned Service", err)
	}
	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
	state, ok := prepared.Result.Actual.Native[id]
	if !ok || !state.Ready || state.Metadata.Mode != "profiling" || state.Metadata.ProfileID != measurementProfileID || state.Metadata.Policy.FixedBatchSize != batch {
		t.Fatalf("prepare did not end on the ready profiling runtime: %+v", prepared.Result.Actual.Native)
	}
	return state
}

// Only a profile-less fixed-batch runtime in profiling mode is a measurement
// instance; a serving preparation still needs its measured profile.
func TestPreparationWithoutProfileNeedsMeasurementInstance(t *testing.T) {
	_, node, profile, _ := observerSourceFixture()
	measurement := measurementInstanceSpec(node, profile)
	if err := servingops.ValidatePreparation(measurement); err != nil {
		t.Fatal("measurement instance refused:", err)
	}
	for name, change := range map[string]func(*servingstate.PrepareSpec){
		"serving mode":         func(s *servingstate.PrepareSpec) { s.Config.Mode = servingstate.NativeServingMode },
		"no fixed batch":       func(s *servingstate.PrepareSpec) { s.Config.Policy.FixedBatchSize = 0 },
		"no published profile": func(s *servingstate.PrepareSpec) { s.Config.Policy.ProfileID = "" },
		"no Node":              func(s *servingstate.PrepareSpec) { s.Instance.Node = serving.NodeReference{} },
		"other memory":         func(s *servingstate.PrepareSpec) { s.MemoryBytes /= 2 },
		// Every initialized batch is warmed; only B belongs to this instance.
		"other batches": func(s *servingstate.PrepareSpec) { s.Config.BatchSizes, s.Config.MaxBatchSize = []int{1, 2}, 2 },
	} {
		spec := measurementInstanceSpec(node, profile)
		change(&spec)
		if err := servingops.ValidatePreparation(spec); err == nil {
			t.Fatalf("%s: preparation without profile accepted", name)
		}
	}
	servingSpec := measurementInstanceSpec(node, profile)
	servingSpec.Config.Mode, servingSpec.Config.ProfilePath = servingstate.NativeServingMode, "/profile/bundle.json"
	servingSpec.Config.Policy = native.Policy{ProfileID: profile.ID}
	servingSpec.Store = storekube.Binding{Store: "store", Endpoint: "http://store", Bucket: "bucket", Region: "region", CredentialsSecretName: "secret"}
	servingSpec.Bundle = evidencestore.ObjectIdentity{Store: "store", Key: "bundle", Size: 1}
	if err := servingops.ValidatePreparation(servingSpec); err == nil || !strings.Contains(err.Error(), "measured firstfloor preparation contract required") {
		t.Fatalf("serving preparation without profile: %v", err)
	}
}
