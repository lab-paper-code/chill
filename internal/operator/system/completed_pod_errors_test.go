package system

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestCompletedPodGCErrorDoesNotBlockSystemStatus(t *testing.T) {
	for _, operation := range []string{"list", "delete"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			r, system, deployment, rs, pod := completedPodFixture(t)
			system.Generation = 7
			base := fake.NewClientBuilder().WithScheme(r.Scheme).WithStatusSubresource(system).WithObjects(system, deployment, rs, pod).Build()
			failures := 0
			gcErr := errors.New("Pod collection unavailable")
			r.Client = interceptor.NewClient(base, interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*corev1.PodList); ok && operation == "list" {
						failures++
						return gcErr
					}
					return c.List(ctx, list, opts...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.DeleteOption) error {
					if _, ok := object.(*corev1.Pod); ok && operation == "delete" {
						failures++
						return gcErr
					}
					return c.Delete(ctx, object, opts...)
				},
			})
			result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(system)})
			if err != nil || result.RequeueAfter == 0 || failures != 1 {
				t.Fatalf("GC failure must only schedule retry: result=%v err=%v failures=%d", result, err, failures)
			}
			saved := &edgev1alpha1.ChillSystem{}
			if err := r.Get(ctx, client.ObjectKeyFromObject(system), saved); err != nil {
				t.Fatal(err)
			}
			if saved.Status.ObservedGeneration != system.Generation || len(saved.Status.Conditions) == 0 {
				t.Fatalf("Pod GC failure blocked system status: %+v", saved.Status)
			}
			if err := r.Get(ctx, client.ObjectKeyFromObject(pod), &corev1.Pod{}); err != nil {
				t.Fatalf("failed GC must leave Pod for retry: %v", err)
			}
		})
	}
}
