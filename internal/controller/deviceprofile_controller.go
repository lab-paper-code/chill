package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// DeviceProfileReconciler reconciles a DeviceProfile object
type DeviceProfileReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceprofiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=deviceprofiles/finalizers,verbs=update

// Reconcile handles DeviceProfile events.
func (r *DeviceProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *DeviceProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&edgev1alpha1.DeviceProfile{}).
		Complete(r)
}
