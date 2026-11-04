// Package controller adapts owned ProfilingRun objects to the pure
// batch-energy-optimization progress rules. It performs no derivation of its own.
package controller

import (
	"encoding/json"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	activeWorkloadMode = profilingrun.EmpiricalCadenceFixedBatchWorkloadMode
	idleWorkloadMode   = "idle-model-loaded"

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

func BuildRun(
	parent *edgev1alpha1.BatchEnergyOptimization,
	measurement batchenergyoptimization.Measurement,
	attempt int,
	backend string,
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
	spec := edgev1alpha1.ProfilingRunSpec{
		MeasurementStateSpec: parent.Spec.State,
		CampaignTrial: &edgev1alpha1.ProfilingRunCampaignTrialSpec{
			Batch: int32(measurement.Batch), Attempt: int32(attempt),
		},
	}
	switch measurement.WorkloadMode {
	case batchenergyoptimization.WorkloadModeActive:
		protocol := parent.Spec.ActiveProtocol
		spec.Measurement = edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch:                    int32(measurement.Batch),
			WarmupIterations:         protocol.WarmupIterations,
			StartupWarmupIterations:  parent.Spec.ActiveProtocol.WarmupIterations,
			DurationSeconds:          protocol.DurationSeconds,
			DurationToleranceSeconds: protocol.DurationToleranceSeconds,
			WorkloadMode:             activeWorkloadMode,
			Dataset:                  protocol.Dataset,
			Workload:                 protocol.Workload,
			WarmupBatchSizes:         append([]int32(nil), parent.Spec.BatchSizes...),
		}
		spec.MetricObservation = protocol.MetricObservation
	case batchenergyoptimization.WorkloadModeIdle:
		protocol := parent.Spec.IdleProtocol
		settling := protocol.SettlingDurationSeconds
		spec.Measurement = edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch:                    int32(profilingrun.IdleExecutionBatch(backend, measurement.Batch)),
			WarmupIterations:         0,
			StartupWarmupIterations:  parent.Spec.ActiveProtocol.WarmupIterations,
			SettlingDurationSeconds:  &settling,
			DurationSeconds:          protocol.DurationSeconds,
			DurationToleranceSeconds: protocol.DurationToleranceSeconds,
			WorkloadMode:             idleWorkloadMode,
			WarmupBatchSizes:         append([]int32(nil), parent.Spec.BatchSizes...),
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

func projectFrozenActiveProtocol(
	measurement *edgev1alpha1.ProfilingRunMeasurementSpec, protocol profilederivation.CurveProtocolContract,
) error {
	measurement.Workload = protocol.WorkloadName
	measurement.WorkloadUID = protocol.WorkloadUID
	measurement.WorkloadResourceVersion = protocol.WorkloadResourceVersion
	measurement.SLOPercentile = protocol.SLOPercentile
	measurement.SLOTargetLatencyNanos = protocol.SLOTargetLatencyNanos
	measurement.MinServiceSamples = int32(protocol.MinServiceSamples)
	body, err := json.Marshal(protocol.Cadences[int(measurement.Batch)])
	if err != nil {
		return fmt.Errorf("encode frozen cadence: %w", err)
	}
	measurement.Cadence = &apiextensionsv1.JSON{Raw: body}
	return nil
}
