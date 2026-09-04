// Package controller adapts owned ProfilingRun objects to the pure
// batch-energy-optimization progress rules. It performs no derivation of its own.
package controller

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	activeWorkloadMode   = "saturated-serial-fixed-batch"
	idleWorkloadMode     = "idle-model-loaded"
	idleInputGenerator   = "none"
	idleOutputValidation = "none"

	labelBatchEnergyOptimization = "edge.dacs.io/batch-energy-optimization"
	labelWorkloadMode            = "edge.dacs.io/workload-mode"
	labelBatch                   = "edge.dacs.io/batch"
	labelAttempt                 = "edge.dacs.io/attempt"
)

// RunName is deterministic so re-executing one decision collides instead of
// creating a duplicate trial.
func RunName(parent string, measurement batchenergyoptimization.Measurement, attempt int) string {
	return fmt.Sprintf("%s-%s-b%d-a%d", parent, measurement.WorkloadMode, measurement.Batch, attempt)
}

func attemptFromRunName(parent string, measurement batchenergyoptimization.Measurement, name string) (int, error) {
	prefix := strings.TrimSuffix(RunName(parent, measurement, 0), "0")
	encoded, found := strings.CutPrefix(name, prefix)
	if !found {
		return 0, errors.New("Run name does not match its BatchEnergyOptimization measurement")
	}
	attempt, err := strconv.Atoi(encoded)
	if err != nil || attempt < 1 || RunName(parent, measurement, attempt) != name {
		return 0, errors.New("Run name has no usable attempt")
	}
	return attempt, nil
}

func BuildRun(
	parent *edgev1alpha1.BatchEnergyOptimization,
	measurement batchenergyoptimization.Measurement,
	attempt int,
) (*edgev1alpha1.ProfilingRun, error) {
	if parent == nil {
		return nil, errors.New("a parent BatchEnergyOptimization is required")
	}
	if err := measurement.Validate(); err != nil {
		return nil, fmt.Errorf("validate measurement: %w", err)
	}
	if attempt < 1 {
		return nil, errors.New("attempt must be positive")
	}
	spec := edgev1alpha1.ProfilingRunSpec{MeasurementStateSpec: parent.Spec.State}
	switch measurement.WorkloadMode {
	case batchenergyoptimization.WorkloadModeActive:
		protocol := parent.Spec.ActiveProtocol
		spec.AdmissionPolicy = protocol.AdmissionPolicy
		spec.Measurement = edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch:            int32(measurement.Batch),
			WarmupIterations: protocol.WarmupIterations,
			DurationSeconds:  protocol.DurationSeconds,
			WorkloadMode:     activeWorkloadMode,
			InputGenerator:   protocol.InputGenerator,
			OutputValidation: protocol.OutputValidation,
		}
		spec.MetricObservation = protocol.MetricObservation
	case batchenergyoptimization.WorkloadModeIdle:
		protocol := parent.Spec.IdleProtocol
		spec.AdmissionPolicy = protocol.AdmissionPolicy
		settling := protocol.SettlingDurationSeconds
		spec.Measurement = edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch:                   int32(measurement.Batch),
			WarmupIterations:        0,
			SettlingDurationSeconds: &settling,
			DurationSeconds:         protocol.DurationSeconds,
			WorkloadMode:            idleWorkloadMode,
			InputGenerator:          idleInputGenerator,
			OutputValidation:        idleOutputValidation,
		}
		spec.MetricObservation = protocol.MetricObservation
	}
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      RunName(parent.Name, measurement, attempt),
			Namespace: parent.Namespace,
			Labels: map[string]string{
				labelBatchEnergyOptimization: parent.Name,
				labelWorkloadMode:            string(measurement.WorkloadMode),
				labelBatch:                   fmt.Sprintf("%d", measurement.Batch),
				labelAttempt:                 fmt.Sprintf("%d", attempt),
			},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
				parent, edgev1alpha1.GroupVersion.WithKind("BatchEnergyOptimization"),
			)},
		},
		Spec: spec,
	}, nil
}
