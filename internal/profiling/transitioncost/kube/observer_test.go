package kube_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	"k8s.io/apimachinery/pkg/api/resource"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	transitionkube "github.com/lab-paper-code/chill/internal/profiling/transitioncost/kube"
	transitionworker "github.com/lab-paper-code/chill/internal/profiling/transitioncost/worker"
)

var _ transitionworker.FactObserver = (*transitionkube.Observer)(nil)

func TestObserverCapturesOneInPlaceTransition(t *testing.T) {
	kubeClient := newClient(t, service(), readyPod("serving", "containerd://same", 0))
	observer := newObserver(t, kubeClient, testRequest())
	times := actuationTimes()
	callbackCount := 0

	raw, err := observer.Observe(context.Background(), func() {
		callbackCount++
		createActuation(t, kubeClient, successfulActuation(times))
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if callbackCount != 1 {
		t.Fatalf("ready callback count = %d, want 1", callbackCount)
	}
	wantPods := []workloadobserver.Pod{{
		Namespace: "measurements", Name: "serving", UID: "serving", Phase: "Running",
		Images: []string{"example.invalid/firstfloor:fixture"},
		Labels: map[string]string{profilingrun.MeasurementWorkloadLabelKey: profilingrun.ServingInstanceRole},
	}}
	if raw.BeforeNative == nil || !reflect.DeepEqual(raw.BeforeNative.Pods, wantPods) {
		t.Fatalf("native Pods = %+v", raw.BeforeNative)
	}
	if raw.SourceReadBack.State != (transitioncost.StateIdentity{
		DeviceType: "jetson_orin", Model: "mobilenet-v2-050", PowerMode: "MAXN",
	}) {
		t.Fatalf("source read-back = %+v", raw.SourceReadBack)
	}
	if raw.TransitionStart == nil || raw.TargetReadBack == nil ||
		raw.TargetReadBack.State.PowerMode != "MODE_30W" {
		t.Fatalf("transition facts = start:%+v target:%+v", raw.TransitionStart, raw.TargetReadBack)
	}
	if raw.PreTransitionProcess == nil || raw.PostTransitionProcess == nil ||
		raw.PreTransitionProcess.Process != *raw.PostTransitionProcess {
		t.Fatalf("process observations = pre:%+v post:%+v", raw.PreTransitionProcess, raw.PostTransitionProcess)
	}
	if !raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt.After(
		raw.MarkerBrackets.TargetObservation.FirstPresentReceivedAt,
	) {
		t.Fatal("ready process witness must follow target read-back")
	}
	for name, bracket := range map[string]transitioncost.ReceiptTimeBracket{
		"apply":         raw.MarkerBrackets.ApplyStartedAt,
		"target":        raw.MarkerBrackets.TargetObservation,
		"ready-process": raw.MarkerBrackets.ReadyProcessWitness,
	} {
		if bracket.LastAbsentReceivedAt.IsZero() ||
			!bracket.FirstPresentReceivedAt.After(bracket.LastAbsentReceivedAt) {
			t.Fatalf("%s marker bracket = %+v", name, bracket)
		}
	}
}

func TestObserverRequiresPreReadyMarkerAbsence(t *testing.T) {
	kubeClient := newClient(
		t,
		service(),
		readyPod("serving", "containerd://same", 0),
		successfulActuation(actuationTimes()),
	)
	observer := newObserver(t, kubeClient, testRequest())
	called := false

	_, err := observer.Observe(context.Background(), func() { called = true })
	if err == nil || !strings.Contains(err.Error(), "last-absent receipt time is missing") {
		t.Fatalf("Observe() error = %v", err)
	}
	if called {
		t.Fatal("ready callback ran without a pre-ready absent marker observation")
	}
}

func TestObserverRetainsRestartedServingProcess(t *testing.T) {
	oldPod := readyPod("serving-old", "containerd://old", 0)
	kubeClient := newClient(t, service(), oldPod)
	observer := newObserver(t, kubeClient, testRequest())
	times := actuationTimes()
	created := make(chan error, 1)

	raw, err := observer.Observe(context.Background(), func() {
		createActuation(t, kubeClient, successfulActuation(times))
		if err := kubeClient.Delete(context.Background(), oldPod.DeepCopy()); err != nil {
			t.Fatalf("delete old serving Pod: %v", err)
		}
		go func() {
			time.Sleep(4 * time.Millisecond)
			pod := readyPod("serving-new", "containerd://new", 1)
			created <- errors.Join(kubeClient.Create(context.Background(), executorService(pod)), kubeClient.Create(context.Background(), pod))
		}()
	})
	if err != nil {
		t.Fatalf("Observe() error = %v", err)
	}
	if createErr := <-created; createErr != nil {
		t.Fatalf("create returned serving Pod: %v", createErr)
	}
	if raw.PreTransitionProcess == nil || raw.PostTransitionProcess == nil ||
		raw.PreTransitionProcess.Process.ContainerID != "containerd://old" ||
		raw.PostTransitionProcess.ContainerID != "containerd://new" {
		t.Fatalf("process observations = pre:%+v post:%+v", raw.PreTransitionProcess, raw.PostTransitionProcess)
	}

}

func TestObserverMapsTerminalActuationStages(t *testing.T) {
	for _, test := range []struct {
		name      string
		stage     edgev1alpha1.NodeActuationFailureStage
		actuation bool
		readBack  bool
	}{
		{name: "apply", stage: edgev1alpha1.NodeActuationFailureStageApply, actuation: true},
		{name: "verify", stage: edgev1alpha1.NodeActuationFailureStageVerify, readBack: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			kubeClient := newClient(t, service(), readyPod("serving", "containerd://same", 0))
			observer := newObserver(t, kubeClient, testRequest())
			times := actuationTimes()
			failed := successfulActuation(times)
			failed.Status.Phase = edgev1alpha1.NodeActuationPhaseFailed
			failed.Status.TargetObservation = nil
			failed.Status.TerminalFailure = &edgev1alpha1.NodeActuationFailure{
				Stage: test.stage, Reason: "MeasuredFailure", Message: "selected operation failed",
				OccurredAt: metav1.NewTime(times.target),
			}

			raw, err := observer.Observe(context.Background(), func() {
				createActuation(t, kubeClient, failed)
			})
			if err != nil {
				t.Fatalf("Observe() error = %v", err)
			}
			if (raw.ActuationFailure != nil) != test.actuation ||
				(raw.TargetReadBackFailure != nil) != test.readBack {
				t.Fatalf("mapped failures = actuation:%+v read-back:%+v", raw.ActuationFailure, raw.TargetReadBackFailure)
			}
			if raw.ObservationEndedAt.IsZero() || !raw.ObservationEndedAt.After(raw.ObservationStartedAt) {
				t.Fatalf("coverage = %v through %v", raw.ObservationStartedAt, raw.ObservationEndedAt)
			}
		})
	}
}

func TestObserverDoesNotGeneralizePreApplyFailures(t *testing.T) {
	for _, stage := range []edgev1alpha1.NodeActuationFailureStage{
		edgev1alpha1.NodeActuationFailureStageLease,
		edgev1alpha1.NodeActuationFailureStagePolicy,
		edgev1alpha1.NodeActuationFailureStageObservePrevious,
	} {
		t.Run(string(stage), func(t *testing.T) {
			kubeClient := newClient(t, service(), readyPod("serving", "containerd://same", 0))
			observer := newObserver(t, kubeClient, testRequest())
			failed := successfulActuation(actuationTimes())
			failed.Status.Phase = edgev1alpha1.NodeActuationPhaseFailed
			failed.Status.ApplyStartedAt = nil
			failed.Status.TargetObservation = nil
			failed.Status.TerminalFailure = &edgev1alpha1.NodeActuationFailure{
				Stage: stage, Reason: "PreApplyFailure", Message: "pre-apply operation failed",
				OccurredAt: metav1.Now(),
			}

			raw, err := observer.Observe(context.Background(), func() {
				createActuation(t, kubeClient, failed)
			})
			if err == nil || !strings.Contains(err.Error(), "unsupported NodeActuation terminal failure stage") {
				t.Fatalf("Observe() error = %v", err)
			}
			if raw.ActuationFailure != nil || raw.TargetReadBackFailure != nil {
				t.Fatalf("scientific failure slots = %+v/%+v", raw.ActuationFailure, raw.TargetReadBackFailure)
			}
		})
	}
}

func TestObserverDoesNotUseSettlingFailureAsCompletion(t *testing.T) {
	pod := readyPod("serving", "containerd://same", 0)
	kubeClient := newClient(t, service(), pod)
	ready, postReadBackProbe := false, false
	reader := interceptor.NewClient(kubeClient, interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if ready {
				// Expire the post-read-back readiness probe, not the native bootstrap.
				postReadBackProbe = true
				return context.DeadlineExceeded
			}
			return c.List(ctx, list, opts...)
		},
	})
	observer := newObserver(t, reader, testRequest())
	times := actuationTimes()
	failed := successfulActuation(times)
	failed.Status.Phase = edgev1alpha1.NodeActuationPhaseFailed
	failed.Status.TerminalFailure = &edgev1alpha1.NodeActuationFailure{
		Stage: edgev1alpha1.NodeActuationFailureStageSettle, Reason: "DeadlineExceeded",
		Message: "operational settling deadline elapsed", OccurredAt: metav1.NewTime(times.target),
	}

	raw, err := observer.Observe(context.Background(), func() {
		createActuation(t, kubeClient, failed)
		if err := kubeClient.Delete(context.Background(), pod.DeepCopy()); err != nil {
			t.Fatalf("delete serving Pod: %v", err)
		}
		ready = true
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Observe() error = %v, want deadline", err)
	}
	if !postReadBackProbe {
		t.Fatal("observer did not probe serving readiness after retaining the settling failure")
	}
	if raw.TargetReadBack == nil || raw.PostTransitionProcess != nil ||
		raw.ActuationFailure != nil || raw.TargetReadBackFailure != nil {
		t.Fatalf("settle result = target:%+v post:%+v failures:%+v/%+v",
			raw.TargetReadBack, raw.PostTransitionProcess, raw.ActuationFailure, raw.TargetReadBackFailure)
	}
}

func TestObserverDoesNotInferReadinessFromMissingOrAmbiguousPods(t *testing.T) {
	for _, test := range []struct {
		name string
		pods []client.Object
	}{
		{name: "missing"},
		{name: "ambiguous", pods: []client.Object{
			readyPod("serving-a", "containerd://a", 0),
			readyPod("serving-b", "containerd://b", 0),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects := []client.Object{service()}
			objects = append(objects, test.pods...)
			request := testRequest()
			request.ObservationTimeout = 8 * time.Millisecond
			observer := newObserver(t, newClient(t, objects...), request)
			called := false

			raw, err := observer.Observe(context.Background(), func() { called = true })
			if err == nil || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Observe() error = %v, want deadline", err)
			}
			if called || raw.PostTransitionProcess != nil || raw.PreTransitionProcess != nil {
				t.Fatalf("callback/post/pre = %t/%+v/%+v", called, raw.PostTransitionProcess, raw.PreTransitionProcess)
			}
		})
	}
}

func TestObserverRequiresReadyPodAndRunningContainer(t *testing.T) {
	for _, test := range []struct {
		name  string
		alter func(*corev1.Pod)
	}{
		{"not ready", func(pod *corev1.Pod) { pod.Status.Conditions[0].Status = corev1.ConditionFalse }},
		{"unknown readiness", func(pod *corev1.Pod) { pod.Status.Conditions[0].Status = corev1.ConditionUnknown }},
		{"missing readiness", func(pod *corev1.Pod) { pod.Status.Conditions = nil }},
		{"not running", func(pod *corev1.Pod) { pod.Status.ContainerStatuses[0].State.Running = nil }},
		{"missing identity", func(pod *corev1.Pod) { pod.Status.ContainerStatuses[0].ContainerID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			pod := readyPod("serving", "containerd://same", 0)
			test.alter(pod)
			request := testRequest()
			request.ObservationTimeout = 8 * time.Millisecond
			observer := newObserver(t, newClient(t, service(), pod), request)
			called := false
			raw, err := observer.Observe(context.Background(), func() { called = true })
			if !errors.Is(err, context.DeadlineExceeded) || called || raw.PreTransitionProcess != nil || raw.PostTransitionProcess != nil {
				t.Fatalf("unready workload became a process witness: callback=%t raw=%+v err=%v", called, raw, err)
			}
		})
	}
}

func TestObserverDoesNotCloseOnAmbiguousPostReadBackPods(t *testing.T) {
	kubeClient := newClient(t, service(), readyPod("serving-a", "containerd://a", 0))
	request := testRequest()
	request.ObservationTimeout = 8 * time.Millisecond
	observer := newObserver(t, kubeClient, request)

	raw, err := observer.Observe(context.Background(), func() {
		createActuation(t, kubeClient, successfulActuation(actuationTimes()))
		if err := kubeClient.Create(
			context.Background(), readyPod("serving-b", "containerd://b", 0),
		); err != nil {
			t.Fatalf("create ambiguous serving Pod: %v", err)
		}
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Observe() error = %v, want deadline", err)
	}
	if raw.TargetReadBack == nil || raw.PostTransitionProcess != nil || !raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt.IsZero() {
		t.Fatalf("target/post/bracket = %+v/%+v/%+v",
			raw.TargetReadBack, raw.PostTransitionProcess, raw.MarkerBrackets.ReadyProcessWitness)
	}
}

func TestObserverDoesNotCompleteFromPreTransitionReady(t *testing.T) {
	kubeClient := newClient(t, service(), readyPod("serving", "containerd://same", 0))
	request := testRequest()
	request.ObservationTimeout = 8 * time.Millisecond
	observer := newObserver(t, kubeClient, request)

	raw, err := observer.Observe(context.Background(), func() {})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Observe() error = %v, want deadline", err)
	}
	if raw.PreTransitionProcess == nil || raw.PostTransitionProcess != nil ||
		!raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt.IsZero() {
		t.Fatalf("pre-transition Ready incorrectly completed measurement: %+v", raw)
	}
}

type transitionTimes struct {
	previous time.Time
	apply    time.Time
	target   time.Time
}

func actuationTimes() transitionTimes {
	base := time.Now().UTC().Truncate(time.Second).Add(-10 * time.Second)
	return transitionTimes{
		previous: base.Add(100 * time.Millisecond),
		apply:    base.Add(200 * time.Millisecond),
		target:   base.Add(300 * time.Millisecond),
	}
}

func successfulActuation(times transitionTimes) *edgev1alpha1.NodeActuation {
	apply := metav1.NewMicroTime(times.apply)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measurements", Name: "transition-1"},
		Status: edgev1alpha1.NodeActuationStatus{
			Phase: edgev1alpha1.NodeActuationPhaseVerifying,
			PreviousObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: "MAXN", ObservedAt: metav1.NewMicroTime(times.previous),
			},
			ApplyStartedAt: &apply,
			TargetObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: "MODE_30W", ObservedAt: metav1.NewMicroTime(times.target),
			},
		},
	}
}

func service() *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measurements", Name: "serving"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "serving"}},
	}
}

func readyPod(name, containerID string, restartCount int32) *corev1.Pod {
	startedAt := metav1.NewTime(time.Now().UTC().Add(-time.Minute))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "measurements", Name: name, UID: types.UID(name), Labels: map[string]string{"app": "serving", profilingrun.MeasurementWorkloadLabelKey: profilingrun.ServingInstanceRole},
		},
		Spec: corev1.PodSpec{
			NodeName:   "jetsono",
			Containers: []corev1.Container{{Name: "firstfloor", Image: "example.invalid/firstfloor:fixture", Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("128Mi")}, Limits: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("128Mi")}}}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			PodIP: "127.0.0.1",
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "firstfloor", ContainerID: containerID, RestartCount: restartCount, Ready: true, ImageID: "containerd://fixture-image",
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: startedAt}},
			}},
		},
	}
}

func testRequest() transitionkube.Request {
	return transitionkube.Request{
		Namespace: "measurements", NodeActuationName: "transition-1", TargetNode: "jetsono", TargetNodeUID: "node-uid",
		ServingService: "serving", ServingContainer: "firstfloor",
		DeviceType: "jetson_orin", Model: "mobilenet-v2-050",
		PollInterval: time.Millisecond, ObservationTimeout: 250 * time.Millisecond,
	}
}

func newObserver(t *testing.T, reader client.Reader, request transitionkube.Request) *transitionkube.Observer {
	t.Helper()
	observer, err := transitionkube.New(reader, request)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/models/chill_model/state" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(native.State{At: time.Now().UTC(), Ready: true, Metadata: native.Metadata{Mode: "serving", ProfileID: "fixture-profile", Runtime: native.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "fixture", ModelFiles: []string{"/model/model.onnx"}, CPUAffinity: []int{0}, IntraOpThreads: 1}}})
	}))
	t.Cleanup(server.Close)
	endpoint, _ := url.Parse(server.URL)
	observer.HTTPClient = &http.Client{Transport: nativeHTTPTransport{endpoint: endpoint}}
	return observer
}

func newClient(t *testing.T, objects ...client.Object) client.WithWatch {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core/v1 scheme: %v", err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add CHILL scheme: %v", err)
	}
	// Every runner Pod gets its chill-executor-<Pod UID> Service, the only dial target.
	for _, object := range objects {
		if pod, ok := object.(*corev1.Pod); ok {
			objects = append(objects, executorService(pod))
		}
	}
	objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "jetsono", UID: "node-uid"}})
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func executorService(pod *corev1.Pod) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: "chill-executor-" + string(pod.UID)},
		Spec: corev1.ServiceSpec{ClusterIP: "10.96.0.10", Selector: map[string]string{"edge.dacs.io/serving-pod-uid": string(pod.UID)},
			Ports: []corev1.ServicePort{{Name: "http", Protocol: corev1.ProtocolTCP, Port: 8080}}},
	}
}

func createActuation(t *testing.T, kubeClient client.Client, actuation *edgev1alpha1.NodeActuation) {
	t.Helper()
	if err := kubeClient.Create(context.Background(), actuation); err != nil {
		t.Fatalf("create NodeActuation: %v", err)
	}
	current := &edgev1alpha1.NodeActuation{}
	if err := kubeClient.Get(context.Background(), client.ObjectKeyFromObject(actuation), current); err != nil {
		t.Fatalf("get created NodeActuation: %v", err)
	}
	if current.Status.Phase == "" {
		t.Fatalf("fake client did not retain NodeActuation status")
	}
}

type nativeHTTPTransport struct{ endpoint *url.URL }

func (t nativeHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Host != "10.96.0.10:8080" {
		return nil, fmt.Errorf("dialed %s, not the runner Service", request.URL.Host)
	}
	copy := request.Clone(request.Context())
	copy.URL.Scheme = t.endpoint.Scheme
	copy.URL.Host = t.endpoint.Host
	return http.DefaultTransport.RoundTrip(copy)
}
