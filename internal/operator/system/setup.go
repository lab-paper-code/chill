package system

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operator/watch"
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
		Watches(&corev1.Pod{}, watch.EnqueueChillSystems(r.Client, nil), builder.WithPredicates(predicate.NewPredicateFuncs(func(object client.Object) bool {
			pod := object.(*corev1.Pod)
			owner := metav1.GetControllerOf(pod)
			return pod.Status.Phase == corev1.PodSucceeded && owner != nil && owner.APIVersion == appsv1.SchemeGroupVersion.String() && owner.Kind == "ReplicaSet"
		}))).
		Complete(r)
}
