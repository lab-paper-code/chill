package controller

import (
	"context"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var testNow = time.Unix(10_000, 0)

// grantedNode is the baseline's target Node with its measurement taint naming
// holder; an empty holder leaves it untainted.
func grantedNode(parent *edgev1alpha1.ModelLoadedIdleBaseline, holder string) *corev1.Node {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: parent.Spec.State.TargetNode, UID: "node-uid"}}
	if holder != "" {
		node.Spec.Taints = []corev1.Taint{{
			Key: profilingrun.MeasurementTaintKey, Value: holder, Effect: corev1.TaintEffectNoSchedule,
		}}
	}
	return node
}

// setAgent records the agent at state for the baseline mode, fenced at
// applyStartedAt.
func setAgent(node *corev1.Node, mode, state string, applyStartedAt time.Time) {
	record := metadata.PowerModeTransitionRecord{
		PowerMode: mode, RequestedAt: applyStartedAt.Format(time.RFC3339Nano), Generation: 1, ApplyStartedAt: &applyStartedAt,
	}
	if state == metadata.PowerModeSuccess {
		readyAt := applyStartedAt.Add(time.Minute)
		record.VerifiedAt, record.ReadyAt = &applyStartedAt, &readyAt
	}
	metadata.SetPowerModeTransition(node, state, 1, record)
}

func getNode(t *testing.T, kube client.Client, name string) *corev1.Node {
	t.Helper()
	node := &corev1.Node{}
	if err := kube.Get(context.Background(), client.ObjectKey{Name: name}, node); err != nil {
		t.Fatal(err)
	}
	return node
}

func TestReconcilerRequestsPowerModeBeforeFirstRun(t *testing.T) {
	parent := testBaselineParent()
	kube := baselineFakeClient(t, parent, grantedNode(parent, string(parent.UID)))
	reconciler := testReconcilerWithPreparer(t, kube, &fakeRunPreparer{prepared: testActuatedPreparedRun(parent)})

	result, err := reconciler.Reconcile(context.Background(), requestFor(parent))
	if err != nil || result.RequeueAfter != admissionWait {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	assertRunCount(t, kube, parent.Namespace, 0)
	node := getNode(t, kube, parent.Spec.State.TargetNode)
	if node.Labels[metadata.PowerMode] != parent.Spec.State.Candidate.PowerMode {
		t.Fatalf("power-mode label = %q", node.Labels[metadata.PowerMode])
	}

	setAgent(node, parent.Spec.State.Candidate.PowerMode, metadata.PowerModePending, testNow.Add(-time.Minute))
	if err := kube.Update(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(parent)); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 0)

	node = getNode(t, kube, parent.Spec.State.TargetNode)
	setAgent(node, parent.Spec.State.Candidate.PowerMode, metadata.PowerModeSuccess, testNow.Add(-2*time.Minute))
	if err := kube.Update(context.Background(), node); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), requestFor(parent)); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 1)
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
	if preparer.calls != 1 {
		t.Fatalf("fixed state prepare calls=%d", preparer.calls)
	}
}

// A failed apply closes the baseline without a Run.
func TestReconcilerClosesFailedPowerModeWithoutRun(t *testing.T) {
	parent := testBaselineParent()
	node := grantedNode(parent, string(parent.UID))
	setAgent(node, parent.Spec.State.Candidate.PowerMode, metadata.PowerModeFailed, testNow.Add(-time.Minute))
	kube := baselineFakeClient(t, parent, node)
	reconciler := testReconcilerWithPreparer(t, kube, &fakeRunPreparer{prepared: testActuatedPreparedRun(parent)})
	if _, err := reconciler.Reconcile(context.Background(), requestFor(parent)); err != nil {
		t.Fatal(err)
	}
	assertRunCount(t, kube, parent.Namespace, 0)
	updated := &edgev1alpha1.ModelLoadedIdleBaseline{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(parent), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient ||
		updated.Status.AttemptedRuns != 0 || updated.Status.Result != nil {
		t.Fatalf("failed state status=%#v", updated.Status)
	}
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
	reconciler.now = func() time.Time { return testNow }
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
