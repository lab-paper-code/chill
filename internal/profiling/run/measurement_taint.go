package profilingrun

import corev1 "k8s.io/api/core/v1"

// MeasurementTaintKey marks a Node of the measurement POOL. An operator adds it
// with effect NoSchedule before a measurement and removes it afterwards.
const MeasurementTaintKey = "chill.dacs.io/measurement"

// MeasurementToleration lets a measurement Pod run on a measurement POOL Node.
func MeasurementToleration() corev1.Toleration {
	return corev1.Toleration{Key: MeasurementTaintKey, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
}

// InMeasurement reports whether the Node carries the measurement POOL taint.
func InMeasurement(node *corev1.Node) bool {
	for _, taint := range node.Spec.Taints {
		if taint.Key == MeasurementTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
			return true
		}
	}
	return false
}
