package operatorconsole

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

const (
	MeasurementComplete            = "complete"
	MeasurementCriteriaMet         = "criteria_met"
	MeasurementPartial             = "partial"
	MeasurementWarning             = "warning"
	MeasurementFailed              = "failed"
	MeasurementRunning             = "running"
	MeasurementUnmeasured          = "unmeasured"
	MeasurementUnsupported         = "unsupported"
	MeasurementUnknown             = "unknown"
	MeasurementUsable              = "usable"
	MeasurementReview              = "review"
	MeasurementProblem             = "problem"
	MeasurementInsufficient        = "insufficient"
	ResultRowConfirmation          = "confirmation"
	ResultRowSourceReference       = "source-reference"
	MeasurementSupportCompatible   = "Compatible"
	MeasurementSupportIncompatible = "Incompatible"
	MeasurementSupportUnknown      = "Unknown"
)

type MeasurementIdentity struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}
type MeasurementBatchProgress struct {
	Basis       string `json:"basis"`
	Target      []int  `json:"target"`
	Measured    []int  `json:"measured"`
	CriteriaMet []int  `json:"criteriaMet"`
	Missing     []int  `json:"missing"`
	InProgress  []int  `json:"inProgress"`
	Unknown     []int  `json:"unknown"`
}
type MeasurementConfidence struct {
	Level     float64 `json:"level"`
	Lower     float64 `json:"lower"`
	Upper     float64 `json:"upper"`
	HalfWidth float64 `json:"halfWidth"`
}
type ResultRow struct {
	ID                       string                 `json:"id"`
	RowKind                  string                 `json:"rowKind"`
	ProfileID                string                 `json:"profileId"`
	SourceProfileID          *string                `json:"sourceProfileId"`
	Batch                    int                    `json:"batch"`
	Condition                any                    `json:"condition"`
	P95Milliseconds          *float64               `json:"p95Milliseconds"`
	LatencySampleCount       *int                   `json:"latencySampleCount"`
	IdleWatts                *float64               `json:"idleWatts"`
	ActiveWatts              *float64               `json:"activeWatts"`
	TotalJoulesPerItem       *float64               `json:"totalJoulesPerItem"`
	IncrementalJoulesPerItem *float64               `json:"incrementalJoulesPerItem"`
	Confidence               *MeasurementConfidence `json:"confidence"`
	ActiveTrialCount         *int                   `json:"activeTrialCount"`
	IdleTrialCount           *int                   `json:"idleTrialCount"`
	CompletedItems           *int                   `json:"completedItems"`
	CompletedCalls           *int                   `json:"completedCalls"`
	PowerSource              any                    `json:"powerSource"`
	Assessment               MeasurementAssessment  `json:"assessment"`
	Sources                  []string               `json:"sources"`
	MissingReasons           []string               `json:"missingReasons"`
	Reuse                    any                    `json:"reuse"`
}
type MeasurementCoverageVariant struct {
	ID            string                   `json:"id"`
	Condition     any                      `json:"condition"`
	Assessment    MeasurementAssessment    `json:"assessment"`
	BatchProgress MeasurementBatchProgress `json:"batchProgress"`
	ResultRows    []ResultRow              `json:"resultRows"`
	RequestIDs    []string                 `json:"requestIds"`
	RunIDs        []string                 `json:"runIds"`
	ProfileIDs    []string                 `json:"profileIds"`
}
type MeasurementSupport struct {
	Verdict string                     `json:"verdict"`
	Node    *MeasurementIdentity       `json:"node"`
	Reasons []ModelCompatibilityReason `json:"reasons"`
}
type MeasurementRepresentative struct {
	VariantID                string   `json:"variantId"`
	Batch                    int      `json:"batch"`
	IncrementalJoulesPerItem *float64 `json:"incrementalJoulesPerItem"`
	P95Milliseconds          *float64 `json:"p95Milliseconds"`
}
type MeasurementFailure struct {
	RequestID    string `json:"requestId"`
	RunID        string `json:"runId"`
	RunNamespace string `json:"runNamespace"`
	RunName      string `json:"runName"`
	Batch        *int   `json:"batch"`
	Code         string `json:"code"`
	Label        string `json:"label"`
	ExitCode     *int   `json:"exitCode"`
	Message      string `json:"message"`
	Relation     string `json:"relation"`
}
type MeasurementHistory struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
}
type MeasurementCoverageCell struct {
	ID             string                       `json:"id"`
	Model          MeasurementIdentity          `json:"model"`
	DeviceClass    MeasurementIdentity          `json:"deviceClass"`
	ExecutionPath  string                       `json:"executionPath"`
	PowerMode      string                       `json:"powerMode"`
	Support        MeasurementSupport           `json:"support"`
	Assessment     MeasurementAssessment        `json:"assessment"`
	VariantCount   int                          `json:"variantCount"`
	Variants       []MeasurementCoverageVariant `json:"variants"`
	BatchProgress  *MeasurementBatchProgress    `json:"batchProgress"`
	Representative *MeasurementRepresentative   `json:"representative"`
	FailureSummary []MeasurementFailure         `json:"failureSummary"`
	History        []MeasurementHistory         `json:"history"`
}
type MeasurementCoverageSourceError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
type MeasurementCoverageSource struct {
	Kind             string                          `json:"kind"`
	ResourceVersions []string                        `json:"resourceVersions"`
	Complete         bool                            `json:"complete"`
	Error            *MeasurementCoverageSourceError `json:"error"`
}
type MeasurementCoverageScope struct {
	Namespace     string `json:"namespace"`
	SupportBasis  string `json:"supportBasis"`
	GoalBasis     string `json:"goalBasis"`
	TargetBatches []int  `json:"targetBatches"`
}
type MeasurementCoverageSummary struct {
	SupportedCellCount           *int            `json:"supportedCellCount"`
	CompleteCellCount            *int            `json:"completeCellCount"`
	CriteriaMetCellCount         *int            `json:"criteriaMetCellCount"`
	ReusedCellCount              *int            `json:"reusedCellCount"`
	UnknownSupportCount          *int            `json:"unknownSupportCount"`
	UnsupportedCellCount         *int            `json:"unsupportedCellCount"`
	CountsByDisplayStatus        map[string]*int `json:"countsByDisplayStatus"`
	RequestCount                 *int            `json:"requestCount"`
	RunCount                     *int            `json:"runCount"`
	ProfileCount                 *int            `json:"profileCount"`
	FailedCellCount              *int            `json:"failedCellCount"`
	CurrentFailedRequestCount    *int            `json:"currentFailedRequestCount"`
	HistoricalFailedRequestCount *int            `json:"historicalFailedRequestCount"`
}
type MeasurementCoverageGroup struct {
	Model          MeasurementIdentity `json:"model"`
	DeviceClass    MeasurementIdentity `json:"deviceClass"`
	CellIDs        []string            `json:"cellIds"`
	Complete       int                 `json:"complete"`
	Supported      *int                `json:"supported"`
	UnknownSupport int                 `json:"unknownSupport"`
}
type MeasurementEntity struct {
	UID            string               `json:"uid"`
	ID             string               `json:"id"`
	Status         string               `json:"status"`
	Label          string               `json:"label"`
	CellIDs        []string             `json:"cellIds"`
	Failed         bool                 `json:"-"`
	FailureSummary []MeasurementFailure `json:"failureSummary,omitempty"`
}
type MeasurementRunEntity struct {
	UID     string   `json:"uid"`
	Status  string   `json:"status"`
	Label   string   `json:"label"`
	CellIDs []string `json:"cellIds"`
}
type MeasurementCoverageEntities struct {
	Requests []MeasurementEntity    `json:"requests"`
	Profiles []MeasurementEntity    `json:"profiles"`
	Runs     []MeasurementRunEntity `json:"runs"`
}
type MeasurementNextAction struct {
	Kind       string  `json:"kind"`
	CellID     string  `json:"cellId"`
	VariantID  *string `json:"variantId"`
	EntityID   string  `json:"entityId"`
	ReasonCode string  `json:"reasonCode"`
	Message    string  `json:"message"`
}
type MeasurementCoverageResponse struct {
	SchemaVersion     string                      `json:"schemaVersion"`
	SnapshotID        string                      `json:"snapshotId"`
	AssessmentVersion string                      `json:"assessmentVersion"`
	DataAsOf          string                      `json:"dataAsOf"`
	Freshness         string                      `json:"freshness"`
	Completeness      string                      `json:"completeness"`
	Scope             MeasurementCoverageScope    `json:"scope"`
	Sources           []MeasurementCoverageSource `json:"sources"`
	Summary           MeasurementCoverageSummary  `json:"summary"`
	Groups            []MeasurementCoverageGroup  `json:"groups"`
	Cells             []MeasurementCoverageCell   `json:"cells"`
	Entities          MeasurementCoverageEntities `json:"entities"`
	NextActions       []MeasurementNextAction     `json:"nextActions"`
}

// MeasurementVariantID uses frozen condition facts, not request names or batch
// goals. Keeping this shared lets preflight invalidate changed conditions.
func MeasurementVariantID(condition any) string {
	raw, _ := json.Marshal(condition)
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) == nil {
		delete(fields, "name")
		delete(fields, "plannedBatchSizes")
		raw, _ = json.Marshal(fields)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
