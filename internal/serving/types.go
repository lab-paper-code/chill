package serving

import (
	"context"
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

const (
	phaseAssigned      = "assigned"
	phaseRunning       = "running"
	phaseFinished      = "finished"
	outcomeIncomplete  = "incomplete"
	reasonShuttingDown = "ShuttingDown"
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
	Name          string                      `json:"name"`
	Artifact      string                      `json:"artifact"`
	Format        string                      `json:"format"`
	Image         string                      `json:"image"`
	Architecture  string                      `json:"architecture"`
	Path          string                      `json:"path"`
	RuntimeFamily string                      `json:"runtimeFamily"`
	Backend       string                      `json:"backend"`
	Tensors       []modelcompat.InputTensor   `json:"tensors"`
	Triton        *modelcompat.TritonSettings `json:"triton,omitempty"`
}

type Model struct {
	Name       string      `json:"name"`
	UID        string      `json:"uid"`
	Generation int64       `json:"generation"`
	Paths      []ModelPath `json:"paths"`
}

// Definition is immutable after the management boundary publishes it. Requests
// share it instead of copying the model declaration for every queue hop.
type Definition struct {
	Name       string `json:"name"`
	UID        string `json:"uid"`
	Generation int64  `json:"generation"`
	Model      Model  `json:"model"`
	SLO        SLO    `json:"slo"`
}

func (d *Definition) Identity() string {
	return fmt.Sprintf("%s/%d/%s/%d", d.UID, d.Generation, d.Model.UID, d.Model.Generation)
}

type BatchProfile struct {
	Batch                int       `json:"batch"`
	DurationNanos        []float64 `json:"durationNanos"`
	EnergyPerInputJoules float64   `json:"energyPerInputJoules"`
	MemoryBytes          uint64    `json:"memoryBytes"`
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
	Pool                   string                         `json:"pool"`
	Node                   NodeReference                  `json:"node"`
	Instance               string                         `json:"instance"`
	Artifact               string                         `json:"artifact"`
	ExecutionPath          string                         `json:"executionPath"`
	Profile                string                         `json:"profile"`
	ProfileReference       evidencestore.ObjectIdentity   `json:"profileReference"`
	ProfileEvidence        []evidencestore.ObjectIdentity `json:"profileEvidence"`
	ProfileConfidenceLevel float64                        `json:"profileConfidenceLevel"`
	Priority               uint64                         `json:"priority"`
	DispatchedAt           time.Time                      `json:"dispatchedAt"`
}

type InstanceState struct {
	ID        string              `json:"id"`
	Node      NodeReference       `json:"node"`
	Pool      string              `json:"pool"`
	Profile   string              `json:"profile"`
	Ready     bool                `json:"ready"`
	Reason    string              `json:"reason,omitempty"`
	Workloads []WorkloadReference `json:"workloads"`
	Waiting   map[string]int      `json:"waiting"`
	Running   map[string]int      `json:"running"`
	// AssignedUnknown has been dispatched but lacks a native phase observation.
	// It is not reported as actual backend waiting or running.
	AssignedUnknown map[string]int `json:"assignedUnknown"`
	ObservedAt      time.Time      `json:"observedAt"`
}

type PoolState struct {
	ID               string              `json:"id"`
	CurrentNodes     []NodeReference     `json:"currentNodes"`
	ServingWorkloads []WorkloadReference `json:"servingWorkloads"`
	Instances        []InstanceState     `json:"instances"`
	ObservedAt       time.Time           `json:"observedAt"`
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
	RequestID      string             `json:"requestID"`
	WorkloadName   string             `json:"workloadName"`
	WorkloadUID    string             `json:"workloadUID,omitempty"`
	Definition     string             `json:"definition,omitempty"`
	Kind           string             `json:"kind"`
	At             time.Time          `json:"at"`
	ReceivedAt     time.Time          `json:"receivedAt"`
	ElapsedNanos   int64              `json:"elapsedNanos"`
	Assignment     *Assignment        `json:"assignment,omitempty"`
	NativeInstance string             `json:"nativeInstance,omitempty"`
	NativeQueue    *NativeQueueTiming `json:"nativeQueue,omitempty"`
	SLO            *SLO               `json:"slo,omitempty"`
	Outcome        string             `json:"outcome,omitempty"`
	Error          string             `json:"error,omitempty"`
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
}

// Dispatch is immutable after assignment. Client cancellation does not cancel
// the backend execution context: drain needs actual backend completion.
type Dispatch struct {
	RequestID  string
	Definition *Definition
	Input      *InferenceInput
	Assignment Assignment
	Priority   uint64
	ReceivedAt time.Time
	Elapsed    time.Duration
	Deadline   time.Time
}

type BackendResult struct {
	Body        []byte
	ContentType string
	StatusCode  int
	// Finished proves either no submission or a completed synchronous response.
	// A transport failure cannot prove that execution stopped.
	Finished bool
}

// ReadyInstance is constructed by the Kubernetes and native-runtime boundary.
// The runtime never turns a desired Pod or a static address into readiness.
type ReadyInstance struct {
	State            InstanceState
	Profile          *Profile
	Weight           int32
	Capacity         int
	Lanes            int
	OwnerWorkloadUID string
	Execute          func(context.Context, Dispatch) (BackendResult, error)
}

// NativeQueueTiming uses Triton's CLOCK_MONOTONIC timestamps, not UTC.
// A missing ComputeStartNanos records queue entry without inventing execution.
type NativeQueueTiming struct {
	QueueStartNanos   uint64 `json:"queueStartNanos"`
	ComputeStartNanos uint64 `json:"computeStartNanos,omitempty"`
}

type BackendProgress struct {
	RequestID   string             `json:"requestID"`
	WorkloadUID string             `json:"workloadUID"`
	Definition  string             `json:"definition"`
	Phase       string             `json:"phase"`
	At          time.Time          `json:"at"`
	Outcome     string             `json:"outcome,omitempty"`
	Error       string             `json:"error,omitempty"`
	NativeQueue *NativeQueueTiming `json:"nativeQueue,omitempty"`
}
