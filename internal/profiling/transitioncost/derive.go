package transitioncost

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/prometheus/common/model"
)

// Derive validates one frozen contract and one factual raw observation, then
// produces one terminal scientific result. Malformed frozen inputs are caller
// errors; terminal properties of retained raw evidence are typed results.
func Derive(frozen FrozenContract, raw RawObservation, policy Policy) (Result, error) {
	verifiedFrozen := cloneFrozen(frozen)
	verifiedRaw := cloneRaw(raw)
	if err := validateFrozen(verifiedFrozen); err != nil {
		return Result{}, fmt.Errorf("validate frozen transition contract: %w", err)
	}
	if err := validatePolicy(policy); err != nil {
		return Result{}, fmt.Errorf("validate transition-cost policy: %w", err)
	}

	verified := verifiedInput{frozen: verifiedFrozen, raw: verifiedRaw, policy: policy}
	if detail := validateRawStructure(verified.raw); detail != "" {
		return classified(verified, VerdictInvalid, ReasonObservationMalformed, detail), nil
	}
	if detail := identityContradiction(verified.frozen, verified.raw); detail != "" {
		return classified(verified, VerdictInvalid, ReasonIdentityContradiction, detail), nil
	}
	if verified.raw.SourceReadBack.State != verified.frozen.SourceState {
		return classified(verified, VerdictInvalid, ReasonSourceStateMismatch,
			"observed source state differs from the frozen source state"), nil
	}
	if verified.raw.ActuationFailure != nil {
		return classified(verified, VerdictFailed, ReasonActuationFailed,
			verified.raw.ActuationFailure.Message), nil
	}
	if verified.raw.TransitionStart == nil {
		return classified(verified, VerdictFailed, ReasonActuationFailed,
			"transition start was not observed"), nil
	}
	if verified.raw.TargetReadBackFailure != nil {
		return classified(verified, VerdictFailed, ReasonTargetReadBackFailed,
			verified.raw.TargetReadBackFailure.Message), nil
	}
	if verified.raw.TargetReadBack == nil {
		return classified(verified, VerdictFailed, ReasonTargetReadBackFailed,
			"independent target read-back was not observed"), nil
	}
	if verified.raw.TargetReadBack.State != verified.frozen.TargetState {
		return classified(verified, VerdictFailed, ReasonTargetStateMismatch,
			"independent read-back did not confirm the frozen target state"), nil
	}

	completedAt, ok := earliestReady(verified)
	if !ok {
		return classified(verified, VerdictFailed, ReasonReadinessNotReached,
			"no fresh Ready=True observation followed target read-back"), nil
	}
	startedAt := verified.raw.TransitionStart.MappedAt
	if !completedAt.After(startedAt) {
		return classified(verified, VerdictInvalid, ReasonObservationMalformed,
			"completed transition boundary does not follow transition start"), nil
	}
	if verified.raw.BoundaryMappingUncertainty > verified.policy.MaximumBoundaryUncertainty {
		return classified(verified, VerdictInsufficient, ReasonBoundaryUncertaintyExceeded,
			"boundary mapping uncertainty exceeds policy"), nil
	}

	joules, err := powerwindow.Integrate(powerwindow.Window{
		Start:   startedAt,
		End:     completedAt,
		Samples: powerSamples(verified.raw.PowerSamples),
	}, powerwindow.Policy{
		MaximumBoundaryDistance: verified.policy.MaximumBoundaryDistance,
		MinimumCoverage:         verified.policy.MinimumCoverage,
		MaximumGap:              verified.policy.MaximumSampleGap,
	})
	if err != nil {
		return classified(verified, VerdictInsufficient, ReasonPowerWindowInsufficient,
			err.Error()), nil
	}

	servingSurvived, known := servingSurvival(verified.raw, startedAt, completedAt)
	if !known {
		return classified(verified, VerdictInsufficient, ReasonServingSurvivalUnknown,
			"pre-transition and post-completion process identity is incomplete"), nil
	}

	tauSeconds := completedAt.Sub(startedAt).Seconds()
	accepted := &acceptedRecord{
		startedAt:                  startedAt,
		completedAt:                completedAt,
		boundaryMappingUncertainty: verified.raw.BoundaryMappingUncertainty,
		tauMilliseconds:            tauSeconds * 1000,
		transitionWallJoules:       joules,
		targetIdleBaseline:         verified.frozen.TargetIdleBaseline,
		etaJoules:                  joules - verified.frozen.TargetIdleBaseline.MeanWallWatts*tauSeconds,
		servingSurvived:            servingSurvived,
	}
	return Result{record: resultRecord{
		verdict: VerdictAccepted, reason: ReasonAccepted,
		frozen: verified.frozen, raw: verified.raw, accepted: accepted,
	}}, nil
}

// verifiedInput can only be constructed after FrozenContract and Policy cross
// their validation boundary.
type verifiedInput struct {
	frozen FrozenContract
	raw    RawObservation
	policy Policy
}

func classified(input verifiedInput, verdict Verdict, reason Reason, detail string) Result {
	return Result{record: resultRecord{
		verdict: verdict, reason: reason, detail: detail,
		frozen: input.frozen, raw: input.raw,
	}}
}

func validateFrozen(frozen FrozenContract) error {
	if !validObject(frozen.Node) || !plainIdentity(frozen.DeviceClass) ||
		!validActuator(frozen.Actuator) || !validNodeActuation(frozen.NodeActuation) ||
		!validState(frozen.SourceState) || !validState(frozen.TargetState) ||
		!validWorkload(frozen.Workload) || !validObserver(frozen.Observer) ||
		!plainIdentity(frozen.ProtocolIdentity) ||
		!plainIdentity(frozen.MeterIdentity) || !validMeterSource(frozen.MeterSource) ||
		!plainIdentity(frozen.TargetIdleBaseline.EvidenceReference) {
		return errors.New("frozen identity is incomplete or contains surrounding whitespace")
	}
	if frozen.SourceState.DeviceType != frozen.TargetState.DeviceType ||
		frozen.SourceState.Model != frozen.TargetState.Model ||
		frozen.SourceState.PowerMode == frozen.TargetState.PowerMode {
		return errors.New("source and target must be distinct power modes of one exact device type and model")
	}
	if frozen.Observer.Node.Name == frozen.Node.Name {
		return errors.New("observer Node must be off-target")
	}
	if frozen.TargetIdleBaseline.State != frozen.TargetState {
		return errors.New("target idle baseline state differs from the frozen target state")
	}
	if !finiteNonNegative(frozen.TargetIdleBaseline.MeanWallWatts) {
		return errors.New("target idle baseline watts must be finite and non-negative")
	}
	return nil
}

func validatePolicy(policy Policy) error {
	if policy.MaximumBoundaryDistance < 0 || policy.MaximumSampleGap <= 0 ||
		policy.MaximumBoundaryUncertainty < 0 || policy.MaximumReadyObservationDelay <= 0 ||
		policy.MinimumCoverage <= 0 || policy.MinimumCoverage > 1 ||
		math.IsNaN(policy.MinimumCoverage) || math.IsInf(policy.MinimumCoverage, 0) {
		return errors.New("policy bounds are invalid")
	}
	return nil
}

func validateRawStructure(raw RawObservation) string {
	if detail := validateRawHeader(raw); detail != "" {
		return detail
	}
	if detail := validateTransitionFacts(raw); detail != "" {
		return detail
	}
	return validateObservedFacts(raw)
}

func validateRawHeader(raw RawObservation) string {
	if raw.ObservationStartedAt.IsZero() || raw.ObservationEndedAt.IsZero() ||
		!raw.ObservationEndedAt.After(raw.ObservationStartedAt) {
		return "raw observation coverage window is invalid"
	}
	if !validObject(raw.Node) || !plainIdentity(raw.DeviceClass) ||
		!validActuator(raw.Actuator) || !validNodeActuation(raw.NodeActuation) ||
		!validState(raw.SourceState) || !validState(raw.TargetState) ||
		!validWorkload(raw.Workload) || !validObserver(raw.Observer) ||
		!plainIdentity(raw.ProtocolIdentity) ||
		!plainIdentity(raw.MeterIdentity) || !validMeterSource(raw.MeterSource) ||
		!plainIdentity(raw.BaselineReference) ||
		!validState(raw.BaselineState) || !validStateReadBack(raw.SourceReadBack) {
		return "raw observation identity or source read-back is malformed"
	}
	if raw.BoundaryMappingUncertainty < 0 {
		return "boundary mapping uncertainty is negative"
	}
	if !within(raw.SourceReadBack.Fact.MappedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) {
		return "source read-back is outside raw observation coverage"
	}
	return ""
}

func validateTransitionFacts(raw RawObservation) string {
	if raw.TransitionStart != nil {
		if !validMappedFact(*raw.TransitionStart) ||
			!within(raw.TransitionStart.MappedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) {
			return "transition-start fact is malformed or outside coverage"
		}
		if raw.TransitionStart.MappedAt.Before(raw.SourceReadBack.Fact.MappedAt) ||
			raw.TransitionStart.OccurredAt.Before(raw.SourceReadBack.Fact.OccurredAt) {
			return "transition start precedes source read-back"
		}
	}
	if detail := validateFailure(raw.ActuationFailure, raw.ObservationStartedAt, raw.ObservationEndedAt); detail != "" {
		return "actuation failure " + detail
	}
	if detail := validateFailure(raw.TargetReadBackFailure, raw.ObservationStartedAt, raw.ObservationEndedAt); detail != "" {
		return "target read-back failure " + detail
	}
	if raw.ActuationFailure != nil && raw.TargetReadBack != nil {
		return "actuation failure contradicts a successful target read-back"
	}
	if raw.TargetReadBackFailure != nil && raw.TargetReadBack != nil {
		return "target read-back has both success and failure facts"
	}
	if raw.TargetReadBack != nil {
		if raw.TransitionStart == nil || !validStateReadBack(*raw.TargetReadBack) ||
			!within(raw.TargetReadBack.Fact.MappedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) {
			return "target read-back is malformed, unstarted, or outside coverage"
		}
		if raw.TargetReadBack.Fact.MappedAt.Before(raw.TransitionStart.MappedAt) ||
			raw.TargetReadBack.Fact.OccurredAt.Before(raw.TransitionStart.OccurredAt) {
			return "target read-back precedes transition start"
		}
	}
	return ""
}

func validateObservedFacts(raw RawObservation) string {
	for _, sample := range raw.PowerSamples {
		if sample.ObservedAt.IsZero() || sample.RequestLatency < 0 ||
			!finiteNonNegative(sample.Watts) ||
			!within(sample.ObservedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) {
			return "power sample is malformed or outside raw observation coverage"
		}
	}
	for index := range raw.PowerFailures {
		if detail := validatePowerReadFailure(raw.PowerFailures[index], raw.ObservationStartedAt, raw.ObservationEndedAt); detail != "" {
			return "power failure " + detail
		}
	}
	for _, readiness := range raw.Readiness {
		if readiness.ObservedAt.IsZero() || !validWorkload(readiness.Workload) ||
			!within(readiness.ObservedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) {
			return "readiness observation is malformed or outside coverage"
		}
	}
	for _, process := range []*ProcessObservation{raw.PreTransitionProcess, raw.PostTransitionProcess} {
		if process == nil {
			continue
		}
		if process.ObservedAt.IsZero() || !within(process.ObservedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) ||
			!validWorkload(process.Workload) || !validProcess(process.Process) {
			return "serving process observation is malformed or outside coverage"
		}
	}
	return ""
}

func identityContradiction(frozen FrozenContract, raw RawObservation) string {
	if raw.Node != frozen.Node {
		return "raw Node identity differs from the frozen Node"
	}
	if raw.DeviceClass != frozen.DeviceClass {
		return "raw DeviceClass differs from the frozen DeviceClass"
	}
	if raw.Actuator != frozen.Actuator {
		return "raw actuator identity differs from the frozen actuator"
	}
	if raw.NodeActuation != frozen.NodeActuation {
		return "raw NodeActuation identity differs from the frozen execution"
	}
	if raw.SourceState != frozen.SourceState || raw.TargetState != frozen.TargetState {
		return "raw declared state pair differs from the frozen state pair"
	}
	if raw.Workload != frozen.Workload {
		return "raw workload identity differs from the frozen workload"
	}
	if raw.Observer != frozen.Observer {
		return "raw observer identity differs from the frozen observer"
	}
	if raw.ProtocolIdentity != frozen.ProtocolIdentity || raw.MeterIdentity != frozen.MeterIdentity {
		return "raw protocol or meter identity differs from the frozen identity"
	}
	if !meterSourceMatches(raw.MeterSource, frozen.MeterSource) {
		return "raw meter source identity differs from the frozen meter source"
	}
	if raw.BaselineReference != frozen.TargetIdleBaseline.EvidenceReference ||
		raw.BaselineState != frozen.TargetIdleBaseline.State {
		return "raw target idle baseline identity differs from the frozen baseline"
	}
	for _, readiness := range raw.Readiness {
		if readiness.Workload != frozen.Workload {
			return "readiness evidence refers to a different workload"
		}
	}
	for _, process := range []*ProcessObservation{raw.PreTransitionProcess, raw.PostTransitionProcess} {
		if process != nil && process.Workload != frozen.Workload {
			return "serving process evidence refers to a different workload"
		}
	}
	return ""
}

func earliestReady(input verifiedInput) (time.Time, bool) {
	targetReadBackAt := input.raw.TargetReadBack.Fact.MappedAt
	latest := targetReadBackAt.Add(input.policy.MaximumReadyObservationDelay)
	var earliest time.Time
	for _, readiness := range input.raw.Readiness {
		if !readiness.Ready || !readiness.ObservedAt.After(targetReadBackAt) ||
			readiness.ObservedAt.After(latest) {
			continue
		}
		if earliest.IsZero() || readiness.ObservedAt.Before(earliest) {
			earliest = readiness.ObservedAt
		}
	}
	return earliest, !earliest.IsZero()
}

func servingSurvival(raw RawObservation, startedAt, completedAt time.Time) (bool, bool) {
	if raw.PreTransitionProcess == nil || raw.PostTransitionProcess == nil ||
		raw.PreTransitionProcess.ObservedAt.After(startedAt) ||
		raw.PostTransitionProcess.ObservedAt.Before(completedAt) {
		return false, false
	}
	return raw.PreTransitionProcess.Process == raw.PostTransitionProcess.Process, true
}

func powerSamples(samples []PowerSample) []powerwindow.Sample {
	converted := make([]powerwindow.Sample, len(samples))
	for index, sample := range samples {
		converted[index] = powerwindow.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	return converted
}

func validMappedFact(fact MappedFact) bool {
	return !fact.OccurredAt.IsZero() && !fact.MappedAt.IsZero()
}

func validStateReadBack(readBack StateReadBack) bool {
	return validMappedFact(readBack.Fact) && validState(readBack.State)
}

func validateFailure(failure *Failure, start, end time.Time) string {
	if failure == nil {
		return ""
	}
	if failure.ObservedAt.IsZero() || !within(failure.ObservedAt, start, end) ||
		!plainIdentity(failure.Message) {
		return "is malformed or outside coverage"
	}
	return ""
}

func validatePowerReadFailure(failure PowerReadFailure, start, end time.Time) string {
	if failure.ObservedAt.IsZero() || !within(failure.ObservedAt, start, end) ||
		failure.RequestLatency < 0 || !plainIdentity(failure.Reason) ||
		!plainIdentity(failure.Message) {
		return "is malformed or outside coverage"
	}
	return ""
}

func validObject(identity NodeIdentity) bool {
	return plainIdentity(identity.Name) && plainIdentity(identity.UID)
}

func validActuator(identity ActuatorIdentity) bool {
	return plainIdentity(identity.Kind) && plainIdentity(identity.Target)
}

func validNodeActuation(identity NodeActuationIdentity) bool {
	return plainIdentity(identity.Namespace) && plainIdentity(identity.Name) && plainIdentity(identity.UID)
}

func validState(identity StateIdentity) bool {
	return plainIdentity(identity.DeviceType) && plainIdentity(identity.Model) && plainIdentity(identity.PowerMode)
}

func validWorkload(identity WorkloadIdentity) bool {
	return plainIdentity(identity.Namespace) && plainIdentity(identity.Name) && plainIdentity(identity.UID)
}

func validObserver(identity ObserverIdentity) bool {
	return validObject(identity.Node) && plainIdentity(identity.ImageIdentity)
}

func validConfigurationSource(identity ConfigurationSourceIdentity) bool {
	return plainIdentity(identity.Namespace) && plainIdentity(identity.Name) &&
		plainIdentity(identity.UID) && plainIdentity(identity.ResourceVersion) &&
		plainIdentity(identity.Key)
}

func validMeterSource(identity MeterSourceIdentity) bool {
	if !plainIdentity(identity.Kind) || !plainIdentity(identity.Endpoint) ||
		!plainIdentity(identity.PowerMetric) ||
		!plainIdentity(identity.TimestampAuthority) ||
		!model.IsValidMetricName(model.LabelValue(identity.PowerMetric)) ||
		!validWorkload(identity.GatewayWorkload) ||
		!validConfigurationSource(identity.ConfigurationSource) || len(identity.Labels) == 0 {
		return false
	}
	for name, value := range identity.Labels {
		if !plainIdentity(name) || !model.LabelName(name).IsValid() ||
			!model.LabelValue(value).IsValid() {
			return false
		}
	}
	return true
}

func meterSourceMatches(left, right MeterSourceIdentity) bool {
	if left.Kind != right.Kind || left.Endpoint != right.Endpoint ||
		left.PowerMetric != right.PowerMetric ||
		left.TimestampAuthority != right.TimestampAuthority ||
		left.GatewayWorkload != right.GatewayWorkload ||
		left.ConfigurationSource != right.ConfigurationSource ||
		len(left.Labels) != len(right.Labels) {
		return false
	}
	for name, value := range left.Labels {
		if rightValue, ok := right.Labels[name]; !ok || rightValue != value {
			return false
		}
	}
	return true
}

func validProcess(identity ProcessIdentity) bool {
	return plainIdentity(identity.ContainerName) && plainIdentity(identity.ContainerID) &&
		!identity.StartedAt.IsZero() && identity.RestartCount >= 0
}

func plainIdentity(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func within(value, start, end time.Time) bool {
	return !value.Before(start) && !value.After(end)
}

func cloneRaw(raw RawObservation) RawObservation {
	clone := raw
	clone.MeterSource = cloneMeterSource(raw.MeterSource)
	clone.PowerSamples = append([]PowerSample(nil), raw.PowerSamples...)
	clone.PowerFailures = append([]PowerReadFailure(nil), raw.PowerFailures...)
	clone.Readiness = append([]ReadyObservation(nil), raw.Readiness...)
	if raw.TransitionStart != nil {
		value := *raw.TransitionStart
		clone.TransitionStart = &value
	}
	if raw.ActuationFailure != nil {
		value := *raw.ActuationFailure
		clone.ActuationFailure = &value
	}
	if raw.TargetReadBack != nil {
		value := *raw.TargetReadBack
		clone.TargetReadBack = &value
	}
	if raw.TargetReadBackFailure != nil {
		value := *raw.TargetReadBackFailure
		clone.TargetReadBackFailure = &value
	}
	if raw.PreTransitionProcess != nil {
		value := *raw.PreTransitionProcess
		clone.PreTransitionProcess = &value
	}
	if raw.PostTransitionProcess != nil {
		value := *raw.PostTransitionProcess
		clone.PostTransitionProcess = &value
	}
	return clone
}

func cloneFrozen(frozen FrozenContract) FrozenContract {
	clone := frozen
	clone.MeterSource = cloneMeterSource(frozen.MeterSource)
	return clone
}

func cloneMeterSource(identity MeterSourceIdentity) MeterSourceIdentity {
	clone := identity
	clone.Labels = make(map[string]string, len(identity.Labels))
	for name, value := range identity.Labels {
		clone.Labels[name] = value
	}
	return clone
}
