package operations

import (
	context "context"
	json "encoding/json"
	errors "errors"

	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
)

func (o *Operations) EnsureInstanceService(ctx context.Context, pod *core.Pod) error {
	if value, set := pod.Labels[servingstate.InstancePodUIDLabel]; set && value != string(pod.UID) {
		return errors.New("existing preparation Pod transport identity differs")
	}
	bound := pod
	if pod.Labels[servingstate.InstancePodUIDLabel] != string(pod.UID) {
		bound = pod.DeepCopy()
		if bound.Labels == nil {
			bound.Labels = map[string]string{}
		}
		bound.Labels[servingstate.InstancePodUIDLabel] = string(pod.UID)
	}
	services := o.Kube.CoreV1().Services(pod.Namespace)
	existing, err := services.Get(ctx, servingstate.InstanceServicePrefix+string(pod.UID), meta.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && existing.DeletionTimestamp != nil {
		return errors.New("existing native Service is being removed")
	}
	if err == nil {
		if err = servingstate.ValidateInstanceService(bound, existing); err != nil {
			return err
		}
	}
	if pod.Labels[servingstate.InstancePodUIDLabel] != string(pod.UID) {
		patch, patchErr := json.Marshal([]map[string]any{
			{"op": "test", "path": "/metadata/uid", "value": pod.UID},
			{"op": "test", "path": "/metadata/labels", "value": pod.Labels},
			{"op": "add", "path": "/metadata/labels/edge.dacs.io~1serving-pod-uid", "value": string(pod.UID)},
		})
		if patchErr != nil {
			return patchErr
		}
		updated, patchErr := o.Kube.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.JSONPatchType, patch, meta.PatchOptions{})
		if patchErr != nil {
			return patchErr
		}
		*pod = *updated
	}
	if err == nil {
		return nil
	}
	_, err = services.Create(ctx, servingstate.InstanceService(pod), meta.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, err = services.Get(ctx, servingstate.InstanceServicePrefix+string(pod.UID), meta.GetOptions{})
		if err == nil {
			err = servingstate.ValidateInstanceService(pod, existing)
			if err == nil && existing.DeletionTimestamp != nil {
				err = errors.New("existing native Service is being removed")
			}
		}
	}
	return err
}
