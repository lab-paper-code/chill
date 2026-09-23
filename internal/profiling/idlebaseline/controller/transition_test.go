package controller

import (
	"context"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestReconcilerWaitsForStateActuationBeforeFirstRun(t *testing.T) {
	parent := testBaselineParent()
	kube := baselineFakeClient(t, parent)
	preparer := &fakeRunPreparer{prepared: testActuatedPreparedRun(parent)}
	reconciler := testReconcilerWithPreparer(t, kube, preparer)
	reconciler.now = func() time.Time { return time.Unix(1_000, 0) }

	request := requestFor(parent)
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 0)
	actuation := &edgev1alpha1.NodeActuation{}
	if err := kube.Get(context.Background(), client.ObjectKey{
		Namespace: parent.Namespace, Name: stateActuationName(parent.Name),
	}, actuation); err != nil {
		t.Fatal(err)
	}
	owner := metav1.GetControllerOf(actuation)
	if owner == nil || owner.UID != parent.UID || actuation.Spec.Node.UID != "node-uid" ||
		actuation.Spec.PolicyName != "mode-policy" ||
		actuation.Spec.Actuator.Kind != edgev1alpha1.PowerModeActuatorJetsonNVPModel ||
		actuation.Spec.Actuator.JetsonNVPModel == nil ||
		actuation.Spec.Actuator.JetsonNVPModel.ID != 2 ||
		actuation.Spec.SettlingDurationSeconds != 30 ||
		actuation.Spec.MaxActuationDurationSeconds != 300 ||
		!actuation.Spec.Deadline.Time.Equal(actuation.Spec.StageRequestedAt.Add(300*time.Second)) ||
		preparer.calls != 1 {
		t.Fatalf("state actuation = %#v, prepare calls = %d", actuation, preparer.calls)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 0)
	if preparer.calls != 1 {
		t.Fatalf("pending state actuation caused %d preparations", preparer.calls)
	}

	markActuationReady(actuation)
	if err := kube.Update(context.Background(), actuation); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 1)
	if preparer.calls != 1 {
		t.Fatalf("stored state actuation caused %d preparations", preparer.calls)
	}
}

func TestReconcilerCreatesFixedRunDirectly(t *testing.T) {
	parent := testBaselineParent()
	kube := baselineFakeClient(t, parent)
	preparer := &fakeRunPreparer{}
	reconciler := testReconcilerWithPreparer(t, kube, preparer)
	if _, err := reconciler.Reconcile(context.Background(), requestFor(parent)); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 1)
	actuation := &edgev1alpha1.NodeActuation{}
	err := kube.Get(context.Background(), client.ObjectKey{
		Namespace: parent.Namespace, Name: stateActuationName(parent.Name),
	}, actuation)
	if !apierrors.IsNotFound(err) || preparer.calls != 1 {
		t.Fatalf("fixed state actuation err=%v prepare calls=%d", err, preparer.calls)
	}
}

func TestReconcilerClosesFailedStateActuationWithoutRun(t *testing.T) {
	parent := testBaselineParent()
	actuation := testStateActuation(t, parent)
	actuation.Status = edgev1alpha1.NodeActuationStatus{
		Phase: edgev1alpha1.NodeActuationPhaseFailed,
		TerminalFailure: &edgev1alpha1.NodeActuationFailure{
			Stage:  edgev1alpha1.NodeActuationFailureStageApply,
			Reason: "ApplyFailed", Message: "nvpmodel failed", OccurredAt: metav1.Now(),
		},
	}
	kube := baselineFakeClient(t, parent, actuation)
	preparer := &fakeRunPreparer{err: context.Canceled}
	reconciler := testReconcilerWithPreparer(t, kube, preparer)
	if _, err := reconciler.Reconcile(context.Background(), requestFor(parent)); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 0)
	updated := &edgev1alpha1.ModelLoadedIdleBaseline{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(parent), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient ||
		updated.Status.AttemptedRuns != 0 || updated.Status.CompletedRuns != 0 ||
		updated.Status.Result != nil || preparer.calls != 0 {
		t.Fatalf("failed state status=%#v prepare calls=%d", updated.Status, preparer.calls)
	}
}

func TestReconcilerUsesAlreadyExistingActuationWinner(t *testing.T) {
	parent := testBaselineParent()
	winner := testStateActuation(t, parent)
	winner.Spec.PolicyName = "stored-policy"
	winner.Spec.SettlingDurationSeconds = 10
	winner.Spec.MaxActuationDurationSeconds = 40
	markActuationReady(winner)
	base := baselineFakeClient(t, parent, winner)
	kube := &actuationCreateRaceClient{Client: base}
	preparer := &fakeRunPreparer{prepared: testActuatedPreparedRun(parent)}
	reconciler := testReconcilerWithPreparer(t, kube, preparer)

	if _, err := reconciler.Reconcile(context.Background(), requestFor(parent)); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 1)
	if preparer.calls != 1 {
		t.Fatalf("AlreadyExists race prepared %d times", preparer.calls)
	}
}

func TestProjectStateActuationRejectsWrongOwnerTargetOrReadyProof(t *testing.T) {
	parent := testBaselineParent()
	for name, mutate := range map[string]func(*edgev1alpha1.NodeActuation){
		"owner": func(actual *edgev1alpha1.NodeActuation) {
			actual.OwnerReferences[0].UID = "another-parent"
		},
		"target": func(actual *edgev1alpha1.NodeActuation) {
			actual.Spec.Node.Name = "another-node"
		},
		"generation": func(actual *edgev1alpha1.NodeActuation) {
			actual.Status.ObservedGeneration--
		},
		"read-back": func(actual *edgev1alpha1.NodeActuation) {
			actual.Status.TargetObservation.PowerMode = "another-mode"
		},
		"typed actuator": func(actual *edgev1alpha1.NodeActuation) {
			actual.Status.TargetObservation.Actuator.JetsonNVPModel.ID++
		},
	} {
		t.Run(name, func(t *testing.T) {
			actual := testStateActuation(t, parent)
			markActuationReady(actual)
			mutate(actual)
			if _, _, err := projectStateActuation(parent, actual); err == nil {
				t.Fatal("invalid state actuation was accepted")
			}
		})
	}
}

type actuationCreateRaceClient struct {
	client.Client
	hidden bool
}

func (c *actuationCreateRaceClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	object client.Object,
	options ...client.GetOption,
) error {
	if _, ok := object.(*edgev1alpha1.NodeActuation); ok && !c.hidden {
		c.hidden = true
		return apierrors.NewNotFound(schema.GroupResource{
			Group: edgev1alpha1.GroupVersion.Group, Resource: "nodeactuations",
		}, key.Name)
	}
	return c.Client.Get(ctx, key, object, options...)
}

func (c *actuationCreateRaceClient) Create(
	ctx context.Context,
	object client.Object,
	options ...client.CreateOption,
) error {
	if _, ok := object.(*edgev1alpha1.NodeActuation); ok {
		return apierrors.NewAlreadyExists(schema.GroupResource{
			Group: edgev1alpha1.GroupVersion.Group, Resource: "nodeactuations",
		}, object.GetName())
	}
	return c.Client.Create(ctx, object, options...)
}

func testReconcilerWithPreparer(
	t *testing.T,
	kube client.Client,
	preparer *fakeRunPreparer,
) *Reconciler {
	t.Helper()
	reconciler, err := NewReconciler(
		kube, preparer, &fakeBaselineDeriver{}, &fakeBaselineResultWriter{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func testActuatedPreparedRun(parent *edgev1alpha1.ModelLoadedIdleBaseline) profiler.PreparedRun {
	target := profilingrun.ActuatorTarget{
		Kind:           profilingrun.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 2},
	}
	policy := profiler.FrozenActuationPolicy{
		SchemaVersion: profiler.FrozenActuationPolicySchemaVersion,
		Source: profiler.ActuationPolicySource{
			Kind: profiler.ConfigMapActuationPolicySourceKind, Namespace: parent.Namespace,
			Name: "facts", UID: "facts-uid", ResourceVersion: "1", Key: "actuation-policies.yaml",
		},
		Policy: profiler.ActuationPolicy{
			SchemaVersion: profiler.ActuationPolicySchemaVersion,
			ActuationPolicySpec: profiler.ActuationPolicySpec{
				Name: "mode-policy", DeviceClass: parent.Spec.State.Candidate.DeviceClass,
				PowerMode:               parent.Spec.State.Candidate.PowerMode,
				ActuatorKind:            target.Kind,
				SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
			},
		},
	}
	return profiler.PreparedRun{
		RunConfig: profilingrun.RunConfig{
			State: profilingrun.ExecutionState{
				DeviceClass: parent.Spec.State.Candidate.DeviceClass,
				PowerMode:   parent.Spec.State.Candidate.PowerMode,
			},
			TargetNode: profilingrun.NodeSnapshot{
				Name: parent.Spec.State.TargetNode, UID: "node-uid",
			},
			Actuation: &profilingrun.ActuationContract{
				PowerMode: parent.Spec.State.Candidate.PowerMode,
				Actuator:  target, PolicyName: policy.Policy.Name,
			},
		},
		ActuationPolicy: &policy,
	}
}

func testStateActuation(
	t *testing.T,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
) *edgev1alpha1.NodeActuation {
	t.Helper()
	actuation, err := buildStateActuation(parent, testActuatedPreparedRun(parent), time.Unix(1_000, 0))
	if err != nil {
		t.Fatal(err)
	}
	actuation.Generation = 1
	return actuation
}

func markActuationReady(actuation *edgev1alpha1.NodeActuation) {
	readyAt := metav1.NewMicroTime(time.Unix(1_100, 0))
	actuation.Status = edgev1alpha1.NodeActuationStatus{
		ObservedGeneration: actuation.Generation,
		Phase:              edgev1alpha1.NodeActuationPhaseReady,
		TargetObservation: &edgev1alpha1.NodeActuationObservation{
			PowerMode:  actuation.Spec.PowerMode,
			Actuator:   *actuation.Spec.Actuator.DeepCopy(),
			ObservedAt: metav1.NewMicroTime(time.Unix(1_090, 0)),
		},
		ReadyAt: &readyAt,
	}
}

func requestFor(parent *edgev1alpha1.ModelLoadedIdleBaseline) ctrl.Request {
	return ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}
}

func assertRunCount(t *testing.T, kube client.Client, namespace string, want int) {
	t.Helper()
	runs := &edgev1alpha1.ProfilingRunList{}
	if err := kube.List(context.Background(), runs, client.InNamespace(namespace)); err != nil {
		t.Fatal(err)
	}
	if len(runs.Items) != want {
		t.Fatalf("ProfilingRun count = %d, want %d", len(runs.Items), want)
	}
}
