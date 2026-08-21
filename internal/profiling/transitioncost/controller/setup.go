package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
)

// SetupOptions contains the existing installation adapters and operational
// bounds needed to register the complete transition-cost controller.
type SetupOptions struct {
	Nodes             profiler.NodeResolver
	Metrics           profiler.MetricSourceResolver
	ActuationPolicies profiler.ActuationPolicyResolver
	StoreBindings     AttemptStoreBindingResolver
	Evidence          interface {
		evidencestore.Reader
		evidencestore.LocationReader
		evidencestore.ImmutableWriter
	}
	EvidenceStore      string
	ObserverNodeName   string
	AttemptJob         AttemptJobOptions
	MaximumObjectBytes int64
}

// SetupWithManager constructs and registers the complete transition-cost
// lifecycle without exposing its package-local sequence or reconciler.
func SetupWithManager(manager ctrl.Manager, options SetupOptions) error {
	if manager == nil {
		return errors.New("controller manager is required")
	}
	baselines, err := NewIdleBaselineSource(
		manager.GetAPIReader(), options.Evidence, options.MaximumObjectBytes,
	)
	if err != nil {
		return fmt.Errorf("construct transition idle-baseline source: %w", err)
	}
	preparer, err := NewPreparer(
		manager.GetAPIReader(), options.Nodes, options.Metrics, baselines,
		options.ActuationPolicies, options.ObserverNodeName, options.EvidenceStore,
	)
	if err != nil {
		return fmt.Errorf("construct transition preparer: %w", err)
	}
	preparations, err := NewPreparationRecords(manager.GetClient(), manager.GetScheme(), preparer)
	if err != nil {
		return fmt.Errorf("construct transition preparation records: %w", err)
	}
	materializer, err := NewAttemptMaterializer(
		manager.GetClient(), manager.GetScheme(), options.StoreBindings, options.AttemptJob,
	)
	if err != nil {
		return fmt.Errorf("construct transition attempt materializer: %w", err)
	}
	finalizer, err := NewAttemptFinalizer(
		manager.GetAPIReader(), options.Evidence, options.MaximumObjectBytes,
	)
	if err != nil {
		return fmt.Errorf("construct transition attempt finalizer: %w", err)
	}
	sequence, err := newMeasurementSequence(preparations, materializer, finalizer)
	if err != nil {
		return fmt.Errorf("construct transition measurement sequence: %w", err)
	}
	results, err := newMeasurementResultWriter(options.Evidence, options.MaximumObjectBytes)
	if err != nil {
		return fmt.Errorf("construct transition result writer: %w", err)
	}
	reconciler, err := newPowerModeTransitionCostReconciler(manager.GetClient(), sequence, results)
	if err != nil {
		return fmt.Errorf("construct PowerModeTransitionCost reconciler: %w", err)
	}
	if err := reconciler.setupWithManager(manager); err != nil {
		return fmt.Errorf("set up PowerModeTransitionCost reconciler: %w", err)
	}
	return nil
}

// setupWithManager registers the enclosing measurement and its owned
// Kubernetes records. Pod receipt reads do not require a Pod watch.
func (r *powerModeTransitionCostReconciler) setupWithManager(manager ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(manager).
		Named("powermodetransitioncost").
		For(&edgev1alpha1.PowerModeTransitionCost{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&batchv1.Job{}).
		Owns(&edgev1alpha1.NodeActuation{}).
		Complete(r)
}
