package nodeactuator

import (
	"context"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSafeToStopBlocksEveryUnfinishedOrLeaseHoldingHandoff(t *testing.T) {
	t.Parallel()
	now := metav1.NewMicroTime(time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	for name, status := range map[string]edgev1alpha1.NodeActuationStatus{
		"pending": {Phase: edgev1alpha1.NodeActuationPhasePending},
		"applying": {
			Phase: edgev1alpha1.NodeActuationPhaseApplying, ApplyStartedAt: &now,
		},
		"ready before Lease release": {
			Phase: edgev1alpha1.NodeActuationPhaseReady,
			Lease: &edgev1alpha1.ProfilingRunObjectReference{Name: "lease", UID: "lease-uid"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if safe := safeToStopWithStatus(t, status); safe {
				t.Fatalf("status unexpectedly safe: %#v", status)
			}
		})
	}
}

func TestSafeToStopAllowsTerminalTransitionsAfterLeaseRelease(t *testing.T) {
	t.Parallel()
	now := metav1.NewMicroTime(time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC))
	for name, status := range map[string]edgev1alpha1.NodeActuationStatus{
		"failed": {Phase: edgev1alpha1.NodeActuationPhaseFailed},
		"ready":  {Phase: edgev1alpha1.NodeActuationPhaseReady, ReadyAt: &now},
	} {
		t.Run(name, func(t *testing.T) {
			if safe := safeToStopWithStatus(t, status); !safe {
				t.Fatalf("status unexpectedly unsafe: %#v", status)
			}
		})
	}
}

func safeToStopWithStatus(t *testing.T, status edgev1alpha1.NodeActuationStatus) bool {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	handoff := &edgev1alpha1.NodeActuation{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "handoff"}, Status: status,
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(handoff).Build()
	safe, err := SafeToStop(context.Background(), reader, "chill-system")
	if err != nil {
		t.Fatal(err)
	}
	return safe
}
