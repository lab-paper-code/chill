// Package transitioncost derives one power-mode transition-cost observation
// from frozen estimator inputs and raw, Kubernetes-independent facts.
package transitioncost

import "time"

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
	ReasonAccepted                    Reason = "Accepted"
	ReasonObservationMalformed        Reason = "ObservationMalformed"
	ReasonSourceStateMismatch         Reason = "SourceStateMismatch"
	ReasonActuationFailed             Reason = "ActuationFailed"
	ReasonTargetReadBackFailed        Reason = "TargetReadBackFailed"
	ReasonTargetStateMismatch         Reason = "TargetStateMismatch"
	ReasonReadinessNotReached         Reason = "ReadinessNotReached"
	ReasonPowerWindowInsufficient     Reason = "PowerWindowInsufficient"
	ReasonBoundaryUncertaintyExceeded Reason = "BoundaryUncertaintyExceeded"
	ReasonServingSurvivalUnknown      Reason = "ServingSurvivalUnknown"
)

// StateIdentity is one exact Layer-0 device, model, and power-mode state.
type StateIdentity struct {
	DeviceType string
	Model      string
	PowerMode  string
}

// IdleBaseline binds the numeric target-idle value to its exact retained
// evidence and state.
type IdleBaseline struct {
	EvidenceReference string
	State             StateIdentity
	MeanWallWatts     float64
}

// FrozenContract contains the immutable estimator inputs for one directed
// transition observation.
type FrozenContract struct {
	SourceState        StateIdentity
	TargetState        StateIdentity
	TargetIdleBaseline IdleBaseline
}

// Policy contains only the quality bounds needed to close and integrate one
// transition window.
type Policy struct {
	MaximumBoundaryDistance      time.Duration
	MinimumCoverage              float64
	MaximumSampleGap             time.Duration
	MaximumBoundaryUncertainty   time.Duration
	MaximumReadyObservationDelay time.Duration
}

// MappedFact retains a device-side fact and its corresponding timestamp in
// the meter/observer time domain.
type MappedFact struct {
	OccurredAt time.Time
	MappedAt   time.Time
}

// StateReadBack is one independently observed state fact.
type StateReadBack struct {
	Fact  MappedFact
	State StateIdentity
}

// Failure is one timestamped failed actuation or state read-back.
type Failure struct {
	ObservedAt time.Time
	Message    string
}

// PowerSample is one wall-power fact. ObservedAt is the observer's HTTP
// receipt time.
type PowerSample struct {
	ObservedAt            time.Time
	RequestLatencySeconds float64
	Watts                 float64
}

// PowerReadFailure is one retained failed meter read. ObservedAt is the
// observer's HTTP receipt time; Reason is the retained factual failure class.
type PowerReadFailure struct {
	ObservedAt            time.Time
	RequestLatencySeconds float64
	Reason                string
	Message               string
}

// ReadyObservation is one observation of the selected serving workload's
// existing request-readiness signal.
type ReadyObservation struct {
	ObservedAt time.Time
	Ready      bool
}

// ProcessIdentity is the exact serving process/container incarnation.
type ProcessIdentity struct {
	ContainerName string
	ContainerID   string
	StartedAt     time.Time
	RestartCount  int32
}

// ProcessObservation binds one serving-process observation to its timestamp.
type ProcessObservation struct {
	ObservedAt time.Time
	Process    ProcessIdentity
}

// RawObservation is one factual observation of one NodeActuation. It contains
// no derived transition boundary, duration, energy, or scientific verdict.
type RawObservation struct {
	ObservationStartedAt time.Time
	ObservationEndedAt   time.Time

	SourceReadBack        StateReadBack
	TransitionStart       *MappedFact
	ActuationFailure      *Failure
	TargetReadBack        *StateReadBack
	TargetReadBackFailure *Failure

	PowerSamples  []PowerSample
	PowerFailures []PowerReadFailure
	Readiness     []ReadyObservation

	PreTransitionProcess  *ProcessObservation
	PostTransitionProcess *ProcessObservation

	BoundaryMappingUncertainty time.Duration
}

type acceptedRecord struct {
	startedAt                  time.Time
	completedAt                time.Time
	boundaryMappingUncertainty time.Duration
	tauMilliseconds            float64
	transitionWallJoules       float64
	targetIdleBaseline         IdleBaseline
	etaJoules                  float64
	servingSurvived            bool
}

type resultRecord struct {
	verdict  Verdict
	reason   Reason
	detail   string
	frozen   FrozenContract
	raw      RawObservation
	accepted *acceptedRecord
}

// Result is an opaque terminal classification. Only Derive can construct a
// scientifically classified result.
type Result struct {
	record resultRecord
}

// Verdict returns the terminal scientific classification.
func (r Result) Verdict() Verdict { return r.record.verdict }

// Reason returns the stable terminal reason.
func (r Result) Reason() Reason { return r.record.reason }

// Detail returns diagnostic detail that is not part of the stable reason.
func (r Result) Detail() string { return r.record.detail }

// FrozenContract returns the immutable inputs retained with this result.
func (r Result) FrozenContract() FrozenContract { return r.record.frozen }

// RawObservation returns a copy of the factual evidence retained with this
// result.
func (r Result) RawObservation() RawObservation { return cloneRaw(r.record.raw) }

// AcceptedObservation returns the derived values only for VerdictAccepted.
func (r Result) AcceptedObservation() (Observation, bool) {
	if r.record.accepted == nil {
		return Observation{}, false
	}
	return Observation{record: *r.record.accepted}, true
}

// Observation is an opaque accepted observation. Its values always originate
// from the common transition window verified by Derive.
type Observation struct {
	record acceptedRecord
}

func (o Observation) StartedAt() time.Time { return o.record.startedAt }

func (o Observation) CompletedAt() time.Time { return o.record.completedAt }

func (o Observation) BoundaryMappingUncertainty() time.Duration {
	return o.record.boundaryMappingUncertainty
}

func (o Observation) TauMilliseconds() float64 { return o.record.tauMilliseconds }

func (o Observation) TransitionWallJoules() float64 { return o.record.transitionWallJoules }

func (o Observation) TargetIdleBaseline() IdleBaseline { return o.record.targetIdleBaseline }

func (o Observation) EtaJoules() float64 { return o.record.etaJoules }

func (o Observation) ServingSurvived() bool { return o.record.servingSurvived }
