package state

import (
	context "context"
	json "encoding/json"
	errors "errors"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	intstr "k8s.io/apimachinery/pkg/util/intstr"
	kubernetes "k8s.io/client-go/kubernetes"
	ptr "k8s.io/utils/ptr"
	net "net"
	client "sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	InstancePodUIDLabel = "edge.dacs.io/serving-pod-uid"
)

const (
	EdgeMeshServiceProxyLabel = "service.edgemesh.kubeedge.io/service-proxy-name"
)

const (
	InstanceServicePrefix = "chill-executor-"
)

func InstanceService(pod *core.Pod) *core.Service {
	return &core.Service{
		ObjectMeta: meta.ObjectMeta{
			Namespace: pod.Namespace,
			Name:      InstanceServicePrefix + string(pod.UID),
			Labels: map[string]string{
				"app.kubernetes.io/component": ServingInstanceComponent,
				InstancePodUIDLabel:           string(pod.UID),
				EdgeMeshServiceProxyLabel:     "",
			},
			OwnerReferences: []meta.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: pod.Name, UID: pod.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}},
		},
		Spec: core.ServiceSpec{
			Type:     core.ServiceTypeClusterIP,
			Selector: map[string]string{InstancePodUIDLabel: string(pod.UID)},
			Ports:    []core.ServicePort{{Name: "http", Protocol: core.ProtocolTCP, Port: 8080, TargetPort: intstr.FromString("http")}},
		},
	}
}

func validateInstanceServiceOwner(pod *core.Pod, service *core.Service) error {
	if service.Namespace != pod.Namespace || service.Name != InstanceServicePrefix+string(pod.UID) || len(service.OwnerReferences) != 1 {
		return errors.New("native Service is not owned by this physical Pod")
	}
	owner := service.OwnerReferences[0]
	if owner.APIVersion != "v1" || owner.Kind != "Pod" || owner.Name != pod.Name || owner.UID != pod.UID || owner.Controller == nil || !*owner.Controller {
		return errors.New("native Service is not owned by this physical Pod")
	}
	return nil
}

func validateNativeTransportPod(pod *core.Pod) error {
	if pod.UID == "" || pod.Labels[InstancePodUIDLabel] != string(pod.UID) || pod.Spec.RestartPolicy != core.RestartPolicyNever || pod.Spec.HostNetwork || len(pod.Spec.Containers) != 1 || len(pod.Spec.EphemeralContainers) != 0 || pod.Spec.Containers[0].Name != FirstfloorContainerName {
		return errors.New("native transport requires a non-restarting isolated Pod with its actual UID label")
	}
	for _, port := range pod.Spec.Containers[0].Ports {
		if port.Name == "http" && port.ContainerPort == 8080 && port.HostPort == 0 && (port.Protocol == "" || port.Protocol == core.ProtocolTCP) {
			return nil
		}
	}
	return errors.New("native Pod does not expose the native http port")
}

func ValidateInstanceService(pod *core.Pod, service *core.Service) error {
	if err := validateInstanceServiceOwner(pod, service); err != nil {
		return err
	}
	if err := validateNativeTransportPod(pod); err != nil {
		return err
	}
	proxy, labelled := service.Labels[EdgeMeshServiceProxyLabel]
	if service.Labels[InstancePodUIDLabel] != string(pod.UID) || !labelled || proxy != "" ||
		len(service.Spec.Selector) != 1 || service.Spec.Selector[InstancePodUIDLabel] != string(pod.UID) ||
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
	service, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, InstanceServicePrefix+string(pod.UID), meta.GetOptions{})
	if err != nil {
		return nil, err
	}
	if service.DeletionTimestamp != nil {
		return nil, errors.New("native Service is being removed")
	}
	if err = ValidateInstanceService(pod, service); err != nil {
		return nil, err
	}
	selected, err := kube.CoreV1().Pods(pod.Namespace).List(ctx, meta.ListOptions{LabelSelector: labels.SelectorFromSet(service.Spec.Selector).String()})
	if err != nil {
		return nil, err
	}
	if len(selected.Items) != 1 || !SameNativeContainer(pod, &selected.Items[0]) || selected.Items[0].Name != pod.Name || selected.Items[0].Spec.NodeName != pod.Spec.NodeName {
		return nil, errors.New("native Service does not select only the current physical executor")
	}
	if err = ValidateInstanceService(&selected.Items[0], service); err != nil {
		return nil, err
	}
	return service, nil
}

// ListedInstanceService finds the Pod's own Service in one namespace list; the
// caller checks the selected executor against a later Pod list.
func ListedInstanceService(pod *core.Pod, services map[string]*core.Service, listErr error) (*core.Service, error) {
	if listErr != nil {
		return nil, listErr
	}
	service := services[InstanceServicePrefix+string(pod.UID)]
	if service == nil {
		return nil, errors.New("native Service unavailable")
	}
	if service.DeletionTimestamp != nil {
		return nil, errors.New("native Service is being removed")
	}
	return service, ValidateInstanceService(pod, service)
}

func InstanceServiceEndpoint(service *core.Service) (string, error) {
	if net.ParseIP(service.Spec.ClusterIP) == nil {
		return "", errors.New("native Service ClusterIP unavailable")
	}
	return "http://" + net.JoinHostPort(service.Spec.ClusterIP, "8080"), nil
}

func NativeInstanceEndpoint(ctx context.Context, kube kubernetes.Interface, pod *core.Pod) (string, error) {
	service, err := nativeInstanceService(ctx, kube, pod)
	if err != nil {
		return "", err
	}
	return InstanceServiceEndpoint(service)
}

func DeleteInstanceService(ctx context.Context, kube kubernetes.Interface, pod *core.Pod) error {
	service, err := kube.CoreV1().Services(pod.Namespace).Get(ctx, InstanceServicePrefix+string(pod.UID), meta.GetOptions{})
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
	return IgnoreNotFound(kube.CoreV1().Services(pod.Namespace).Delete(ctx, service.Name, meta.DeleteOptions{Preconditions: &meta.Preconditions{UID: &uid, ResourceVersion: &rv}}))
}

// PatchSourceFinalizer changes only our primitive-list member. The API server
// rejects a different immutable UID, but unrelated status/RV and other
// finalizer updates do not invalidate this metadata-only delta.
func PatchSourceFinalizer(ctx context.Context, c client.Client, pod *core.Pod, present bool) error {
	if pod.UID == "" {
		return errors.New("physical Pod UID required for source finalizer")
	}
	metadata := map[string]any{"uid": pod.UID}
	if present {
		metadata["finalizers"] = []string{SourceFinalizer}
	} else {
		metadata["$deleteFromPrimitiveList/finalizers"] = []string{SourceFinalizer}
	}
	patch, err := json.Marshal(map[string]any{"metadata": metadata})
	if err != nil {
		return err
	}
	return c.Patch(ctx, pod, client.RawPatch(types.StrategicMergePatchType, patch))
}
