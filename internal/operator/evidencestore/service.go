package evidencestore

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func buildService(instance Instance) *corev1.Service {
	labels := operandLabels(instance.SystemName)
	labels[edgeMeshServiceProxyLabel] = ""
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      instance.ServiceName,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selectorLabels(instance.SystemName),
			Ports: []corev1.ServicePort{
				{
					Name:       s3PortName,
					Port:       s3Port,
					Protocol:   corev1.ProtocolTCP,
					TargetPort: intstr.FromString(s3PortName),
				},
			},
		},
	}
}
