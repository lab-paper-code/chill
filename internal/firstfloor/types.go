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
	DeadlineDrop           = "drop"
	DeadlineKeep           = "keep"
	DeadlineDefer          = "defer"
	AdmissionReject        = "Reject"
	AdmissionDelay         = "Delay"
	CodeQueueFull          = "QueueFull"
	CodeDeadlineInfeasible = "DeadlineInfeasible"
	CodeRequestTimeout     = "RequestTimeout"
	AdmissionStageExecutor = "executor"
	OutcomeDeadlineDrop    = "deadline_drop"
	ReasonDeadlineDefer    = "deadline_defer"
	// FailureMemoryAllocation denotes a native allocator rejection, not kernel OOMKilled.
	FailureMemoryAllocation = "MemoryAllocationFailed"
	// OutcomePreprocessFailed ends one request whose sample the module rejected (HTTP 422).
	OutcomePreprocessFailed = "preprocess_failed"
	// OutcomeBatchError ends every request of a failed native execution batch.
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
	AdmissionPolicy    string    `json:"admissionPolicy"`
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

// HTTPRequestTiming records native-local monotonic offsets from handler entry.
// HandlerEnteredAt is a UTC correlation anchor, not a cross-host latency clock.
// ReceivedNanos marks the unchanged NativeElapsedMS origin (after readiness).
// ResponseWriteStartNanos precedes JSON encoding and the first response write;
// it does not measure encoding, socket write completion, or delivery.
type HTTPRequestTiming struct {
	HandlerEnteredAt        time.Time `json:"handlerEnteredAt"`
	ReceivedNanos           *int64    `json:"receivedNanos,omitempty"`
	DecodeStartNanos        *int64    `json:"decodeStartNanos,omitempty"`
	DecodeDoneNanos         *int64    `json:"decodeDoneNanos,omitempty"`
	EnqueueNanos            *int64    `json:"enqueueNanos,omitempty"`
	FinishedNanos           *int64    `json:"finishedNanos,omitempty"`
	ResponseWriteStartNanos *int64    `json:"responseWriteStartNanos,omitempty"`
}

// Merge fills missing boundaries from the same native handler observation.
// Existing observations, including zero offsets, always keep their first value.
// Boundary pointers are immutable once published in progress or a response.
func (t *HTTPRequestTiming) Merge(other HTTPRequestTiming) {
	if !t.HandlerEnteredAt.Equal(other.HandlerEnteredAt) {
		return
	}
	for _, field := range [...]struct {
		target **int64
		source *int64
	}{
		{&t.ReceivedNanos, other.ReceivedNanos},
		{&t.DecodeStartNanos, other.DecodeStartNanos},
		{&t.DecodeDoneNanos, other.DecodeDoneNanos},
		{&t.EnqueueNanos, other.EnqueueNanos},
		{&t.FinishedNanos, other.FinishedNanos},
		{&t.ResponseWriteStartNanos, other.ResponseWriteStartNanos},
	} {
		if *field.target == nil {
			*field.target = field.source
		}
	}
}

type ErrorResponse struct {
	Error             string             `json:"error"`
	FailureReason     string             `json:"failureReason,omitempty"`
	ExecutionComplete bool               `json:"executionComplete"`
	Progress          *Progress          `json:"progress,omitempty"`
	Code              string             `json:"code,omitempty"`
	Stage             string             `json:"stage,omitempty"`
	Policy            string             `json:"policy,omitempty"`
	RemainingSLONanos *int64             `json:"remainingSLONanos,omitempty"`
	ExecutorWaitNanos *int64             `json:"executorWaitNanos,omitempty"`
	NotAdmitted       bool               `json:"notAdmitted,omitempty"`
	NativeHTTP        *HTTPRequestTiming `json:"nativeHTTP,omitempty"`
}
type BatchProfile struct {
	Size           int                  `json:"size"`
	ServiceSamples []ServiceSample      `json:"serviceSamples"`
	Distribution   *ServiceDistribution `json:"-"`
	EnergyPerItemJ float64              `json:"energyPerItemJ"`
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
	DeadlinePolicy string         `json:"deadlinePolicy,omitempty"` // Measurement provenance; omitted means keep. Serving uses each request's admission policy.
	FixedBatchSize int            `json:"fixedBatchSize,omitempty"` // Frozen for the instance; zero is dynamic.
	Batches        []BatchProfile `json:"-"`                        // Immutable bundle, loaded once. Policy file never repeats samples.
}
type Config struct {
	// Execution conditions are frozen once and projected to both serving and measurement.
	RuntimeImage        string            `json:"runtimeImage,omitempty"`
	Backend             string            `json:"backend,omitempty"`
	Artifact            string            `json:"artifact,omitempty"`
	ArtifactImage       string            `json:"artifactImage,omitempty"`
	ArtifactFiles       []string          `json:"artifactFiles,omitempty"`
	Preprocessor        string            `json:"preprocessor,omitempty"`
	PreprocessorImage   string            `json:"preprocessorImage,omitempty"`
	PreprocessorPath    string            `json:"preprocessorPath,omitempty"`
	RuntimePackages     map[string]string `json:"runtimePackages,omitempty"`
	PowerMode           string            `json:"powerMode,omitempty"`
	MemoryRequestBytes  int64             `json:"memoryRequestBytes,omitempty"`
	MemoryLimitBytes    int64             `json:"memoryLimitBytes,omitempty"`
	GPUUUID             string            `json:"gpuUUID,omitempty"`
	AcceleratorResource string            `json:"acceleratorResource,omitempty"`
	WarmupBatchSizes    []int             `json:"warmupBatchSizes,omitempty"`
	WarmupIterations    int               `json:"warmupIterations,omitempty"`
	Model               string            `json:"model"`
	Inputs              []string          `json:"inputs"`  // raw input field names
	Outputs             []string          `json:"outputs"` // raw output field names
	MaxNewTokens        int               `json:"maxNewTokens,omitempty"`
	RunnerCommand       []string          `json:"runnerCommand"`
	RunnerTimeoutMS     int               `json:"runnerTimeoutMS"`
	MaxQueueRequests    int               `json:"maxQueueRequests"`
	MaxQueueBytes       int               `json:"maxQueueBytes"`
	MaxRequestBytes     int               `json:"maxRequestBytes"`
	Mode                string            `json:"mode"` // serving or profiling
	BatchSizes          []int             `json:"batchSizes"`
	MaxBatchSize        int               `json:"maxBatchSize"`
	ProfilePath         string            `json:"profilePath,omitempty"`
	Policy              Policy            `json:"policy"`
}
type Progress struct {
	RequestID         string             `json:"requestID"`
	WorkloadUID       string             `json:"workloadUID"`
	Definition        string             `json:"definition"`
	Phase             string             `json:"phase"`
	At                time.Time          `json:"at"`
	BatchID           uint64             `json:"batchID,omitempty"`
	BatchSize         int                `json:"batchSize,omitempty"`
	ProfileID         string             `json:"profileID,omitempty"`
	DecisionID        string             `json:"decisionID,omitempty"`
	QueueMS           float64            `json:"queueMS"`
	ExecutorWaitNanos *int64             `json:"executorWaitNanos,omitempty"`
	ServiceMS         float64            `json:"serviceMS"`
	BackendMS         float64            `json:"backendMS"`
	PreprocessMS      float64            `json:"preprocessMS"`
	PostprocessMS     float64            `json:"postprocessMS"`
	FirstTokenMS      float64            `json:"firstTokenMS,omitempty"` // generative paths only
	Units             int                `json:"units,omitempty"`        // successful requests only
	NativeElapsedMS   float64            `json:"nativeElapsedMS"`
	ExecutionComplete bool               `json:"executionComplete"`
	Outcome           string             `json:"outcome,omitempty"`
	ServiceSample     *ServiceSample     `json:"serviceSample,omitempty"`
	NativeHTTP        *HTTPRequestTiming `json:"nativeHTTP,omitempty"`
	NativeDeferredAt  *time.Time         `json:"nativeDeferredAt,omitempty"`
}
type RuntimeMetadata struct {
	Runtime        string   `json:"runtime"`
	RuntimeVersion string   `json:"runtimeVersion"`
	NumpyVersion   string   `json:"numpyVersion"`
	ModelFiles     []string `json:"modelFiles"`
	Inputs         []string `json:"inputs"`
	Outputs        []string `json:"outputs"`
	Providers      []string `json:"providers"`
	Device         string   `json:"device"`
	GPUUUID        string   `json:"gpuUUID,omitempty"`
	CPUAffinity    []int    `json:"cpuAffinity"`
	// Applied ORT session settings; zero means runtime automatic/default, not one thread.
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
	// Actual IPC-boundary fields. Both are nil until the exchange begins;
	// a nil preceding idle with nonnil elapsed identifies a startup exchange.
	PrecedingIdleNanos   *int64 `json:"precedingIdleNanos"`
	ExchangeElapsedNanos *int64 `json:"exchangeElapsedNanos"`
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
	// Decision-age approximation before packing/goroutine startup, not G_actual.
	LastServiceEndAgeNanos  *int64 `json:"lastServiceEndAgeNanos"`
	DecisionIdleApproximate bool   `json:"decisionIdleApproximate"`
}
type BatchChoice struct {
	RequestID               string
	WorkloadUID             string
	Definition              string
	Size                    int
	WaitMS                  float64
	Reason                  string
	DecisionIdleNanos       *int64
	DecisionIdleApproximate bool
}

// ProfileRequest is exactly one execution of BatchSize samples.
type ProfileRequest struct {
	BatchSize int     `json:"batchSize"`
	Inputs    []Field `json:"inputs"`
	TrialID   string  `json:"trialID"`
}
type ProfileResponse struct {
	BatchSize            int             `json:"batchSize"`
	BatchIDs             []uint64        `json:"batchIDs"`
	NativeSamplesMS      []float64       `json:"nativeSamplesMS"`
	ServiceSamples       []ServiceSample `json:"serviceSamples"`
	BackendSamplesMS     []float64       `json:"backendSamplesMS"`
	PreprocessSamplesMS  []float64       `json:"preprocessSamplesMS"`
	PostprocessSamplesMS []float64       `json:"postprocessSamplesMS"`
	FirstTokenSamplesMS  []float64       `json:"firstTokenSamplesMS,omitempty"` // generative paths only
	Units                []int           `json:"units"`                         // per request of the execution
	NativeElapsedMS      float64         `json:"nativeElapsedMS"`
	ExecutionComplete    bool            `json:"executionComplete"`
}
