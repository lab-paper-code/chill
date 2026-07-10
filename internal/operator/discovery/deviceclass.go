package discovery

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/operator/ownership"
)

func (r *DeviceDiscoveryReconciler) ensureDeviceClass(ctx context.Context, system *edgev1alpha1.ChillSystem, discovered deviceclass.DiscoveredClass) error {
	existing := &edgev1alpha1.DeviceClass{}
	if err := r.Get(ctx, types.NamespacedName{Name: discovered.Name}, existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get DeviceClass %q: %w", discovered.Name, err)
		}

		deviceClass := &edgev1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: discovered.Name,
				Labels: map[string]string{
					chillmeta.System: system.Name,
				},
				Annotations: map[string]string{
					chillmeta.ManagedBy:       chillmeta.ManagedByDeviceDiscovery,
					chillmeta.DiscoverySource: chillmeta.SourceNode,
				},
			},
			Spec: discovered.Spec,
		}
		if err := controllerutil.SetControllerReference(system, deviceClass, r.Scheme); err != nil {
			return fmt.Errorf("set DeviceClass %q owner reference: %w", discovered.Name, err)
		}
		if err := r.Create(ctx, deviceClass); err != nil {
			return fmt.Errorf("create DeviceClass %q: %w", discovered.Name, err)
		}
		return nil
	}

	if existing.Annotations[chillmeta.ManagedBy] != chillmeta.ManagedByDeviceDiscovery {
		return nil
	}
	if !ownership.BelongsToChillSystem(existing, system) {
		return nil
	}

	original := existing.DeepCopy()
	ownership.EnsureSystemLabel(existing, system.Name)
	if !deviceclass.SpecEqual(existing.Spec, discovered.Spec) {
		existing.Spec = discovered.Spec
	}
	if err := controllerutil.SetControllerReference(system, existing, r.Scheme); err != nil {
		return fmt.Errorf("set DeviceClass %q owner reference: %w", discovered.Name, err)
	}
	if err := r.Patch(ctx, existing, client.MergeFrom(original)); err != nil {
		return fmt.Errorf("patch DeviceClass %q: %w", discovered.Name, err)
	}
	return nil
}

func (r *DeviceDiscoveryReconciler) pruneDeviceClasses(ctx context.Context, system *edgev1alpha1.ChillSystem, discovered map[string]struct{}) error {
	deviceClasses := &edgev1alpha1.DeviceClassList{}
	if err := r.List(ctx, deviceClasses); err != nil {
		return fmt.Errorf("list DeviceClasses for pruning: %w", err)
	}

	for i := range deviceClasses.Items {
		deviceClass := &deviceClasses.Items[i]
		if deviceClass.Annotations[chillmeta.ManagedBy] != chillmeta.ManagedByDeviceDiscovery {
			continue
		}
		if !ownership.BelongsToChillSystem(deviceClass, system) {
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
