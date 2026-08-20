package kube_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

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
		raw.PreTransitionProcess.Process != raw.PostTransitionProcess.Process {
		t.Fatalf("process observations = pre:%+v post:%+v", raw.PreTransitionProcess, raw.PostTransitionProcess)
	}
	if len(raw.Readiness) != 2 || !raw.Readiness[0].Ready || !raw.Readiness[1].Ready ||
		!raw.Readiness[1].ObservedAt.After(raw.TargetReadBack.Fact.MappedAt) {
		t.Fatalf("readiness = %+v, target read-back = %+v", raw.Readiness, raw.TargetReadBack)
	}
	if raw.BoundaryMappingUncertainty != testRequest().BoundaryMappingUncertainty {
		t.Fatalf("boundary uncertainty = %v", raw.BoundaryMappingUncertainty)
	}
}

func TestNewRequiresBoundaryBoundToCoverPolling(t *testing.T) {
	request := testRequest()
	request.BoundaryMappingUncertainty = request.PollInterval - time.Nanosecond
	_, err := transitionkube.New(newClient(t), request)
	if err == nil || !strings.Contains(err.Error(), "cover the poll interval") {
		t.Fatalf("New() error = %v", err)
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
			created <- kubeClient.Create(
				context.Background(),
				readyPod("serving-new", "containerd://new", 1),
			)
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
		raw.PostTransitionProcess.Process.ContainerID != "containerd://new" {
		t.Fatalf("process observations = pre:%+v post:%+v", raw.PreTransitionProcess, raw.PostTransitionProcess)
	}

	startedAt := raw.TransitionStart.MappedAt
	completedAt := raw.Readiness[len(raw.Readiness)-1].ObservedAt
	window := completedAt.Sub(startedAt)
	raw.PowerSamples = []transitioncost.PowerSample{
		{ObservedAt: startedAt.Add(window / 4), Watts: 10},
		{ObservedAt: startedAt.Add(3 * window / 4), Watts: 10},
	}
	source := transitioncost.StateIdentity{DeviceType: "jetson_orin", Model: "mobilenet-v2-050", PowerMode: "MAXN"}
	target := transitioncost.StateIdentity{DeviceType: "jetson_orin", Model: "mobilenet-v2-050", PowerMode: "MODE_30W"}
	result, err := transitioncost.Derive(
		transitioncost.FrozenContract{
			SourceState: source,
			TargetState: target,
			TargetIdleBaseline: transitioncost.IdleBaseline{
				EvidenceReference: "baselines/mode-30w.json", State: target, MeanWallWatts: 5,
			},
		},
		raw,
		transitioncost.Policy{
			MaximumBoundaryDistance:      10 * time.Millisecond,
			MinimumCoverage:              0.49,
			MaximumSampleGap:             time.Second,
			MaximumBoundaryUncertainty:   10 * time.Millisecond,
			MaximumReadyObservationDelay: time.Second,
		},
	)
	if err != nil || result.Verdict() != transitioncost.VerdictAccepted {
		t.Fatalf("Derive() = %s/%s (%s), %v; start=%+v target=%+v",
			result.Verdict(), result.Reason(), result.Detail(), err, raw.TransitionStart, raw.TargetReadBack)
	}
	accepted, ok := result.AcceptedObservation()
	if !ok || accepted.ServingSurvived() {
		t.Fatalf("accepted observation = %+v, present = %t", accepted, ok)
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
		edgev1alpha1.NodeActuationFailureStageSafety,
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
	request := testRequest()
	request.ObservationTimeout = 8 * time.Millisecond
	observer := newObserver(t, kubeClient, request)
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
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Observe() error = %v, want deadline", err)
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
			if called || len(raw.Readiness) != 0 || raw.PreTransitionProcess != nil {
				t.Fatalf("callback/readiness/process = %t/%+v/%+v", called, raw.Readiness, raw.PreTransitionProcess)
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
	if raw.TargetReadBack == nil || raw.PostTransitionProcess != nil || len(raw.Readiness) != 1 {
		t.Fatalf("target/post/readiness = %+v/%+v/%+v",
			raw.TargetReadBack, raw.PostTransitionProcess, raw.Readiness)
	}
}

func TestObserverRetainsOneUnchangedReadinessFactPerPhase(t *testing.T) {
	kubeClient := newClient(t, service(), readyPod("serving", "containerd://same", 0))
	request := testRequest()
	request.ObservationTimeout = 8 * time.Millisecond
	observer := newObserver(t, kubeClient, request)

	raw, err := observer.Observe(context.Background(), func() {})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Observe() error = %v, want deadline", err)
	}
	if len(raw.Readiness) != 1 || !raw.Readiness[0].Ready {
		t.Fatalf("readiness = %+v, want one initial Ready=True fact", raw.Readiness)
	}
}

type transitionTimes struct {
	previous time.Time
	apply    time.Time
	target   time.Time
}

func actuationTimes() transitionTimes {
	base := time.Now().UTC().Add(-10 * time.Second)
	return transitionTimes{
		previous: base,
		apply:    base.Add(time.Second),
		target:   base.Add(3 * time.Second),
	}
}

func successfulActuation(times transitionTimes) *edgev1alpha1.NodeActuation {
	apply := metav1.NewMicroTime(times.apply)
	return &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{Namespace: "measurements", Name: "transition-1"},
		Status: edgev1alpha1.NodeActuationStatus{
			Phase: edgev1alpha1.NodeActuationPhaseVerifying,
			PreviousObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: "MAXN", ObservedAt: metav1.NewTime(times.previous),
			},
			ApplyStartedAt: &apply,
			TargetObservation: &edgev1alpha1.NodeActuationObservation{
				PowerMode: "MODE_30W", ObservedAt: metav1.NewTime(times.target),
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
			Namespace: "measurements", Name: name, UID: types.UID(name), Labels: map[string]string{"app": "serving"},
		},
		Spec: corev1.PodSpec{
			NodeName:   "jetsono",
			Containers: []corev1.Container{{Name: "model", Image: "example.invalid/model:fixture"}},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
			}},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "model", ContainerID: containerID, RestartCount: restartCount,
				State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: startedAt}},
			}},
		},
	}
}

func testRequest() transitionkube.Request {
	return transitionkube.Request{
		Namespace: "measurements", NodeActuationName: "transition-1", TargetNode: "jetsono",
		ServingService: "serving", ServingContainer: "model",
		DeviceType: "jetson_orin", Model: "mobilenet-v2-050",
		PollInterval: time.Millisecond, ObservationTimeout: 250 * time.Millisecond,
		BoundaryMappingUncertainty: 2 * time.Millisecond,
	}
}

func newObserver(t *testing.T, reader client.Reader, request transitionkube.Request) *transitionkube.Observer {
	t.Helper()
	observer, err := transitionkube.New(reader, request)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return observer
}

func newClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core/v1 scheme: %v", err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add CHILL scheme: %v", err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
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
