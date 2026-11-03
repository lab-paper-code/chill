package queryapi

import "time"

// DispatchOccupancyProgress adds progress without changing the retained result envelope.
// Node remains the existing envelope's node field.
type DispatchOccupancyProgress struct {
	Name                    string                      `json:"name"`
	UID                     string                      `json:"uid"`
	BatchEnergyOptimization string                      `json:"batchEnergyOptimization"`
	Workload                string                      `json:"workload"`
	Batch                   int                         `json:"batch"`
	ProfileID               string                      `json:"profileID"`
	Phase                   *string                     `json:"phase"`
	Reason                  string                      `json:"reason"`
	CreatedAt               time.Time                   `json:"createdAt"`
	MeasurementWindow       *OccupancyMeasurementWindow `json:"measurementWindow"`
	D                       *OccupancyValue             `json:"d"`
	Evidence                *OccupancyEvidence          `json:"evidence"`
}

type OccupancyMeasurementWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}
type OccupancyValue struct {
	PerRequestNanos *int64   `json:"perRequestNanos"`
	Throughput      *float64 `json:"throughput"`
	IdleFraction    *float64 `json:"idleFraction"`
	Verdict         string   `json:"verdict"`
}
type OccupancyEvidence struct {
	Store string `json:"store"`
	Key   string `json:"key"`
	Size  int64  `json:"size"`
}
type MeasurementOccupancy struct {
	Node      MeasurementIdentity  `json:"node"`
	ProfileID string               `json:"profileID"`
	Batch     int                  `json:"batch"`
	State     string               `json:"state"`
	Request   *MeasurementIdentity `json:"request"`
	Phase     *string              `json:"phase"`
	Reason    string               `json:"reason"`
	D         *OccupancyValue      `json:"d"`
	Evidence  *OccupancyEvidence   `json:"evidence"`
	Adopted   *bool                `json:"adopted"`
}
