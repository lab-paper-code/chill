package kube

import (
	"context"
	"errors"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const WorkloadFinalizer = "edge.dacs.io/serving-workload"

// Store uses a direct client: management decisions never read an informer cache.
// The coordinator owns semantic model validation and operation serialization.
type Store struct {
	client client.Client
}

func NewStore(config *rest.Config, scheme *runtime.Scheme) (*Store, error) {
	if config == nil || scheme == nil {
		return nil, errors.New("Kubernetes config and scheme are required")
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	return &Store{client: c}, nil
}

func (s *Store) GetWorkload(ctx context.Context, name string) (*edgev1alpha1.Workload, error) {
	obj := &edgev1alpha1.Workload{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *Store) ListWorkloads(ctx context.Context) (*edgev1alpha1.WorkloadList, error) {
	list := &edgev1alpha1.WorkloadList{}
	if err := s.client.List(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *Store) CreateWorkload(ctx context.Context, obj *edgev1alpha1.Workload) error {
	if obj.Namespace != "" {
		return errors.New("Workload must be cluster scoped")
	}
	controllerutil.AddFinalizer(obj, WorkloadFinalizer)
	return s.client.Create(ctx, obj)
}

func (s *Store) UpdateWorkload(ctx context.Context, obj *edgev1alpha1.Workload) error {
	if err := requireVersion(obj); err != nil {
		return err
	}
	return s.client.Update(ctx, obj)
}

func (s *Store) DeleteWorkload(ctx context.Context, obj *edgev1alpha1.Workload) error {
	return s.delete(ctx, obj)
}

// UpdateWorkloadStatus returns the updated object, including its resourceVersion.
func (s *Store) UpdateWorkloadStatus(ctx context.Context, before *edgev1alpha1.Workload, status edgev1alpha1.WorkloadStatus) (*edgev1alpha1.Workload, error) {
	if err := requireVersion(before); err != nil {
		return nil, err
	}
	after := before.DeepCopy()
	after.Status = status
	if err := s.client.Status().Update(ctx, after); err != nil {
		return nil, err
	}
	return after, nil
}

func (s *Store) EnsureWorkloadFinalizer(ctx context.Context, obj *edgev1alpha1.Workload) error {
	if !obj.DeletionTimestamp.IsZero() {
		return errors.New("cannot add serving finalizer to a deleting Workload")
	}
	return s.patchFinalizer(ctx, obj, true)
}

// RemoveWorkloadFinalizer is called only after the coordinator confirms drain
// and actual runtime resource cleanup. Its success does not prove deletion.
func (s *Store) RemoveWorkloadFinalizer(ctx context.Context, obj *edgev1alpha1.Workload) error {
	if obj.DeletionTimestamp.IsZero() {
		return errors.New("cannot remove serving finalizer before deletion is requested")
	}
	return s.patchFinalizer(ctx, obj, false)
}

func (s *Store) patchFinalizer(ctx context.Context, obj *edgev1alpha1.Workload, add bool) error {
	if err := requireVersion(obj); err != nil {
		return err
	}
	before := obj.DeepCopy()
	changed := false
	if add {
		changed = controllerutil.AddFinalizer(obj, WorkloadFinalizer)
	} else {
		changed = controllerutil.RemoveFinalizer(obj, WorkloadFinalizer)
	}
	if !changed {
		return nil
	}
	return s.client.Patch(ctx, obj, client.MergeFromWithOptions(before, client.MergeFromWithOptimisticLock{}))
}

// WorkloadDeleted confirms disappearance of the exact registration. A newly
// registered object with the same name does not resurrect the deleted UID.
func (s *Store) WorkloadDeleted(ctx context.Context, name string, uid types.UID) (bool, error) {
	if uid == "" {
		return false, errors.New("Workload UID is required for deletion confirmation")
	}
	obj, err := s.GetWorkload(ctx, name)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return obj.UID != uid, nil
}

func (s *Store) GetModel(ctx context.Context, name string) (*edgev1alpha1.ModelSpec, error) {
	obj := &edgev1alpha1.ModelSpec{}
	if err := s.client.Get(ctx, client.ObjectKey{Name: name}, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func (s *Store) ListModels(ctx context.Context) (*edgev1alpha1.ModelSpecList, error) {
	list := &edgev1alpha1.ModelSpecList{}
	if err := s.client.List(ctx, list); err != nil {
		return nil, err
	}
	return list, nil
}

func (s *Store) CreateModel(ctx context.Context, obj *edgev1alpha1.ModelSpec) error {
	if obj.Namespace != "" {
		return errors.New("ModelSpec must be cluster scoped")
	}
	return s.client.Create(ctx, obj)
}

func (s *Store) UpdateModel(ctx context.Context, obj *edgev1alpha1.ModelSpec) error {
	if err := requireVersion(obj); err != nil {
		return err
	}
	return s.client.Update(ctx, obj)
}

func (s *Store) DeleteModel(ctx context.Context, obj *edgev1alpha1.ModelSpec) error {
	return s.delete(ctx, obj)
}

func requireVersion(obj client.Object) error {
	if obj.GetNamespace() != "" {
		return errors.New("management resources must be cluster scoped")
	}
	if obj.GetResourceVersion() == "" {
		return errors.New("resourceVersion precondition is required")
	}
	return nil
}

func (s *Store) delete(ctx context.Context, obj client.Object) error {
	if err := requireVersion(obj); err != nil {
		return err
	}
	uid, version := obj.GetUID(), obj.GetResourceVersion()
	if uid == "" {
		return errors.New("UID precondition is required for deletion")
	}
	return s.client.Delete(ctx, obj, &client.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &version}})
}
