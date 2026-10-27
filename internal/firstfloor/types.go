// Package firstfloor is the bounded single-runner executor adopted from firstfloor.
// It intentionally has no dependency on CHILL serving, Kubernetes or profiling.
package firstfloor

import (
	"encoding/json"
	"time"
)

const MaxHeaderBytes = 64 << 10
const MaxFrameBytes = 64 << 20
const MaxProgressBytes = 4096
const MaxErrorBytes = 1024

const (
	modeServing   = "serving"
	modeProfiling = "profiling"
	phaseQueued   = "queued"
	phaseDeferred = "deferred"
	phaseRunning  = "running"
	phaseFinished = "finished"
)

const (
	DeadlineDrop        = "drop"
	DeadlineKeep        = "keep"
	DeadlineDefer       = "defer"
	OutcomeDeadlineDrop = "deadline_drop"
	ReasonDeadlineDefer = "deadline_defer"
	// OutcomePreprocessFailed ends one request whose sample the module rejected (HTTP 422).
	OutcomePreprocessFailed = "preprocess_failed"
	// OutcomeBatchError ends every request of a batch whose module result broke the contract.
	OutcomeBatchError = "batch_error"
	outcomeCompleted  = "completed"
)

func EffectiveDeadlinePolicy(value string) string {
	if value == "" {
		return DeadlineKeep
	}
	return value
}

// Field carries one raw field: one element per request for inference and n
// elements for a profile execution. Content checks belong to the gateway.
type Field struct {
	Name string   `json:"name"`
	Data [][]byte `json:"data"`
}
type SLO struct {
	Percentile float64 `json:"percentile"`
	TargetMS   float64 `json:"targetMS"`
}
type RequestContext struct {
	RequestID          string    `json:"requestID"`
	WorkloadUID        string    `json:"workloadUID"`
	Definition         string    `json:"definition"`
	ReceivedAt         time.Time `json:"receivedAt"`
	SenderElapsedMS    float64   `json:"senderElapsedMS"`
	CompletionMarginMS float64   `json:"completionMarginMS"`
	SLO                SLO       `json:"slo"`
	LifetimeMS         float64   `json:"lifetimeMS,omitempty"` // Zero means no explicit lifetime; measured from original receipt.
}
type Request struct {
	ID      string         `json:"id"`
	Context RequestContext `json:"context"`
	Inputs  []Field        `json:"inputs"`
}
type Response struct {
	ModelName         string   `json:"model_name"`
	ID                string   `json:"id"`
	Outputs           []Field  `json:"outputs"`
	Progress          Progress `json:"progress"`
	ExecutionComplete bool     `json:"executionComplete"`
}
type ErrorResponse struct {
	Error             string    `json:"error"`
	ExecutionComplete bool      `json:"executionComplete"`
	Progress          *Progress `json:"progress,omitempty"`
}
type BatchProfile struct {
	Size             int       `json:"size"`
	ServiceSamplesMS []float64 `json:"serviceSamplesMS"`
	EnergyPerItemJ   float64   `json:"energyPerItemJ"`
}
type ProfileBundle struct {
	ProfileID string         `json:"profileID"`
	Batches   []BatchProfile `json:"batches"`
}
type Policy struct {
	DecisionID     string         `json:"decisionID"`
	ProfileID      string         `json:"profileID"`
	MaxBatchWaitMS float64        `json:"maxBatchWaitMS"`
	MaxBatchSize   int            `json:"maxBatchSize,omitempty"`   // Zero permits every initialized measured batch.
	DeadlinePolicy string         `json:"deadlinePolicy,omitempty"` // Frozen for the instance; omitted means keep.
	FixedBatchSize int            `json:"fixedBatchSize,omitempty"` // Frozen for the instance; zero is dynamic.
	Batches        []BatchProfile `json:"-"`                        // Immutable bundle, loaded once. Policy file never repeats samples.
}
type Config struct {
	Model            string   `json:"model"`
	Inputs           []string `json:"inputs"`  // raw input field names
	Outputs          []string `json:"outputs"` // raw output field names
	MaxNewTokens     int      `json:"maxNewTokens,omitempty"`
	RunnerCommand    []string `json:"runnerCommand"`
	RunnerTimeoutMS  int      `json:"runnerTimeoutMS"`
	MaxQueueRequests int      `json:"maxQueueRequests"`
	MaxQueueBytes    int      `json:"maxQueueBytes"`
	MaxRequestBytes  int      `json:"maxRequestBytes"`
	Mode             string   `json:"mode"` // serving or profiling
	BatchSizes       []int    `json:"batchSizes"`
	MaxBatchSize     int      `json:"maxBatchSize"`
	ProfilePath      string   `json:"profilePath,omitempty"`
	Policy           Policy   `json:"policy"`
}
type Progress struct {
	RequestID         string    `json:"requestID"`
	WorkloadUID       string    `json:"workloadUID"`
	Definition        string    `json:"definition"`
	Phase             string    `json:"phase"`
	At                time.Time `json:"at"`
	BatchID           uint64    `json:"batchID,omitempty"`
	BatchSize         int       `json:"batchSize,omitempty"`
	ProfileID         string    `json:"profileID,omitempty"`
	DecisionID        string    `json:"decisionID,omitempty"`
	QueueMS           float64   `json:"queueMS"`
	ServiceMS         float64   `json:"serviceMS"`
	BackendMS         float64   `json:"backendMS"`
	PreprocessMS      float64   `json:"preprocessMS"`
	PostprocessMS     float64   `json:"postprocessMS"`
	FirstTokenMS      float64   `json:"firstTokenMS,omitempty"` // generative paths only
	Units             int       `json:"units,omitempty"`        // successful requests only
	NativeElapsedMS   float64   `json:"nativeElapsedMS"`
	ExecutionComplete bool      `json:"executionComplete"`
	Outcome           string    `json:"outcome,omitempty"`
}
type RuntimeMetadata struct {
	Runtime            string          `json:"runtime"`
	RuntimeVersion     string          `json:"runtimeVersion"`
	NumpyVersion       string          `json:"numpyVersion"`
	ModelFiles         []string        `json:"modelFiles"`
	Inputs             []string        `json:"inputs"`
	Outputs            []string        `json:"outputs"`
	Providers          []string        `json:"providers"`
	Device             string          `json:"device"`
	GPUUUID            string          `json:"gpuUUID,omitempty"`
	CPUAffinity        []int           `json:"cpuAffinity"`
	IntraOpThreads     int             `json:"intraOpThreads"`
	InterOpThreads     int             `json:"interOpThreads"`
	InitializedBatches []int           `json:"initializedBatches"`
	WarmupIterations   int             `json:"warmupIterations"`
	Synthetic          bool            `json:"synthetic"`
	AvailableProviders []string        `json:"availableProviders,omitempty"`
	CgroupCPUMax       string          `json:"cgroupCPUMax,omitempty"`
	CPUSet             string          `json:"cpuSet,omitempty"`
	RKNN               json.RawMessage `json:"rknn,omitempty"`
}
type Metadata struct {
	Model              string          `json:"model"`
	Mode               string          `json:"mode"`
	Runtime            RuntimeMetadata `json:"runtime"`
	ProfileID          string          `json:"profileID"`
	DecisionID         string          `json:"decisionID"`
	AppliedAt          time.Time       `json:"appliedAt"`
	Policy             Policy          `json:"policy"`
	MaxQueueRequests   int             `json:"maxQueueRequests"`
	MaxQueueBytes      int             `json:"maxQueueBytes"`
	MaxRequestBytes    int             `json:"maxRequestBytes"`
	MaxHeaderBytes     int             `json:"maxHeaderBytes"`
	MaxFrameBytes      int             `json:"maxFrameBytes"`
	MaxProgressBytes   int             `json:"maxProgressBytes"`
	MaxErrorBytes      int             `json:"maxErrorBytes"`
	MaxBatchSize       int             `json:"maxBatchSize"`
	ModelInstanceCount int             `json:"modelInstanceCount"`
}

// RequestState contains frozen request context plus local monotonic elapsed spans.
type RequestState struct {
	Context             RequestContext `json:"context"`
	QueueMS             float64        `json:"queueMS"`
	ElapsedMS           float64        `json:"elapsedMS"`
	ArrivalRate         float64        `json:"arrivalRate"`
	Phase               string         `json:"phase"`
	BatchID             uint64         `json:"batchID,omitempty"`
	BatchSize           int            `json:"batchSize,omitempty"`
	ServiceElapsedMS    float64        `json:"serviceElapsedMS"`
	ProfileID           string         `json:"profileID,omitempty"`
	DecisionID          string         `json:"decisionID,omitempty"`
	RemainingLifetimeMS *float64       `json:"remainingLifetimeMS,omitempty"`
}
type State struct {
	At                time.Time            `json:"at"`
	Ready             bool                 `json:"ready"`
	Metadata          Metadata             `json:"metadata"`
	Requests          []RequestState       `json:"requests"`
	AcceptedRequests  uint64               `json:"acceptedRequests"`
	CompletedRequests uint64               `json:"completedRequests"`
	Executions        uint64               `json:"executions"`
	ArrivalAgesMS     map[string][]float64 `json:"arrivalAgesMS"`
}
type BatchChoice struct {
	RequestID   string
	WorkloadUID string
	Definition  string
	Size        int
	WaitMS      float64
	Reason      string
}

// ProfileRequest is exactly one execution of BatchSize samples.
type ProfileRequest struct {
	BatchSize int     `json:"batchSize"`
	Inputs    []Field `json:"inputs"`
}
type ProfileResponse struct {
	BatchSize            int       `json:"batchSize"`
	BatchIDs             []uint64  `json:"batchIDs"`
	NativeSamplesMS      []float64 `json:"nativeSamplesMS"`
	ServiceSamplesMS     []float64 `json:"serviceSamplesMS"`
	BackendSamplesMS     []float64 `json:"backendSamplesMS"`
	PreprocessSamplesMS  []float64 `json:"preprocessSamplesMS"`
	PostprocessSamplesMS []float64 `json:"postprocessSamplesMS"`
	FirstTokenSamplesMS  []float64 `json:"firstTokenSamplesMS,omitempty"` // generative paths only
	Units                []int     `json:"units"`                         // per request of the execution
	NativeElapsedMS      float64   `json:"nativeElapsedMS"`
	ExecutionComplete    bool      `json:"executionComplete"`
}
