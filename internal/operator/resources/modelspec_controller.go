package resources

import (
	"context"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// ModelSpecReconciler reconciles a ModelSpec object
type ModelSpecReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=edge.dacs.io,resources=modelspecs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=edge.dacs.io,resources=modelspecs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=edge.dacs.io,resources=modelspecs/finalizers,verbs=update

// Reconcile handles ModelSpec events.
func (r *ModelSpecReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ModelSpecReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&edgev1alpha1.ModelSpec{}).
		Complete(r)
}
