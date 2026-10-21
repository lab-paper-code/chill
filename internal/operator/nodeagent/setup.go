package nodeagent

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operator/watch"
)

func (r *Reconciler) SetupWithManager(manager ctrl.Manager) error {
	if err := r.Options.DefaultAndValidate(); err != nil {
		return fmt.Errorf("validate node agent operator options: %w", err)
	}
	mapToSystem := watch.EnqueueChillSystems(r.Client, nil)
	return ctrl.NewControllerManagedBy(manager).
		Named("node-agent").
		For(&edgev1alpha1.ChillSystem{}).
		Watches(&corev1.ConfigMap{}, mapToSystem).
		Watches(&edgev1alpha1.NodeActuation{}, mapToSystem).
		Watches(&edgev1alpha1.DeviceClass{}, mapToSystem).
		Owns(&appsv1.DaemonSet{}).
		Complete(r)
}
