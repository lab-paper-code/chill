package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	idleWorkloadMode     = "idle-model-loaded"
	idleInputGenerator   = "none"
	idleOutputValidation = "none"

	labelIdleBaselineUID = "edge.dacs.io/model-loaded-idle-baseline-uid"
	labelAttempt         = "edge.dacs.io/attempt"
)

func runName(parent string, attempt int32) string {
	return fmt.Sprintf("%s-idle-a%d", parent, attempt)
}

func buildRun(parent *edgev1alpha1.ModelLoadedIdleBaseline, attempt int32) (*edgev1alpha1.ProfilingRun, error) {
	if parent == nil || parent.UID == "" {
		return nil, errors.New("a ModelLoadedIdleBaseline with UID is required")
	}
	if attempt < 1 {
		return nil, errors.New("idle baseline attempt must be positive")
	}
	settling := parent.Spec.Protocol.SettlingDurationSeconds
	cpuQuota := parent.Spec.CPUQuota.DeepCopy()
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      runName(parent.Name, attempt),
			Namespace: parent.Namespace,
			Labels: map[string]string{
				labelIdleBaselineUID: string(parent.UID),
				labelAttempt:         fmt.Sprintf("%d", attempt),
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				parent, edgev1alpha1.GroupVersion.WithKind("ModelLoadedIdleBaseline"),
			)},
		},
		Spec: edgev1alpha1.ProfilingRunSpec{
			MeasurementStateSpec: parent.Spec.State,
			Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{
				Batch: parent.Spec.Batch, WarmupIterations: 0,
				CPUQuota:                &cpuQuota,
				CPUAffinity:             append([]int32(nil), parent.Spec.CPUAffinity...),
				SettlingDurationSeconds: &settling,
				DurationSeconds:         parent.Spec.Protocol.DurationSeconds,
				WorkloadMode:            idleWorkloadMode,
				InputGenerator:          idleInputGenerator,
				OutputValidation:        idleOutputValidation,
			},
			MetricObservation: parent.Spec.Protocol.MetricObservation,
		},
	}, nil
}
