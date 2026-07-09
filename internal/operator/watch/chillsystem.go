package watch

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

// EnqueueChillSystems maps related object events to ChillSystem reconcile requests.
func EnqueueChillSystems(reader client.Reader, include func(edgev1alpha1.ChillSystem) bool) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		systems := &edgev1alpha1.ChillSystemList{}
		if err := reader.List(ctx, systems); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "list ChillSystems for event mapping")
			return nil
		}
		requests := make([]reconcile.Request, 0, len(systems.Items))
		for i := range systems.Items {
			system := systems.Items[i]
			if include != nil && !include(system) {
				continue
			}
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: system.Name}})
		}
		return requests
	})
}
