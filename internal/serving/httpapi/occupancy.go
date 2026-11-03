package httpapi

import api "github.com/lab-paper-code/chill/api/v1alpha1"

// OccupancyIntent selects what to measure. Protocol fields are server-owned.
type OccupancyIntent struct {
 Name string `json:"name,omitempty"`
 Node string `json:"node"`
 BatchEnergyOptimization string `json:"batchEnergyOptimization"`
 Workload string `json:"workload"`
 Batch int32 `json:"batch"`
}

// OccupancyMutationResult returns the complete immutable native task.
type OccupancyMutationResult struct {
 Request string `json:"request"`
 Task *api.DispatchOccupancyMeasurement `json:"task"`
 EvidenceDeleted bool `json:"evidenceDeleted,omitempty"`
 DeletedEvidence []MeasurementEvidenceLocation `json:"deletedEvidence,omitempty"`
}
