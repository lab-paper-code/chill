package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

const (
	phaseAssigned                = "assigned"
	phaseRunning                 = "running"
	phaseFinished                = "finished"
	phaseReceived                = "received"
	phaseAccepted                = "accepted"
	phaseQueued                  = "queued"
	phaseDeferred                = "deferred"
	phasePending                 = "pending"
	phaseReturning               = "returning"
	eventResponseFinished        = "response_finished"
	eventBackendExchangeFinished = "backend_exchange_finished"
	outcomeCompleted             = "completed"
	outcomeIncomplete            = "incomplete"
	reasonReady                  = "Ready"
	reasonShuttingDown           = "ShuttingDown"
	reasonGatewayOverloaded      = "GatewayOverloaded"
)

// Overload admission codes and stages are shared by gateway and executor paths.
const (
	CodeQueueFull          = "QueueFull"
	CodeDeadlineInfeasible = "DeadlineInfeasible"
	AdmissionUnknown       = "AdmissionUnknown"
	AdmissionStageGateway  = "gateway"
	AdmissionStageExecutor = "executor"
)

type AdmissionPolicy string

const (
	AdmissionPolicyReject AdmissionPolicy = "Reject"
	AdmissionPolicyDelay  AdmissionPolicy = "Delay"
)

type Admission struct {
	Policy            AdmissionPolicy `json:"policy"`
	MaxQueuedRequests int32           `json:"maxQueuedRequests"`
}

const DefinitionAdmissionInvalid = "DefinitionAdmissionInvalid"

// Error carries a consumer-visible failure without depending on its transport.
type Error struct {
	Code                     string          `json:"code"`
	Message                  string          `json:"message"`
	Field                    string          `json:"field,omitempty"`
	Stage                    string          `json:"stage,omitempty"`
	Policy                   AdmissionPolicy `json:"policy,omitempty"`
	RemainingSLONanos        *int64          `json:"remainingSLONanos,omitempty"`
	PredictedCompletionNanos *int64          `json:"predictedCompletionNanos,omitempty"`
	ExecutorWaitNanos        *int64          `json:"executorWaitNanos,omitempty"`
	OutsideSupport           bool            `json:"outsideSupport,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// AdmissionRejection is the public overload decision, without transport details.
type AdmissionRejection struct {
	Code                     string          `json:"code"`
	Stage                    string          `json:"stage"`
	Policy                   AdmissionPolicy `json:"policy"`
	PredictedCompletionNanos *int64          `json:"predictedCompletionNanos,omitempty"`
	RemainingSLONanos        *int64          `json:"remainingSLONanos,omitempty"`
	OutsideSupport           bool            `json:"outsideSupport,omitempty"`
}

func failure(code, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

type SLO struct {
	Percentile    float64       `json:"percentile"`
	TargetLatency time.Duration `json:"targetLatencyNanos"`
}

type ModelPath struct {
	Name          string                          `json:"name"`
	Artifact      string                          `json:"artifact"`
	Format        string                          `json:"format"`
	Image         string                          `json:"image"`
	Architecture  string                          `json:"architecture"`
	Files         []string                        `json:"files"`
	RuntimeFamily string                          `json:"runtimeFamily"`
	Backend       string                          `json:"backend"`
	Firstfloor    *modelcompat.FirstfloorSettings `json:"firstfloor,omitempty"`
	// Preprocessor is the preprocessing code carrier for this path's artifact.
	Preprocessor PreprocessorArtifact `json:"preprocessor"`
}

type PreprocessorArtifact struct {
	Image        string `json:"image"`
	Architecture string `json:"architecture"`
	Path         string `json:"path"`
}

type Model struct {
	Name       string      `json:"name"`
	UID        string      `json:"uid"`
	Generation int64       `json:"generation"`
	Paths      []ModelPath `json:"paths"`
}

// RawField is one field of a raw sample or raw response. MaxBytes bounds input fields only.
type RawField struct {
	Name     string `json:"name"`
	Format   string `json:"format"`
	MaxBytes int32  `json:"maxBytes,omitempty"`
}

// Preprocessor is the applied raw contract. MaxNewTokens is 0 for non-generative preprocessing.
type Preprocessor struct {
	Name         string     `json:"name"`
	UID          string     `json:"uid"`
	Generation   int64      `json:"generation"`
	Input        []RawField `json:"input"`
	Output       []RawField `json:"output"`
	MaxNewTokens int32      `json:"maxNewTokens,omitempty"`
}

type DatasetReference struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

// Definition is immutable after the management boundary publishes it. Requests
// share it instead of copying the model declaration for every queue hop.
type Definition struct {
	Name         string       `json:"name"`
	UID          string       `json:"uid"`
	Generation   int64        `json:"generation"`
	Model        Model        `json:"model"`
	Preprocessor Preprocessor `json:"preprocessor"`
	// Dataset is absent until the Workload names one; admission waits with MissingDataset.
	Dataset         *DatasetReference `json:"dataset,omitempty"`
	SLO             SLO               `json:"slo"`
	Admission       Admission         `json:"admission"`
	RequestLifetime *time.Duration    `json:"requestLifetime,omitempty"`
}

// Identity names the accepted model and preprocessing definition. SLO, dataset,
// lifetime and admission changes advance the Workload generation.
func (d *Definition) Identity() string {
	return fmt.Sprintf("%s/%d/%s/%d/%s/%d", d.UID, d.Generation, d.Model.UID, d.Model.Generation, d.Preprocessor.UID, d.Preprocessor.Generation)
}

func (d *Definition) validateAdmission() error {
	if (d.Admission.Policy != AdmissionPolicyReject && d.Admission.Policy != AdmissionPolicyDelay) || d.Admission.MaxQueuedRequests < 1 {
		return failure(DefinitionAdmissionInvalid, "workload %q (%s), definition %q has invalid admission: policy=%q maxQueuedRequests=%d", d.Name, d.UID, d.Identity(), d.Admission.Policy, d.Admission.MaxQueuedRequests)
	}
	return nil
}

// SameExecution reports whether both definitions run the same model and
// preprocessing, so replacing one with the other needs no drain.
func (d *Definition) SameExecution(o *Definition) bool {
	return d.Model.UID == o.Model.UID && d.Model.Generation == o.Model.Generation &&
		d.Preprocessor.UID == o.Preprocessor.UID && d.Preprocessor.Generation == o.Preprocessor.Generation
}

type BatchProfile struct {
	Batch                 int                             `json:"batch"`
	ServiceSamples        []firstfloor.ServiceSample      `json:"serviceSamples"`
	ServiceDistribution   *firstfloor.ServiceDistribution `json:"-"`
	EnergyPerInputJoules  float64                         `json:"energyPerInputJoules"`
	EnergyBelowResolution bool                            `json:"energyBelowResolution,omitempty"`
	MemoryBytes           uint64                          `json:"memoryBytes"`
	// MemoryBasis is transient derivation lineage; retained observation profile
	// bodies keep their existing stored contract. Catalog reads expose it.
	MemoryBasis   profilederivation.MemoryBasis `json:"-"`
	MeanWallWatts float64                       `json:"meanWallWatts,omitempty"`
}

// Profile retains the frozen measurement condition and its raw evidence. It is
// built once by the evidence boundary, not decoded or revalidated per request.
type Profile struct {
	ID              string                          `json:"id"`
	Reference       evidencestore.ObjectIdentity    `json:"reference"`
	Evidence        []evidencestore.ObjectIdentity  `json:"evidence"`
	NodeName        string                          `json:"nodeName"`
	Condition       profilederivation.CurvePlanSpec `json:"condition"`
	Batches         []BatchProfile                  `json:"batches"`
	ConfidenceLevel float64                         `json:"confidenceLevel"`
	TrialsPerBatch  int                             `json:"trialsPerBatch,omitempty"`
	// IdleBaselineWatts is the exact baseline subtracted by the incremental
	// estimator. Whole-node prediction adds it back according to its scope.
	IdleBaselineWatts float64                       `json:"idleBaselineWatts"`
	MeasuredAt        time.Time                     `json:"measuredAt"`
	RuntimeMetadata   *firstfloor.RuntimeMetadata   `json:"runtimeMetadata,omitempty"`
	MetricSource      metricobserver.SourceIdentity `json:"metricSource"`
	// RuntimeDelivery is the JSON delivery of the measured RunConfig, read from
	// the same verified raw evidence. Preparation projects RKNNLite host access
	// and the runner's delivery file from it; views and observation logs omit it.
	RuntimeDelivery json.RawMessage    `json:"-"`
	Reuse           *ProfileReuseCheck `json:"reuse,omitempty"`
}

// ProfileReuseCheck retains the comparison without merging either raw population.
type ProfileReuseCheck struct {
	SourceProfileID             string                        `json:"sourceProfileID"`
	ConfirmationProfileID       string                        `json:"confirmationProfileID"`
	SourceReference             evidencestore.ObjectIdentity  `json:"sourceReference"`
	ConfirmationReference       evidencestore.ObjectIdentity  `json:"confirmationReference"`
	SourceNode                  NodeReference                 `json:"sourceNode"`
	TargetNode                  NodeReference                 `json:"targetNode"`
	MaximumRelativeDifference   float64                       `json:"maximumRelativeDifference"`
	MinimumMeasurementSeconds   float64                       `json:"minimumMeasurementSeconds"`
	MinimumTrials               int                           `json:"minimumTrials"`
	ConfirmationActiveSeconds   int                           `json:"confirmationActiveSeconds"`
	ConfirmationIdleSeconds     int                           `json:"confirmationIdleSeconds"`
	ConfirmationTrialsPerBatch  int                           `json:"confirmationTrialsPerBatch"`
	SourceIdleWatts             float64                       `json:"sourceIdleWatts"`
	TargetIdleWatts             float64                       `json:"targetIdleWatts"`
	IdlePowerRelativeDifference *float64                      `json:"idlePowerRelativeDifference,omitempty"`
	Batches                     []ProfileReuseBatchCheck      `json:"batches,omitempty"`
	Outcome                     string                        `json:"outcome"`
	Reason                      string                        `json:"reason,omitempty"`
	Record                      *evidencestore.ObjectIdentity `json:"record,omitempty"`
}

type ProfileReuseBatchCheck struct {
	Batch                     int     `json:"batch"`
	SourceMeanServiceNanos    float64 `json:"sourceMeanServiceNanos"`
	TargetMeanServiceNanos    float64 `json:"targetMeanServiceNanos"`
	ServiceRelativeDifference float64 `json:"serviceRelativeDifference"`
	SourceMeanWallWatts       float64 `json:"sourceMeanWallWatts"`
	TargetMeanWallWatts       float64 `json:"targetMeanWallWatts"`
	PowerRelativeDifference   float64 `json:"powerRelativeDifference"`
}

// NativeBundle projects this catalog-verified profile for immutable startup
// delivery. Both the publisher and consumer derive identity from this producer.
// MeanNanos is the mean measured execution time of batch b.
func (p *Profile) MeanNanos(b int) (float64, bool) {
	for _, batch := range p.Batches {
		if batch.Batch == b && len(batch.ServiceSamples) > 0 {
			sum := 0.0
			for _, sample := range batch.ServiceSamples {
				sum += float64(sample.ServiceDurationNanos)
			}
			return sum / float64(len(batch.ServiceSamples)), true
		}
	}
	return 0, false
}

func (p *Profile) NativeBundle() firstfloor.ProfileBundle {
	bundle := firstfloor.ProfileBundle{ProfileID: p.ID, Batches: make([]firstfloor.BatchProfile, len(p.Batches))}
	for i, batch := range p.Batches {
		bundle.Batches[i] = firstfloor.BatchProfile{
			Size: batch.Batch, ServiceSamples: batch.ServiceSamples, Distribution: batch.ServiceDistribution,
			EnergyPerItemJ: batch.EnergyPerInputJoules,
		}
	}
	return bundle
}

type NodeReference struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type WorkloadReference struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}

type Assignment struct {
	Node          NodeReference `json:"node"`
	Instance      string        `json:"instance"`
	Artifact      string        `json:"artifact"`
	ExecutionPath string        `json:"executionPath"`
	Profile       string        `json:"profile"`
	DispatchedAt  time.Time     `json:"dispatchedAt"`
	// ProfileBody is the catalog body in memory; observation rows keep Profile.
	ProfileBody *Profile `json:"-"`
}

type InstanceState struct {
	ID                  string              `json:"id"`
	PodUID              string              `json:"podUID,omitempty"`
	Allocation          SchedulerAllocation `json:"allocation"`
	AttemptedAt         time.Time           `json:"attemptedAt"`
	TargetID            string              `json:"targetID,omitempty"`
	OwnerWorkloadUID    string              `json:"ownerWorkloadUID,omitempty"`
	Node                NodeReference       `json:"node"`
	Profile             string              `json:"profile"`
	ActualImageID       string              `json:"actualImageID,omitempty"`
	DesiredRuntimeImage string              `json:"desiredRuntimeImage,omitempty"`
	ReplacementReason   string              `json:"replacementReason,omitempty"`
	Ready               bool                `json:"ready"`
	Stopped             bool                `json:"stopped"`
	// Terminated is exact container termination, not logical admission-stop.
	// It remains true for this physical ID until actual Pod retirement.
	Terminated     bool                           `json:"terminated,omitempty"`
	StartupFailure *PreparationFailureObservation `json:"startupFailure,omitempty"`
	Routable       map[string]bool                `json:"routable"`
	Reason         string                         `json:"reason,omitempty"`
	Workloads      []WorkloadReference            `json:"workloads"`
	Waiting        map[string]int                 `json:"waiting"`
	Running        map[string]int                 `json:"running"`
	// AssignedUnknown has been dispatched but lacks a native phase observation.
	// It is not reported as actual backend waiting or running.
	AssignedUnknown map[string]int    `json:"assignedUnknown"`
	ObservedAt      time.Time         `json:"observedAt"`
	Native          *firstfloor.State `json:"native,omitempty"`
	// NativeSpanAt is the local anchor of Native's derived spans after owned
	// completion reconciliation. Zero means the unchanged probe's ObservedAt.
	NativeSpanAt time.Time `json:"nativeSpanAt,omitzero"`
	// A gateway completion bounds the last service end rather than inventing
	// its timestamp. Native.LastServiceEndAgeNanos is the lower idle age.
	LastServiceEndAgeUpperNanos *int64 `json:"lastServiceEndAgeUpperNanos,omitempty"`
	AdmissionIdleUnknown        bool   `json:"admissionIdleUnknown,omitempty"`
	// NativeVerified belongs to this publication's successful exact-identity
	// physical probe, independently of catalog admission for dispatch.
	// Retained Native after a failed read is not fresh drain evidence.
	NativeVerified  bool `json:"-"`
	RecoveryUnknown bool `json:"recoveryUnknown,omitempty"`
	// CreatedThisRun is the gateway's exact successful Pod-create proof,
	// never a conclusion drawn from native status or readiness.
	CreatedThisRun bool `json:"-"`
	// NeverStarted is a fresh PodAwaitingExecutor observation, not termination.
	NeverStarted bool `json:"-"`
}

// PoolsState derives POOL membership from actual instances. A Workload's
// active POOL is the Nodes of instances it owns or is bound to.
type PoolsState struct {
	Workloads   []WorkloadPool    `json:"workloads"`
	Measurement []MeasurementPool `json:"measurement"`
	Available   []PoolNode        `json:"available"`
	Other       []PoolNode        `json:"other"`
	ObservedAt  time.Time         `json:"observedAt"`
}

type WorkloadPool struct {
	Workload  WorkloadReference `json:"workload"`
	Nodes     []NodeReference   `json:"nodes"`
	Instances []InstanceState   `json:"instances"`
}

type MeasurementPool struct {
	Node      NodeReference   `json:"node"`
	Instances []InstanceState `json:"instances"`
}

type PoolNode struct {
	Node      NodeReference `json:"node"`
	Ready     bool          `json:"ready"`
	PowerMode string        `json:"powerMode"`
	Reasons   []string      `json:"reasons"`
}

type WorkloadState struct {
	Applied    *Definition `json:"appliedDefinition"`
	Admission  bool        `json:"admission"`
	Reason     string      `json:"reason,omitempty"`
	Pending    int         `json:"pending"`
	Assigned   int         `json:"assigned"`
	ObservedAt time.Time   `json:"observedAt"`
}

// RequestEvent records facts, including responses that failed to reach their
// caller. An absent assignment or completion is never synthesized.
type RequestEvent struct {
	RequestID           string               `json:"requestID"`
	WorkloadName        string               `json:"workloadName"`
	WorkloadUID         string               `json:"workloadUID,omitempty"`
	Definition          string               `json:"definition,omitempty"`
	Kind                string               `json:"kind"`
	At                  time.Time            `json:"at"`
	ReceivedAt          time.Time            `json:"receivedAt"`
	ElapsedNanos        int64                `json:"elapsedNanos"`
	ServerTiming        RequestTimings       `json:"serverTiming,omitempty"`
	Assignment          *Assignment          `json:"assignment,omitempty"`
	NativeInstance      string               `json:"nativeInstance,omitempty"`
	NativeProgress      *BackendProgress     `json:"nativeProgress,omitempty"`
	ClientSpanNanos     int64                `json:"clientSpanNanos,omitempty"`
	ExecutionComplete   bool                 `json:"executionComplete"`
	LifetimeNanos       int64                `json:"lifetimeNanos,omitempty"`
	CompletionMarginMS  *float64             `json:"completionMarginMS,omitempty"`
	SLO                 *SLO                 `json:"slo,omitempty"`
	Outcome             string               `json:"outcome,omitempty"`
	Error               string               `json:"error,omitempty"`
	Policy              AdmissionPolicy      `json:"policy,omitempty"`
	Rejection           *Error               `json:"rejection,omitempty"`
	GatewayWaitNanos    *int64               `json:"gatewayWaitNanos,omitempty"`
	AdmissionPrediction *AdmissionPrediction `json:"admissionPrediction,omitempty"`
	AdmissionWaits      []AdmissionWait      `json:"admissionWaits,omitempty"`
	DeferredNanos       *int64               `json:"deferredNanos,omitempty"`
}

type ManagementEvent struct {
	WorkloadUID   string      `json:"workloadUID"`
	Kind          string      `json:"kind"`
	Phase         string      `json:"phase"`
	ActorUsername string      `json:"actorUsername,omitempty"`
	ActorUID      string      `json:"actorUID,omitempty"`
	Before        *Definition `json:"before,omitempty"`
	After         *Definition `json:"after,omitempty"`
	Target        *Definition `json:"target,omitempty"`
	At            time.Time   `json:"at"`
	Error         string      `json:"error,omitempty"`
	// Capacity is the built row of a capacity-row-built event.
	Capacity *CapacityRow `json:"capacity,omitempty"`
}

// Dispatch retains the original accepted context. Client cancellation can
// withdraw queued native work; physical drain requires actual completion proof.
type Dispatch struct {
	RequestID  string
	Definition *Definition
	Input      *InferenceInput
	Assignment Assignment
	ReceivedAt time.Time
	Elapsed    time.Duration
	Deadline   time.Time
	// CompletionMarginMS is the POST reserve frozen when this call was accepted.
	CompletionMarginMS float64
}

type BackendResult struct {
	Body        []byte
	ContentType string
	StatusCode  int
	// Finished proves either no submission or a completed synchronous response.
	// A transport failure cannot prove that execution stopped.
	Finished     bool
	Progress     *BackendProgress
	ClientSpan   time.Duration
	ServerTiming RequestTimings
}

// ReadyInstance is constructed by the Kubernetes and native-runtime boundary.
// The runtime never turns a desired Pod or a static address into readiness.
type ReadyInstance struct {
	State            InstanceState
	Profile          *Profile
	Routing          map[string]int32 // last applied weight per Workload UID, at most one
	Capacity         int
	OwnerWorkloadUID string
	Execute          func(context.Context, Dispatch) (BackendResult, error)
}

type BackendProgress struct {
	RequestID         string                        `json:"requestID"`
	WorkloadUID       string                        `json:"workloadUID"`
	Definition        string                        `json:"definition"`
	Phase             string                        `json:"phase"`
	At                time.Time                     `json:"at"`
	Outcome           string                        `json:"outcome,omitempty"`
	Error             string                        `json:"error,omitempty"`
	BatchID           uint64                        `json:"batchID,omitempty"`
	BatchSize         int                           `json:"batchSize,omitempty"`
	ProfileID         string                        `json:"profileID,omitempty"`
	DecisionID        string                        `json:"decisionID,omitempty"`
	QueueMS           float64                       `json:"queueMS"`
	ExecutorWaitNanos *int64                        `json:"executorWaitNanos,omitempty"`
	ServiceMS         float64                       `json:"serviceMS"`
	BackendMS         float64                       `json:"backendMS"`
	PreprocessMS      float64                       `json:"preprocessMS"`
	PostprocessMS     float64                       `json:"postprocessMS"`
	FirstTokenMS      float64                       `json:"firstTokenMS,omitempty"`
	Units             int                           `json:"units,omitempty"`
	NativeElapsedMS   float64                       `json:"nativeElapsedMS"`
	ExecutionComplete bool                          `json:"executionComplete"`
	ServiceSample     *firstfloor.ServiceSample     `json:"serviceSample,omitempty"`
	NativeHTTP        *firstfloor.HTTPRequestTiming `json:"nativeHTTP,omitempty"`
	NativeDeferredAt  *time.Time                    `json:"nativeDeferredAt,omitempty"`
}
