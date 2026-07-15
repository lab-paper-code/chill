package evidencestore

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func buildPersistentVolumeClaim(instance Instance, config Config) *corev1.PersistentVolumeClaim {
	claim := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      instance.PersistentVolumeClaimName,
			Labels:    operandLabels(instance.SystemName),
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: config.StorageSize.DeepCopy()},
			},
		},
	}
	if storageClassName := strings.TrimSpace(config.StorageClassName); storageClassName != "" {
		claim.Spec.StorageClassName = &storageClassName
	}
	return claim
}
