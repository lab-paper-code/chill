package discoverycontroller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclasscatalog"
)

func (r *DeviceDiscoveryReconciler) ensureDeviceClass(ctx context.Context, discovered deviceclasscatalog.DiscoveredClass) error {
	existing := &edgev1alpha1.DeviceClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: discovered.Name}, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get DeviceClass %q: %w", discovered.Name, err)
		}

		deviceClass := &edgev1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: discovered.Name,
				Annotations: map[string]string{
					deviceDiscoveryManagedByKey: deviceDiscoveryManagedBy,
					deviceDiscoverySourceKey:    deviceDiscoverySourceNode,
				},
			},
			Spec: discovered.Spec,
		}
		if err := r.Create(ctx, deviceClass); err != nil {
			return fmt.Errorf("create DeviceClass %q: %w", discovered.Name, err)
		}
		return nil
	}

	if existing.Annotations[deviceDiscoveryManagedByKey] != deviceDiscoveryManagedBy {
		return nil
	}
	if deviceclasscatalog.SpecEqual(existing.Spec, discovered.Spec) {
		return nil
	}

	original := existing.DeepCopy()
	existing.Spec = discovered.Spec
	if err := r.Patch(ctx, existing, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch DeviceClass %q: %w", discovered.Name, err)
	}
	return nil
}

func (r *DeviceDiscoveryReconciler) pruneDeviceClasses(ctx context.Context, discovered map[string]struct{}) error {
	deviceClasses := &edgev1alpha1.DeviceClassList{}
	if err := r.List(ctx, deviceClasses); err != nil {
		return fmt.Errorf("list DeviceClasses for pruning: %w", err)
	}

	for i := range deviceClasses.Items {
		deviceClass := &deviceClasses.Items[i]
		if deviceClass.Annotations[deviceDiscoveryManagedByKey] != deviceDiscoveryManagedBy {
			continue
		}
		if _, ok := discovered[deviceClass.Name]; ok {
			continue
		}
		if err := r.Delete(ctx, deviceClass); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete stale DeviceClass %q: %w", deviceClass.Name, err)
		}
	}
	return nil
}
