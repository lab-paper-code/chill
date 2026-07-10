package system

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operator/watch"
)

// SetupWithManager sets up event-driven and periodic status refreshes.
func (r *ChillSystemReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate ChillSystem options: %w", err)
	}

	mapToSystem := watch.EnqueueChillSystems(r.Client, nil)
	return ctrl.NewControllerManagedBy(mgr).
		Named("chillsystem").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&appsv1.DaemonSet{}, mapToSystem).
		Complete(r)
}
