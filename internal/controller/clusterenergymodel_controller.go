package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/gearedge/api/v1alpha1"
)

// ClusterEnergyModelReconciler reconciles a ClusterEnergyModel object
type ClusterEnergyModelReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=clusterenergymodels,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=edge.dacs.io,resources=clusterenergymodels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=clusterenergymodels/finalizers,verbs=update

// Reconcile handles ClusterEnergyModel events.
func (r *ClusterEnergyModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ClusterEnergyModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&edgev1alpha1.ClusterEnergyModel{}).
		Complete(r)
}
