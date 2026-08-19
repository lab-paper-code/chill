package controller

import (
	"context"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// A recorded confirming transition is pinned by name and UID, and both a
// terminal NodeActuation and the recorded reference are immutable. Losing that
// object is therefore permanent, and retrying cannot turn it into a
// confirmation. Returning an error instead would reconcile without bound and
// stall the campaign that owns the Run.
func TestRecordedConfirmingTransitionLostIsPermanent(t *testing.T) {
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run-1"},
		Status: edgev1alpha1.ProfilingRunStatus{
			Actuation: &edgev1alpha1.ProfilingRunObjectReference{
				Name: "transition-1", UID: "transition-uid-1",
			},
		},
	}
	intent := profilingrun.Intent{
		TargetNode: profilingrun.NodeSnapshot{Name: "jetsono", UID: "node-uid-1"},
		Actuation:  &profilingrun.ActuationContract{PowerMode: "MAXN"},
	}

	for _, testCase := range []struct {
		name    string
		objects []*edgev1alpha1.NodeActuation
	}{
		{name: "deleted"},
		{
			name: "replaced by a different object",
			objects: []*edgev1alpha1.NodeActuation{{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: "chill-system", Name: "transition-1", UID: "transition-uid-2",
				},
			}},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(run)
			for _, object := range testCase.objects {
				builder = builder.WithObjects(object)
			}
			reconciler := &RunReconciler{Client: builder.Build()}

			transition, unconfirmed, err := reconciler.findConfirmingTransition(
				context.Background(), run, intent,
			)
			if err != nil {
				t.Fatalf("permanent loss returned a retryable error: %v", err)
			}
			if transition != nil {
				t.Fatal("a lost recorded transition was treated as a confirmation")
			}
			if unconfirmed == "" {
				t.Fatal("terminal outcome carries no reason for an operator")
			}
		})
	}
}
