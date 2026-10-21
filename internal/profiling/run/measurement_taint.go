package profilingrun

import (
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// MeasurementTaintKey marks a Node of the measurement POOL. ChillServer adds it
// with effect NoSchedule when it admits a measurement onto the Node, the value
// naming that measurement, and removes it when the Node is handed back.
const MeasurementTaintKey = "chill.dacs.io/measurement"

// AdmissionAnnotation carries ChillServer's admission of a measurement object:
// "Granted <RFC3339 time>" or "Refused: <Reason> <detail>".
const AdmissionAnnotation = "chill.dacs.io/admission"

// InvalidAnnotation marks a measurement whose Node changed power mode inside
// the measurement window by a request the measurement did not make. Evidence
// readers count it and do not use it.
const InvalidAnnotation = "chill.dacs.io/measurement-invalid"

// RunnerProfileAnnotation names the profile of the transition-cost runner
// ChillServer prepared for a PowerModeTransitionCost.
const RunnerProfileAnnotation = "chill.dacs.io/runner-profile"

const (
	admissionGranted = "Granted "
	admissionRefused = "Refused: "
)

// GrantedAdmission is the admission annotation value of a grant at.
func GrantedAdmission(at time.Time) string {
	return admissionGranted + at.UTC().Format(time.RFC3339Nano)
}

// RefusedAdmission is the admission annotation value of a refusal.
func RefusedAdmission(reason, detail string) string {
	return strings.TrimSpace(admissionRefused + reason + " " + detail)
}

// Admission reads the admission annotation: whether the measurement is granted,
// when, and the refusal reason (its first word) when it is refused.
func Admission(annotations map[string]string) (granted bool, at time.Time, refused string) {
	value := annotations[AdmissionAnnotation]
	if rest, ok := strings.CutPrefix(value, admissionGranted); ok {
		at, err := time.Parse(time.RFC3339Nano, rest)
		return err == nil, at, ""
	}
	if rest, ok := strings.CutPrefix(value, admissionRefused); ok {
		reason, _, _ := strings.Cut(rest, " ")
		return false, time.Time{}, reason
	}
	return false, time.Time{}, ""
}

// MeasurementToleration lets a measurement Pod run on a measurement POOL Node.
func MeasurementToleration() corev1.Toleration {
	return corev1.Toleration{Key: MeasurementTaintKey, Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
}

// InMeasurement reports whether the Node carries the measurement POOL taint.
func InMeasurement(node *corev1.Node) bool {
	_, ok := MeasurementHolder(node)
	return ok
}

// MeasurementHolder returns the value of the Node's measurement taint, the
// measurement it is admitted to.
func MeasurementHolder(node *corev1.Node) (string, bool) {
	for _, taint := range node.Spec.Taints {
		if taint.Key == MeasurementTaintKey && taint.Effect == corev1.TaintEffectNoSchedule {
			return taint.Value, true
		}
	}
	return "", false
}
