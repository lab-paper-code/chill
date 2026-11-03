package main

import (
	"math"
	"strconv"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

type dispatchOccupancyProjection struct {
	httpapi.MeasurementProjection
	queryapi.DispatchOccupancyProgress
	record *servingkube.DispatchOccupancyRecord
}
type dispatchOccupancyProjectionList struct {
	Items []dispatchOccupancyProjection `json:"items"`
}

func finiteOccupancyValue(value float64) *float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil
	}
	return &value
}

func occupancyProgress(cr api.DispatchOccupancyMeasurement) queryapi.DispatchOccupancyProgress {
	out := queryapi.DispatchOccupancyProgress{
		Name: cr.Name, UID: string(cr.UID), BatchEnergyOptimization: cr.Spec.BatchEnergyOptimization,
		Workload: cr.Spec.Workload, Batch: int(cr.Spec.Batch), ProfileID: cr.Spec.Conditions.ProfileID,
		Reason: cr.Status.Reason, CreatedAt: cr.CreationTimestamp.Time,
	}
	if cr.Status.Phase != "" {
		phase := string(cr.Status.Phase)
		out.Phase = &phase
	}
	if ref := cr.Status.Result; ref != nil {
		out.Evidence = &queryapi.OccupancyEvidence{Store: ref.Store, Key: ref.Key, Size: ref.Size}
	}
	if cr.Status.PerRequestNanos != nil {
		out.D = &queryapi.OccupancyValue{PerRequestNanos: cr.Status.PerRequestNanos}
		if value, err := strconv.ParseFloat(cr.Status.Throughput, 64); err == nil {
			out.D.Throughput = finiteOccupancyValue(value)
		}
	}
	return out
}
