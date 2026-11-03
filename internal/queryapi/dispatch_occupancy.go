package queryapi

import (
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

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
	SchemaVersion           string                      `json:"schemaVersion"`
	Estimate                *OccupancyEstimate          `json:"estimate"`
	Verdict                 string                      `json:"verdict"`
	MinimumPairs            int                         `json:"minimumPairs"`
	Saturation              *bool                       `json:"saturation"`
	IdleFraction            *float64                    `json:"idleFraction"`
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
	Node       MeasurementIdentity  `json:"node"`
	ProfileID  string               `json:"profileID"`
	Batch      int                  `json:"batch"`
	State      string               `json:"state"`
	Request    *MeasurementIdentity `json:"request"`
	Phase      *string              `json:"phase"`
	Reason     string               `json:"reason"`
	D          *OccupancyValue      `json:"d"`
	Evidence   *OccupancyEvidence   `json:"evidence"`
	Adopted    *bool                `json:"adopted"`
	SchedulerD *SchedulerOccupancy  `json:"schedulerD"`
	ObservedD  *OccupancyEstimate   `json:"observedD"`
}

type SchedulerOccupancy struct {
	Source    string                      `json:"source"`
	Value     int64                       `json:"value"`
	Pairs     int                         `json:"pairs"`
	Window    *OccupancyMeasurementWindow `json:"window"`
	UpdatedAt time.Time                   `json:"updatedAt"`
}

// OccupancyEstimate is the read model for signed occupancy facts, independent
// of the scheduler implementation. Composition roots map retained values here.
type OccupancyEstimate struct {
	Node                    MeasurementIdentity        `json:"node"`
	WorkloadUID             string                     `json:"workloadUID"`
	Model                   string                     `json:"model"`
	PowerMode               string                     `json:"powerMode"`
	ExecutionPath           string                     `json:"executionPath"`
	RuntimeImage            string                     `json:"runtimeImage"`
	Batch                   int                        `json:"batch"`
	Profile                 OccupancyProfile           `json:"profile"`
	Pairs                   int                        `json:"pairs"`
	Batches                 int                        `json:"batches"`
	CompleteBatches         int                        `json:"completeBatches"`
	Requests                int                        `json:"requests"`
	CompleteRequests        int                        `json:"completeRequests"`
	RawRows                 int                        `json:"rawRows"`
	CompleteRawRows         int                        `json:"completeRawRows"`
	EnrichedCompleteRawRows int                        `json:"enrichedCompleteRawRows"`
	FinalRows               int                        `json:"finalRows"`
	CompleteFinalRows       int                        `json:"completeFinalRows"`
	Statistics              *OccupancyStatistics       `json:"statistics"`
	Window                  OccupancyMeasurementWindow `json:"window"`
}

type OccupancyProfile struct {
	ProfileID string                         `json:"profileID"`
	Reference evidencestore.ObjectIdentity   `json:"reference"`
	Evidence  []evidencestore.ObjectIdentity `json:"evidence"`
	MeanNanos float64                        `json:"meanNanos"`
}

type OccupancyStatistics struct {
	Median           float64 `json:"median"`
	P25              float64 `json:"p25"`
	P75              float64 `json:"p75"`
	IQR              float64 `json:"iqr"`
	P10              float64 `json:"p10"`
	P90              float64 `json:"p90"`
	Mean             float64 `json:"mean"`
	NegativeFraction float64 `json:"negativeFraction"`
}
