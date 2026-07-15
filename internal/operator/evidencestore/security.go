package evidencestore

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

func restrictedContainerSecurityContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: ptr.To(false),
		Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		ReadOnlyRootFilesystem:   ptr.To(true),
		RunAsGroup:               ptr.To[int64](1000),
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To[int64](1000),
	}
}

func runtimeDefaultPodSecurityContext() *corev1.PodSecurityContext {
	return &corev1.PodSecurityContext{
		SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
	}
}
