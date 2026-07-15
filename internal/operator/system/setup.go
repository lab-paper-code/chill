package system

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// SetupWithManager sets up event-driven and periodic status refreshes.
func (r *ChillSystemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate ChillSystem options: %w", err)
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("chillsystem").
		For(&edgev1alpha1.ChillSystem{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&appsv1.Deployment{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
