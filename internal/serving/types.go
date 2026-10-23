package serving

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	reasonShuttingDown           = "ShuttingDown"
	reasonGatewayOverloaded      = "GatewayOverloaded"
)

// Error carries a consumer-visible failure without depending on its transport.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func (e *Error) Error() string { return e.Message }

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
	Dataset *DatasetReference `json:"dataset,omitempty"`
	SLO     SLO               `json:"slo"`
}

// Identity names the accepted model and preprocessing definition. SLO and
// dataset changes advance the Workload generation.
func (d *Definition) Identity() string {
	return fmt.Sprintf("%s/%d/%s/%d/%s/%d", d.UID, d.Generation, d.Model.UID, d.Model.Generation, d.Preprocessor.UID, d.Preprocessor.Generation)
}

// SameExecution reports whether both definitions run the same model and
// preprocessing, so replacing one with the other needs no drain.
func (d *Definition) SameExecution(o *Definition) bool {
	return d.Model.UID == o.Model.UID && d.Model.Generation == o.Model.Generation &&
		d.Preprocessor.UID == o.Preprocessor.UID && d.Preprocessor.Generation == o.Preprocessor.Generation
}

type BatchProfile struct {
	Batch                int       `json:"batch"`
	DurationNanos        []float64 `json:"durationNanos"`
	EnergyPerInputJoules float64   `json:"energyPerInputJoules"`
	// EnergyPerInputHalfWidthJoules is the confidence half-width of
	// EnergyPerInputJoules at the profile's ConfidenceLevel (Student t,
	// Welch–Satterthwaite with the idle baseline variance).
	EnergyPerInputHalfWidthJoules float64 `json:"energyPerInputHalfWidthJoules,omitempty"`
	MemoryBytes                   uint64  `json:"memoryBytes"`
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
	// IdleBaselineWatts is the exact baseline subtracted by the incremental
	// estimator. Whole-node prediction adds it back according to its scope.
	IdleBaselineWatts float64 `json:"idleBaselineWatts"`
	// IdleBaselineHalfWidthWatts is its confidence half-width at ConfidenceLevel
	// (Student t over the model-loaded idle trials).
	IdleBaselineHalfWidthWatts float64                       `json:"idleBaselineHalfWidthWatts,omitempty"`
	MeasuredAt                 time.Time                     `json:"measuredAt"`
	RuntimeMetadata            *firstfloor.RuntimeMetadata   `json:"runtimeMetadata,omitempty"`
	MetricSource               metricobserver.SourceIdentity `json:"metricSource"`
	// RuntimeDelivery is the JSON delivery of the measured RunConfig, read from
	// the same verified raw evidence. Preparation projects RKNNLite host access
	// and the runner's delivery file from it; views and observation logs omit it.
	RuntimeDelivery json.RawMessage `json:"-"`
}

// NativeBundle projects this catalog-verified profile for immutable startup
// delivery. Both the publisher and consumer derive identity from this producer.
// MeanNanos is the mean measured execution time of batch b.
func (p *Profile) MeanNanos(b int) (float64, bool) {
	for _, batch := range p.Batches {
		if batch.Batch == b && len(batch.DurationNanos) > 0 {
			sum := 0.0
			for _, d := range batch.DurationNanos {
				sum += d
			}
			return sum / float64(len(batch.DurationNanos)), true
		}
	}
	return 0, false
}

func (p *Profile) NativeBundle() firstfloor.ProfileBundle {
	bundle := firstfloor.ProfileBundle{ProfileID: p.ID, Batches: make([]firstfloor.BatchProfile, len(p.Batches))}
	for i, batch := range p.Batches {
		samples := make([]float64, len(batch.DurationNanos))
		for j, value := range batch.DurationNanos {
			samples[j] = value / float64(time.Millisecond)
		}
		slices.Sort(samples)
		bundle.Batches[i] = firstfloor.BatchProfile{
			Size: batch.Batch, ServiceSamplesMS: samples, EnergyPerItemJ: batch.EnergyPerInputJoules,
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
	ID               string              `json:"id"`
	TargetID         string              `json:"targetID,omitempty"`
	OwnerWorkloadUID string              `json:"ownerWorkloadUID,omitempty"`
	Node             NodeReference       `json:"node"`
	Profile          string              `json:"profile"`
	Ready            bool                `json:"ready"`
	Stopped          bool                `json:"stopped"`
	Reason           string              `json:"reason,omitempty"`
	Workloads        []WorkloadReference `json:"workloads"`
	Waiting          map[string]int      `json:"waiting"`
	Running          map[string]int      `json:"running"`
	// AssignedUnknown has been dispatched but lacks a native phase observation.
	// It is not reported as actual backend waiting or running.
	AssignedUnknown map[string]int    `json:"assignedUnknown"`
	ObservedAt      time.Time         `json:"observedAt"`
	Native          *firstfloor.State `json:"native,omitempty"`
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
	RequestID         string           `json:"requestID"`
	WorkloadName      string           `json:"workloadName"`
	WorkloadUID       string           `json:"workloadUID,omitempty"`
	Definition        string           `json:"definition,omitempty"`
	Kind              string           `json:"kind"`
	At                time.Time        `json:"at"`
	ReceivedAt        time.Time        `json:"receivedAt"`
	ElapsedNanos      int64            `json:"elapsedNanos"`
	Assignment        *Assignment      `json:"assignment,omitempty"`
	NativeInstance    string           `json:"nativeInstance,omitempty"`
	NativeProgress    *BackendProgress `json:"nativeProgress,omitempty"`
	ClientSpanNanos   int64            `json:"clientSpanNanos,omitempty"`
	ExecutionComplete bool             `json:"executionComplete"`
	LifetimeNanos     int64            `json:"lifetimeNanos,omitempty"`
	SLO               *SLO             `json:"slo,omitempty"`
	Outcome           string           `json:"outcome,omitempty"`
	Error             string           `json:"error,omitempty"`
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
	// CompletionMarginMS is the scheduler setting current at dispatch.
	CompletionMarginMS float64
}

type BackendResult struct {
	Body        []byte
	ContentType string
	StatusCode  int
	// Finished proves either no submission or a completed synchronous response.
	// A transport failure cannot prove that execution stopped.
	Finished   bool
	Progress   *BackendProgress
	ClientSpan time.Duration
}

// ReadyInstance is constructed by the Kubernetes and native-runtime boundary.
// The runtime never turns a desired Pod or a static address into readiness.
type ReadyInstance struct {
	State            InstanceState
	Profile          *Profile
	Weight           int32
	Routing          map[string]int32 // last applied weight per Workload UID; nil applies Weight to every compatible Workload
	Capacity         int
	OwnerWorkloadUID string
	Execute          func(context.Context, Dispatch) (BackendResult, error)
}

type BackendProgress struct {
	RequestID         string    `json:"requestID"`
	WorkloadUID       string    `json:"workloadUID"`
	Definition        string    `json:"definition"`
	Phase             string    `json:"phase"`
	At                time.Time `json:"at"`
	Outcome           string    `json:"outcome,omitempty"`
	Error             string    `json:"error,omitempty"`
	BatchID           uint64    `json:"batchID,omitempty"`
	BatchSize         int       `json:"batchSize,omitempty"`
	ProfileID         string    `json:"profileID,omitempty"`
	DecisionID        string    `json:"decisionID,omitempty"`
	QueueMS           float64   `json:"queueMS"`
	ServiceMS         float64   `json:"serviceMS"`
	BackendMS         float64   `json:"backendMS"`
	PreprocessMS      float64   `json:"preprocessMS"`
	PostprocessMS     float64   `json:"postprocessMS"`
	FirstTokenMS      float64   `json:"firstTokenMS,omitempty"`
	Units             int       `json:"units,omitempty"`
	NativeElapsedMS   float64   `json:"nativeElapsedMS"`
	ExecutionComplete bool      `json:"executionComplete"`
}
