package nodediscovery

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operator/watch"
)

func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate node-discovery operator options: %w", err)
	}

	mapToSystem := watch.EnqueueChillSystems(r.Client, nil)
	return ctrl.NewControllerManagedBy(mgr).
		Named("node-discovery").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&corev1.ConfigMap{}, mapToSystem).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}
