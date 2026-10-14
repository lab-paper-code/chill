package kube

import (
	"context"
	"encoding/json"
	"errors"
	"net"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/ptr"
)

const (
	instancePodUIDLabel       = "edge.dacs.io/serving-pod-uid"
	edgeMeshServiceProxyLabel = "service.edgemesh.kubeedge.io/service-proxy-name"
)

func instanceService(pod *core.Pod) *core.Service {
	return &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Namespace: pod.Namespace,
			Name:      "chill-native-" + string(pod.UID),
			Labels: map[string]string{
				"app.kubernetes.io/component": servingInstanceComponent,
				instancePodUIDLabel:           string(pod.UID),
				edgeMeshServiceProxyLabel:     "",
			},
			OwnerReferences: []meta.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}},
		},
		Spec: core.ServiceSpec{
			Type:     core.ServiceTypeClusterIP,
			Selector: map[string]string{instancePodUIDLabel: string(pod.UID)},
			Ports:    []core.ServicePort{{Name: "http", Protocol: core.ProtocolTCP, Port: 8080, TargetPort: intstr.FromString("http")}},
		},
	}
}

func (o *Operations) ensureInstanceService(ctx context.Context, pod *core.Pod) error {
	if value, set := pod.Labels[instancePodUIDLabel]; set && value != string(pod.UID) {
		return errors.New("existing preparation Pod transport identity differs")
	}
	bound := pod
	if pod.Labels[instancePodUIDLabel] != string(pod.UID) {
		bound = pod.DeepCopy()
		if bound.Labels == nil {
			bound.Labels = map[string]string{}
		}
		bound.Labels[instancePodUIDLabel] = string(pod.UID)
	}
	services := o.kube.CoreV1().Services(pod.Namespace)
	existing, err := services.Get(ctx, "chill-native-"+string(pod.UID), meta.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && existing.DeletionTimestamp != nil {
		return errors.New("existing native Service is being removed")
	}
	if err == nil {
		if err = validateInstanceService(bound, existing); err != nil {
			return err
		}
	}
	if pod.Labels[instancePodUIDLabel] != string(pod.UID) {
		patch, patchErr := json.Marshal([]map[string]any{
			{"op": "test", "path": "/metadata/uid", "value": pod.UID},
			{"op": "test", "path": "/metadata/labels", "value": pod.Labels},
			{"op": "add", "path": "/metadata/labels/edge.dacs.io~1serving-pod-uid", "value": string(pod.UID)},
		})
		if patchErr != nil {
			return patchErr
		}
		updated, patchErr := o.kube.CoreV1().Pods(pod.Namespace).Patch(ctx, pod.Name, types.JSONPatchType, patch, meta.PatchOptions{})
		if patchErr != nil {
			return patchErr
		}
		*pod = *updated
	}
	if err == nil {
		return nil
	}
	_, err = services.Create(ctx, instanceService(pod), meta.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		existing, err = services.Get(ctx, "chill-native-"+string(pod.UID), meta.GetOptions{})
		if err == nil {
			err = validateInstanceService(pod, existing)
			if err == nil && existing.DeletionTimestamp != nil {
				err = errors.New("existing native Service is being removed")
			}
		}
	}
	return err
}

func validateInstanceServiceOwner(pod *core.Pod, service *core.Service) error {
	if service.Namespace != pod.Namespace || service.Name != "chill-native-"+string(pod.UID) || len(service.OwnerReferences) != 1 {
		return errors.New("native Service is not owned by this physical Pod")
	}
	owner := service.OwnerReferences[0]
	if owner.APIVersion != "v1" || owner.Kind != "Pod" || owner.Name != pod.Name || owner.UID != pod.UID || owner.Controller == nil || !*owner.Controller {
		return errors.New("native Service is not owned by this physical Pod")
	}
	return nil
}

func validateNativeTransportPod(pod *core.Pod) error {
	if pod.UID == "" || pod.Labels[instancePodUIDLabel] != string(pod.UID) || pod.Spec.RestartPolicy != core.RestartPolicyNever || pod.Spec.HostNetwork || len(pod.Spec.Containers) != 1 || len(pod.Spec.EphemeralContainers) != 0 || pod.Spec.Containers[0].Name != firstfloorContainerName {
		return errors.New("native transport requires a non-restarting isolated Pod with its actual UID label")
	}
	for _, port := range pod.Spec.Containers[0].Ports {
		if port.Name == "http" && port.ContainerPort == 8080 && port.HostPort == 0 && (port.Protocol == "" || port.Protocol == core.ProtocolTCP) {
			return nil
		}
	}
	return errors.New("native Pod does not expose the native http port")
}

func validateInstanceService(pod *core.Pod, service *core.Service) error {
	if err := validateInstanceServiceOwner(pod, service); err != nil {
		return err
	}
	if err := validateNativeTransportPod(pod); err != nil {
		return err
	}
	proxy, labelled := service.Labels[edgeMeshServiceProxyLabel]
	if service.Labels[instancePodUIDLabel] != string(pod.UID) || !labelled || proxy != "" ||
		len(service.Spec.Selector) != 1 || service.Spec.Selector[instancePodUIDLabel] != string(pod.UID) ||
		service.Spec.Type != core.ServiceTypeClusterIP || len(service.Spec.Ports) != 1 ||
		service.Spec.PublishNotReadyAddresses || len(service.Spec.ExternalIPs) != 0 || service.Spec.ExternalName != "" ||
		service.Spec.SessionAffinity != core.ServiceAffinityNone && service.Spec.SessionAffinity != "" ||
		service.Spec.InternalTrafficPolicy != nil && *service.Spec.InternalTrafficPolicy != core.ServiceInternalTrafficPolicyCluster {
		return errors.New("native Service differs from the actual single-Pod transport binding")
	}
	port := service.Spec.Ports[0]
	if port.Name != "http" || port.Protocol != core.ProtocolTCP || port.Port != 8080 || port.TargetPort != intstr.FromString("http") || port.NodePort != 0 || port.AppProtocol != nil {
		return errors.New("native Service does not expose the native http port")
	}
	return nil
}

func nativeInstanceService(ctx context.Context, kube kubernetes.Interface, pod *core.Pod) (*core.Service, error) {
	service, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, "chill-native-"+string(pod.UID), meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	if service.DeletionTimestamp != nil {
		return nil, errors.New("native Service is being removed")
	}
	if err = validateInstanceService(pod, service); err != nil {
		return nil, err
	}
	selected, err := kube.CoreV1().Pods(pod.Namespace).List(ctx, meta.ListOptions{LabelSelector: labels.SelectorFromSet(service.Spec.Selector).String()})
	if err != nil {
		return nil, err
	}
	if len(selected.Items) != 1 || !sameNativeContainer(pod, &selected.Items[0]) || selected.Items[0].Name != pod.Name || selected.Items[0].Spec.NodeName != pod.Spec.NodeName {
		return nil, errors.New("native Service does not select only the current physical executor")
	}
	if err = validateInstanceService(&selected.Items[0], service); err != nil {
		return nil, err
	}
	return service, nil
}

func instanceServiceEndpoint(service *core.Service) (string, error) {
	if net.ParseIP(service.Spec.ClusterIP) == nil {
		return "", errors.New("native Service ClusterIP unavailable")
	}
	return "http://" + net.JoinHostPort(service.Spec.ClusterIP, "8080"), nil
}

func nativeInstanceEndpoint(ctx context.Context, kube kubernetes.Interface, pod *core.Pod) (string, error) {
	service, err := nativeInstanceService(ctx, kube, pod)
	if err != nil {
		return "", err
	}
	return instanceServiceEndpoint(service)
}

func deleteInstanceService(ctx context.Context, kube kubernetes.Interface, pod *core.Pod) error {
	service, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, "chill-native-"+string(pod.UID), meta.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err = validateInstanceServiceOwner(pod, service); err != nil {
		return err
	}
	uid, rv := service.UID, service.ResourceVersion
	return eIfNotFound(kube.CoreV1().Services(pod.Namespace).Delete(ctx, service.Name, meta.DeleteOptions{Preconditions: &meta.Preconditions{UID: &uid, ResourceVersion: &rv}}))
}
