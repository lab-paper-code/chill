// Package transitioncost derives one power-mode transition-cost observation
// from frozen estimator inputs and raw, Kubernetes-independent facts.
package transitioncost

import (
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
)

// Verdict is the terminal scientific classification of one raw transition
// observation.
type Verdict string

const (
	VerdictAccepted     Verdict = "Accepted"
	VerdictInvalid      Verdict = "Invalid"
	VerdictFailed       Verdict = "Failed"
	VerdictInsufficient Verdict = "Insufficient"
)

// Reason is the stable terminal reason paired with a Verdict.
type Reason string

const (
	ReasonAccepted                      Reason = "Accepted"
	ReasonObservationMalformed          Reason = "ObservationMalformed"
	ReasonSourceStateMismatch           Reason = "SourceStateMismatch"
	ReasonActuationFailed               Reason = "ActuationFailed"
	ReasonTargetReadBackFailed          Reason = "TargetReadBackFailed"
	ReasonTargetStateMismatch           Reason = "TargetStateMismatch"
	ReasonReadinessNotReached           Reason = "ReadinessNotReached"
	ReasonPowerWindowInsufficient       Reason = "PowerWindowInsufficient"
	ReasonBoundaryUncertaintyExceeded   Reason = "BoundaryUncertaintyExceeded"
	ReasonServingSurvivalUnknown        Reason = "ServingSurvivalUnknown"
	ReasonNativeStateUnverified         Reason = "NativeStateUnverified"
	ReasonPowerChangedDuringMeasurement Reason = "PowerChangedDuringMeasurement"
)

// StateIdentity is one exact Layer-0 device, model, and power-mode state.
type StateIdentity struct {
	DeviceType string `json:"deviceType"`
	Model      string `json:"model"`
	PowerMode  string `json:"powerMode"`
}

// IdleBaseline binds the numeric target-idle value to its exact retained
// evidence and state.
type IdleBaseline struct {
	EvidenceReference string        `json:"evidenceReference"`
	State             StateIdentity `json:"state"`
	MeanWallWatts     float64       `json:"meanWallWatts"`
}

// FrozenContract contains the immutable estimator inputs for one directed
// transition observation.
type FrozenContract struct {
	SourceState        StateIdentity `json:"sourceState"`
	TargetState        StateIdentity `json:"targetState"`
	TargetIdleBaseline IdleBaseline  `json:"targetIdleBaseline"`
	// SettlingSeconds is the declared post-Ready energy observation period.
	// Zero retains the no-settling measurement contract.
	SettlingSeconds int32 `json:"settlingSeconds,omitempty"`
}

// Policy contains only the quality bounds needed to close and integrate one
// transition window.
type Policy struct {
	MaximumBoundaryDistance      time.Duration `json:"maximumBoundaryDistanceNanoseconds"`
	MinimumCoverage              float64       `json:"minimumCoverage"`
	MaximumSampleGap             time.Duration `json:"maximumSampleGapNanoseconds"`
	MaximumBoundaryUncertainty   time.Duration `json:"maximumBoundaryUncertaintyNanoseconds"`
	MaximumReadyObservationDelay time.Duration `json:"maximumReadyObservationDelayNanoseconds"`
}

// MappedFact retains a device-side fact and its corresponding timestamp in
// the meter/observer time domain.
type MappedFact struct {
	OccurredAt time.Time `json:"occurredAt"`
	MappedAt   time.Time `json:"mappedAt"`
}

// OccurredFact is one device-clock fact whose observer-clock mapping is
// retained separately as a receipt-time bracket.
type OccurredFact struct {
	OccurredAt time.Time `json:"occurredAt"`
}

// ReceiptTimeBracket bounds when one marker first became visible to the
// observer using two consecutive receipt times on that observer's clock.
type ReceiptTimeBracket struct {
	LastAbsentReceivedAt   time.Time `json:"lastAbsentReceivedAt"`
	FirstPresentReceivedAt time.Time `json:"firstPresentReceivedAt"`
}

// TransitionMarkerBrackets retains the three observer-clock mappings needed
// to derive one transition window and its measured mapping uncertainty.
type TransitionMarkerBrackets struct {
	ApplyStartedAt      ReceiptTimeBracket `json:"applyStartedAt"`
	TargetObservation   ReceiptTimeBracket `json:"targetObservation"`
	ReadyProcessWitness ReceiptTimeBracket `json:"readyProcessWitness"`
}

// StateReadBack is one independently observed state fact.
type StateReadBack struct {
	Fact  MappedFact    `json:"fact"`
	State StateIdentity `json:"state"`
}

// TargetStateReadBack retains the device fact and state without duplicating
// the observer receipt mapping owned by TransitionMarkerBrackets.
type TargetStateReadBack struct {
	Fact  OccurredFact  `json:"fact"`
	State StateIdentity `json:"state"`
}

// Failure is one timestamped failed actuation or state read-back.
type Failure struct {
	ObservedAt time.Time `json:"observedAt"`
	Message    string    `json:"message"`
}

// PowerSample is one wall-power fact. ObservedAt is the observer's HTTP
// receipt time.
type PowerSample struct {
	ObservedAt            time.Time `json:"observedAt"`
	RequestLatencySeconds float64   `json:"requestLatencySeconds"`
	Watts                 float64   `json:"watts"`
}

// PowerReadFailure is one retained failed meter read. ObservedAt is the
// observer's HTTP receipt time; Reason is the retained factual failure class.
type PowerReadFailure struct {
	ObservedAt            time.Time `json:"observedAt"`
	RequestLatencySeconds float64   `json:"requestLatencySeconds"`
	Reason                string    `json:"reason"`
	Message               string    `json:"message"`
}

// ProcessIdentity is the exact serving process/container incarnation.
type ProcessIdentity struct {
	ContainerName string    `json:"containerName"`
	ContainerID   string    `json:"containerID"`
	StartedAt     time.Time `json:"startedAt"`
	RestartCount  int32     `json:"restartCount"`
}

// ProcessObservation records a running process observed in a Ready Pod.
type ProcessObservation struct {
	ObservedAt time.Time       `json:"observedAt"`
	Process    ProcessIdentity `json:"process"`
}

// RawObservation is one factual observation of one Node power-mode transition.
// It contains no derived transition boundary, duration, energy, or scientific
// verdict.
type RawObservation struct {
	ObservationStartedAt time.Time `json:"observationStartedAt"`
	ObservationEndedAt   time.Time `json:"observationEndedAt"`

	SourceReadBack        StateReadBack            `json:"sourceReadBack"`
	TransitionStart       *OccurredFact            `json:"transitionStart"`
	ActuationFailure      *Failure                 `json:"actuationFailure"`
	TargetReadBack        *TargetStateReadBack     `json:"targetReadBack"`
	TargetReadBackFailure *Failure                 `json:"targetReadBackFailure"`
	MarkerBrackets        TransitionMarkerBrackets `json:"markerBrackets"`
	// Generation is the Node power-mode generation at observer start and
	// finish; empty on a Node without an actuation agent.
	Generation *PowerModeGeneration `json:"generation,omitempty"`

	PowerSamples  []PowerSample      `json:"powerSamples"`
	PowerFailures []PowerReadFailure `json:"powerFailures"`

	// PostTransitionProcess is the running process in the fresh Ready Pod whose
	// receipt time is recorded by MarkerBrackets.ReadyProcessWitness.
	PreTransitionProcess  *ProcessObservation           `json:"preTransitionProcess"`
	PostTransitionProcess *ProcessIdentity              `json:"postTransitionProcess"`
	BeforeNative          *NativeNodeObservation        `json:"beforeNative"`
	AfterNative           *NativeNodeObservation        `json:"afterNative"`
	NativeObservations    []NativeNodeObservation       `json:"nativeObservations"`
	NativeFailures        []NativeReadFailure           `json:"nativeFailures"`
	PowerSource           metricobserver.SourceIdentity `json:"powerSource"`
}

// PowerModeGeneration is the Node power-mode generation at window start and end.
type PowerModeGeneration struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Observation is an opaque accepted observation. Its values originate from
// one transition and its declared post-Ready energy-cost window.
type Observation struct {
	record          FinalizedAcceptedObservationRecord
	baseline        IdleBaseline
	settlingSeconds int32
}

func (o Observation) StartedAt() time.Time { return o.record.StartedAt }

func (o Observation) CompletedAt() time.Time { return o.record.CompletedAt }

func (o Observation) CostWindowEndedAt() time.Time {
	return o.record.CompletedAt.Add(time.Duration(o.settlingSeconds) * time.Second)
}

func (o Observation) BoundaryMappingUncertainty() time.Duration {
	return time.Duration(o.record.BoundaryMappingUncertaintyMilliseconds) * time.Millisecond
}

func (o Observation) TauMilliseconds() float64 { return o.record.TauMilliseconds }

func (o Observation) TransitionWallJoules() float64 { return o.record.TransitionWallJoules }

func (o Observation) TargetIdleBaseline() IdleBaseline { return o.baseline }

func (o Observation) EtaJoules() float64 { return o.record.EtaJoules }

func (o Observation) ServingSurvived() bool { return o.record.ServingSurvived }
