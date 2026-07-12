package powerobserverprobe

// TODO(internal): Replace Pod-IP coupling with a stable PowerSource resolver
// contract that can select Service, Pod, or external endpoint forms and report
// ambiguity/unavailability as Kubernetes conditions.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

var (
	ErrSourceUnavailable = errors.New("power source unavailable")
	ErrSourceNotReady    = errors.New("power source not ready")
	ErrSourceAmbiguous   = errors.New("power source ambiguous")
)

type ResolvedTarget struct {
	NodeName  string `json:"nodeName"`
	Namespace string `json:"namespace"`
	PodName   string `json:"podName"`
	Endpoint  string `json:"endpoint"`
}

type TargetResolver interface {
	Resolve(context.Context, string) (ResolvedTarget, error)
}

type KubernetesPodResolver struct {
	client        kubernetes.Interface
	namespace     string
	labelSelector string
	port          int
}

func NewKubernetesPodResolver(
	client kubernetes.Interface,
	namespace string,
	labelSelector string,
	port int,
) (*KubernetesPodResolver, error) {
	switch {
	case client == nil:
		return nil, errors.New("Kubernetes client is required")
	case namespace == "":
		return nil, errors.New("namespace is required")
	case labelSelector == "":
		return nil, errors.New("label selector is required")
	case port < 1 || port > 65535:
		return nil, errors.New("port must be between 1 and 65535")
	}
	return &KubernetesPodResolver{
		client: client, namespace: namespace, labelSelector: labelSelector, port: port,
	}, nil
}

func (r *KubernetesPodResolver) Resolve(ctx context.Context, nodeName string) (ResolvedTarget, error) {
	if nodeName == "" {
		return ResolvedTarget{}, errors.New("node name is required")
	}
	pods, err := r.client.CoreV1().Pods(r.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: r.labelSelector,
		FieldSelector: fields.OneTermEqualSelector("spec.nodeName", nodeName).String(),
	})
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("list power-source Pods on node %q: %w", nodeName, err)
	}
	if len(pods.Items) == 0 {
		return ResolvedTarget{}, fmt.Errorf("%w: no matching Pod on node %q", ErrSourceUnavailable, nodeName)
	}

	ready := make([]corev1.Pod, 0, 1)
	for _, pod := range pods.Items {
		if isReadySourcePod(pod) {
			ready = append(ready, pod)
		}
	}
	switch len(ready) {
	case 0:
		return ResolvedTarget{}, fmt.Errorf("%w: %d matching Pod(s) on node %q", ErrSourceNotReady, len(pods.Items), nodeName)
	case 1:
		pod := ready[0]
		endpoint := &url.URL{
			Scheme: "http",
			Host:   net.JoinHostPort(pod.Status.PodIP, strconv.Itoa(r.port)),
			Path:   "/metrics",
		}
		return ResolvedTarget{
			NodeName: nodeName, Namespace: pod.Namespace, PodName: pod.Name,
			Endpoint: endpoint.String(),
		}, nil
	default:
		return ResolvedTarget{}, fmt.Errorf("%w: %d Ready Pods on node %q", ErrSourceAmbiguous, len(ready), nodeName)
	}
}

func isReadySourcePod(pod corev1.Pod) bool {
	if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || pod.Status.PodIP == "" {
		return false
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}
