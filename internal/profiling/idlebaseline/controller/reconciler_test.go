package controller

import (
	"context"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeBaselineDeriver struct {
	baseline   profilederivation.IdleBaseline
	references idlebaseline.FinalEvidence
	calls      int
	runs       []edgev1alpha1.ProfilingRun
}

type fakeRunPreparer struct {
	prepared profiler.PreparedRun
	err      error
	calls    int
}

func (p *fakeRunPreparer) Prepare(
	context.Context,
	profiler.PreparationRequest,
) (profiler.PreparedRun, error) {
	p.calls++
	return p.prepared, p.err
}

func (d *fakeBaselineDeriver) Derive(
	_ context.Context,
	runs []edgev1alpha1.ProfilingRun,
	_ profilederivation.PointPolicy,
) (profilederivation.IdleBaseline, idlebaseline.FinalEvidence, error) {
	d.calls++
	d.runs = append([]edgev1alpha1.ProfilingRun(nil), runs...)
	references := make(idlebaseline.FinalEvidence, len(d.references))
	for trialID, reference := range d.references {
		references[trialID] = reference
	}
	return d.baseline, references, nil
}

type fakeBaselineResultWriter struct {
	reference edgev1alpha1.ProfilingRunEvidenceReference
	found     bool
	loads     int
	writes    int
}

func (w *fakeBaselineResultWriter) Load(
	context.Context,
	*edgev1alpha1.ModelLoadedIdleBaseline,
) (edgev1alpha1.ProfilingRunEvidenceReference, bool, error) {
	w.loads++
	return w.reference, w.found, nil
}

func (w *fakeBaselineResultWriter) Write(
	context.Context,
	*edgev1alpha1.ModelLoadedIdleBaseline,
	idlebaseline.Result,
) (edgev1alpha1.ProfilingRunEvidenceReference, error) {
	w.writes++
	return w.reference, nil
}

func TestReconcilerCreatesOneIdleRunAtATimeAndCompletes(t *testing.T) {
	parent := testBaselineParent()
	runs := []client.Object{parent}
	for attempt := int32(1); attempt <= 2; attempt++ {
		run := acceptedRun(t, parent, attempt)
		runs = append(runs, &run)
	}
	kube := baselineFakeClient(t, runs...)
	deriver := &fakeBaselineDeriver{baseline: testDerivedBaseline(t, 3), references: testFinalReferences(3)}
	writer := &fakeBaselineResultWriter{reference: testResultReference()}
	reconciler := newTestReconciler(t, kube, deriver, writer)
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}

	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	third := &edgev1alpha1.ProfilingRun{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: parent.Namespace, Name: runName(parent.Name, 3)}, third); err != nil {
		t.Fatal(err)
	}
	list := &edgev1alpha1.ProfilingRunList{}
	if err := kube.List(context.Background(), list, client.InNamespace(parent.Namespace)); err != nil || len(list.Items) != 3 {
		t.Fatalf("owned Runs = %d, err = %v", len(list.Items), err)
	}

	third.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: "evidence", Key: "runs/2/final.json", SchemaVersion: "final.v1", Size: 20,
	}
	if err := kube.Update(context.Background(), third); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	updated := &edgev1alpha1.ModelLoadedIdleBaseline{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(parent), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != edgev1alpha1.ModelLoadedIdleBaselinePhaseComplete ||
		updated.Status.CompletedRuns != 3 || updated.Status.AttemptedRuns != 3 ||
		updated.Status.Result == nil || *updated.Status.Result != testResultReference() ||
		deriver.calls != 1 || len(deriver.runs) != 3 || writer.loads != 1 || writer.writes != 1 {
		t.Fatalf("completion drifted: status=%#v derive=%d writer=%d/%d", updated.Status, deriver.calls, writer.loads, writer.writes)
	}
}

func TestReconcilerClosesInsufficientWithoutResult(t *testing.T) {
	parent := testBaselineParent()
	parent.Spec.AttemptLimit = 3
	objects := []client.Object{parent}
	for attempt := int32(1); attempt <= 3; attempt++ {
		run, err := buildRun(parent, attempt)
		if err != nil {
			t.Fatal(err)
		}
		run.Status.Conditions = []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed"}}
		objects = append(objects, run)
	}
	kube := baselineFakeClient(t, objects...)
	deriver := &fakeBaselineDeriver{}
	writer := &fakeBaselineResultWriter{}
	reconciler := newTestReconciler(t, kube, deriver, writer)
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}); err != nil {
		t.Fatal(err)
	}
	updated := &edgev1alpha1.ModelLoadedIdleBaseline{}
	if err := kube.Get(context.Background(), client.ObjectKeyFromObject(parent), updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.Phase != edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient ||
		updated.Status.Result != nil || updated.Status.AttemptedRuns != 3 || updated.Status.CompletedRuns != 0 ||
		deriver.calls != 0 || writer.loads != 0 || writer.writes != 0 {
		t.Fatalf("Insufficient status fabricated a result: %#v", updated.Status)
	}
}

func TestReconcilerRecoversExistingCompleteWinnerWithoutRederiving(t *testing.T) {
	parent := testBaselineParent()
	objects := []client.Object{parent}
	for attempt := int32(1); attempt <= 3; attempt++ {
		run := acceptedRun(t, parent, attempt)
		objects = append(objects, &run)
	}
	kube := baselineFakeClient(t, objects...)
	deriver := &fakeBaselineDeriver{}
	writer := &fakeBaselineResultWriter{reference: testResultReference(), found: true}
	reconciler := newTestReconciler(t, kube, deriver, writer)
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}); err != nil {
		t.Fatal(err)
	}
	if deriver.calls != 0 || writer.loads != 1 || writer.writes != 0 {
		t.Fatalf("winner recovery recomputed result: derive=%d writer=%d/%d", deriver.calls, writer.loads, writer.writes)
	}
}

func TestPointPolicyRejectsNonFiniteOrOutOfRangeConfidence(t *testing.T) {
	spec := testBaselineParent().Spec.PointPolicy
	for _, invalid := range []string{"NaN", "Inf", "0", "1", " 0.95"} {
		spec.ConfidenceLevel = invalid
		if _, err := pointPolicy(spec); err == nil {
			t.Fatalf("pointPolicy accepted confidence %q", invalid)
		}
	}
}

func TestUIDSelectorExcludesChildrenFromRecreatedSameNameParent(t *testing.T) {
	parent := testBaselineParent()
	oldParent := parent.DeepCopy()
	oldParent.UID = types.UID("old-parent-uid")
	oldRun, err := buildRun(oldParent, 1)
	if err != nil {
		t.Fatal(err)
	}
	kube := baselineFakeClient(t, parent, oldRun)
	reconciler := newTestReconciler(t, kube, &fakeBaselineDeriver{}, &fakeBaselineResultWriter{})
	runs, err := reconciler.listRuns(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 0 {
		t.Fatalf("new parent selected %d old-UID children", len(runs))
	}
	if err := reconciler.ensureRun(context.Background(), parent, 1); err == nil {
		t.Fatal("same-name Run collision accepted an old parent's child")
	}
}

func TestRunTerminalIgnoresTerminalReasonOnUnrelatedCondition(t *testing.T) {
	if runTerminal([]metav1.Condition{{
		Type: "Unrelated", Status: metav1.ConditionFalse, Reason: "JobFailed",
	}}) {
		t.Fatal("unrelated condition terminated an idle baseline attempt")
	}
	if !runTerminal([]metav1.Condition{{
		Type: "ExecutionComplete", Status: metav1.ConditionFalse, Reason: "JobFailed",
	}}) {
		t.Fatal("owned execution terminal condition was ignored")
	}
}

func TestReconcilerWaitsForAdmission(t *testing.T) {
	for _, test := range []struct {
		name, admission string
		requeue         time.Duration
		phase           string
	}{
		{"not admitted", "", 5 * time.Second, ""},
		{"refused", profilingrun.RefusedAdmission("NodeServing", "serving/a"), 0, edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := testBaselineParent()
			parent.Annotations = map[string]string{profilingrun.AdmissionAnnotation: test.admission}
			kube := baselineFakeClient(t, parent)
			reconciler := newTestReconciler(t, kube, &fakeBaselineDeriver{}, &fakeBaselineResultWriter{})
			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)})
			if err != nil || result.RequeueAfter != test.requeue {
				t.Fatalf("result = %#v, err = %v", result, err)
			}
			updated := &edgev1alpha1.ModelLoadedIdleBaseline{}
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(parent), updated); err != nil {
				t.Fatal(err)
			}
			actuations := &edgev1alpha1.NodeActuationList{}
			if err := kube.List(context.Background(), actuations); err != nil {
				t.Fatal(err)
			}
			assertRunCount(t, kube, parent.Namespace, 0)
			if updated.Status.Phase != test.phase || len(actuations.Items) != 0 {
				t.Fatalf("phase = %q, NodeActuations = %d", updated.Status.Phase, len(actuations.Items))
			}
		})
	}
}

func baselineFakeClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&edgev1alpha1.ModelLoadedIdleBaseline{}).
		WithObjects(objects...).Build()
}

func newTestReconciler(
	t *testing.T,
	kube client.Client,
	deriver BaselineDeriver,
	writer BaselineResultWriter,
) *Reconciler {
	t.Helper()
	reconciler, err := NewReconciler(kube, &fakeRunPreparer{}, deriver, writer)
	if err != nil {
		t.Fatal(err)
	}
	return reconciler
}

func testResultReference() edgev1alpha1.ProfilingRunEvidenceReference {
	return edgev1alpha1.ProfilingRunEvidenceReference{
		Store: "evidence", Key: "model-loaded-idle-baselines/uid/result.json",
		SchemaVersion: idlebaseline.ResultSchemaVersion, Size: 100,
	}
}
