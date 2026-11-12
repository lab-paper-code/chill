// Package controller adapts owned ProfilingRun objects to the pure
// batch-energy-optimization progress rules. It performs no derivation of its own.
package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
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

// campaignNameLabelValue preserves existing short-name selectors. Long names
// use a bounded candidate token; the controller owner UID remains authority.
func campaignNameLabelValue(name string) string {
	if len(name) <= 63 {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	return "beo-" + hex.EncodeToString(sum[:28])
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
		points := protocol.OperatingPoints
		points.Utilizations = append([]float64(nil), points.Utilizations...)
		spec.Measurement = edgev1alpha1.ProfilingRunMeasurementSpec{
			Batch:                    int32(measurement.Batch),
			WarmupIterations:         0,
			StartupWarmupIterations:  parent.Spec.ActiveProtocol.WarmupIterations,
			DurationSeconds:          protocol.DurationSeconds,
			DurationToleranceSeconds: protocol.DurationToleranceSeconds,
			WorkloadMode:             activeWorkloadMode,
			Dataset:                  protocol.Dataset,
			Workload:                 protocol.Workload,
			WarmupBatchSizes:         []int32{1},
			OperatingPoints:          &points,
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
			WarmupBatchSizes:         []int32{1},
		}
		spec.MetricObservation = protocol.MetricObservation
	}
	return &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{
			Name:      batchenergyoptimization.RunName(parent.Name, measurement, attempt),
			Namespace: parent.Namespace,
			Labels: map[string]string{
				labelBatchEnergyOptimization: campaignNameLabelValue(parent.Name),
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

// applyFrozenRunProtocol preserves the actual recorded startup and measurement
// contract, including plans written before automatic discovery.
func applyFrozenRunProtocol(run *edgev1alpha1.ProfilingRun, plan profilederivation.CurvePlan, mode batchenergyoptimization.WorkloadMode) {
	shared := plan.Shared()
	protocol := shared.ActiveProtocol
	if mode == batchenergyoptimization.WorkloadModeIdle {
		protocol = shared.IdleProtocol
	}
	measurement := &run.Spec.Measurement
	measurement.WarmupIterations = int32(protocol.WarmupIterations)
	measurement.StartupWarmupIterations = int32(protocol.StartupWarmupIterations)
	measurement.WarmupBatchSizes = make([]int32, len(shared.Actor.WarmupBatchSizes))
	for index, batch := range shared.Actor.WarmupBatchSizes {
		measurement.WarmupBatchSizes[index] = int32(batch)
	}
	measurement.DurationSeconds = int32(protocol.DurationSeconds)
	tolerance := protocol.DurationToleranceSeconds
	measurement.DurationToleranceSeconds = &tolerance
	measurement.Workload = protocol.WorkloadName
	measurement.OperatingPoints = nil
	if p := protocol.OperatingPoints; p != nil {
		measurement.OperatingPoints = &edgev1alpha1.MeasurementOperatingPointsSpec{
			Utilizations: append([]float64(nil), p.Utilizations...), SparseMeanGapMilliseconds: p.SparseMeanGapMilliseconds,
			PhaseDurationSeconds: int32(p.PhaseDurationSeconds), RequestLifetimeSeconds: int32(p.RequestLifetimeSeconds),
			PostLoadDurationSeconds:        int32(p.PostLoadDurationSeconds),
			DeliveryMarginMilliseconds:     int32(p.DeliveryMarginMilliseconds),
			FramedDecodedRequestUpperBytes: p.FramedDecodedRequestUpperBytes,
		}
	}
	if protocol.Dataset != nil {
		measurement.Dataset = protocol.Dataset.Name
	}
	if mode == batchenergyoptimization.WorkloadModeIdle {
		settling := int32(protocol.SettlingDurationSeconds)
		measurement.SettlingDurationSeconds = &settling
	}
	run.Spec.MetricObservation = edgev1alpha1.ProfilingRunMetricObservationSpec{
		IntervalMilliseconds: int32(protocol.MetricIntervalMilliseconds),
		TimeoutMilliseconds:  int32(protocol.MetricTimeoutMilliseconds),
		DurationSeconds:      int32(protocol.MetricDurationSeconds),
	}
}
