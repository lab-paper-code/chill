package transitioncost

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

// Derive validates one frozen contract and one factual raw observation, then
// produces one terminal scientific result. Malformed frozen inputs are caller
// errors; terminal properties of retained raw evidence are typed results.
func Derive(frozen FrozenContract, raw RawObservation, policy Policy) (FinalizedObservation, error) {
	if err := validateFrozen(frozen); err != nil {
		return FinalizedObservation{}, fmt.Errorf("validate frozen transition contract: %w", err)
	}
	if err := validatePolicy(policy); err != nil {
		return FinalizedObservation{}, fmt.Errorf("validate transition-cost policy: %w", err)
	}

	verified := verifiedInput{frozen: frozen, raw: raw, policy: policy}
	if detail := validateRawStructure(verified.raw); detail != "" {
		return classified(verified, VerdictInvalid, ReasonObservationMalformed, detail), nil
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
	if generation := verified.raw.Generation; generation != nil && generation.End-generation.Start != 1 {
		// Frozen modes are distinct, so the window holds exactly its own change.
		return classified(verified, VerdictInvalid, ReasonPowerChangedDuringMeasurement, fmt.Sprintf(
			"Node power-mode generation went from %d to %d, expected one change",
			generation.Start, generation.End)), nil
	}

	if verified.raw.PostTransitionProcess == nil {
		return classified(verified, VerdictFailed, ReasonReadinessNotReached,
			"no fresh Ready=True process witness followed target read-back"), nil
	}
	startedAt := verified.raw.MarkerBrackets.ApplyStartedAt.FirstPresentReceivedAt
	targetReadBackAt := verified.raw.MarkerBrackets.TargetObservation.FirstPresentReceivedAt
	completedAt := verified.raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt
	if completedAt.Sub(targetReadBackAt) > verified.policy.MaximumReadyObservationDelay {
		return classified(verified, VerdictFailed, ReasonReadinessNotReached,
			"fresh Ready=True process witness exceeds the timing bound"), nil
	}
	boundaryUncertainty, representable := measuredBoundaryMappingUncertainty(
		verified.raw.MarkerBrackets,
	)
	if !representable {
		return classified(verified, VerdictInvalid, ReasonObservationMalformed,
			"boundary mapping uncertainty cannot be represented in milliseconds"), nil
	}
	if boundaryUncertainty > verified.policy.MaximumBoundaryUncertainty {
		return classified(verified, VerdictInsufficient, ReasonBoundaryUncertaintyExceeded,
			"boundary mapping uncertainty exceeds policy"), nil
	}

	costWindowEndedAt := completedAt.Add(time.Duration(frozen.SettlingSeconds) * time.Second)
	if costWindowEndedAt.After(raw.ObservationEndedAt) {
		return classified(verified, VerdictInsufficient, ReasonPowerWindowInsufficient,
			"raw observation does not cover the declared post-Ready settling window"), nil
	}

	joules, err := powerwindow.Integrate(powerwindow.Window{
		Start:   startedAt,
		End:     costWindowEndedAt,
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

	servingSurvived, known := servingSurvival(verified.raw, startedAt)
	if !known {
		return classified(verified, VerdictInsufficient, ReasonServingSurvivalUnknown,
			"pre-transition and post-completion process identity is incomplete"), nil
	}
	if err := VerifyNativeTransition(verified.raw.BeforeNative, verified.raw.AfterNative); err != nil {
		return classified(verified, VerdictInsufficient, ReasonNativeStateUnverified, err.Error()), nil
	}
	for index := range verified.raw.NativeObservations {
		if err := VerifyNativeTransition(verified.raw.BeforeNative, &verified.raw.NativeObservations[index]); err != nil {
			return classified(verified, VerdictInsufficient, ReasonNativeStateUnverified, err.Error()), nil
		}
	}
	if verified.raw.BeforeNative.ReceivedAt.After(startedAt) || verified.raw.AfterNative.ReceivedAt.Before(costWindowEndedAt) {
		return classified(verified, VerdictInsufficient, ReasonNativeStateUnverified, "native zero-inference observations do not enclose the energy-cost window"), nil
	}
	if !nativeProcessObserved(raw.BeforeNative, &raw.PreTransitionProcess.Process) || !nativeProcessObserved(raw.AfterNative, raw.PostTransitionProcess) {
		return classified(verified, VerdictInsufficient, ReasonNativeStateUnverified, "process witness does not match actual native physical incarnation"), nil
	}

	tauSeconds := completedAt.Sub(startedAt).Seconds()
	return FinalizedObservation{record: FinalizedObservationRecord{
		Verdict: VerdictAccepted, Reason: ReasonAccepted, FrozenContract: frozen,
		AcceptedObservation: &FinalizedAcceptedObservationRecord{
			StartedAt: startedAt, CompletedAt: completedAt,
			BoundaryMappingUncertaintyMilliseconds: boundaryUncertainty.Milliseconds(),
			TauMilliseconds:                        tauSeconds * 1000, TransitionWallJoules: joules,
			EtaJoules:       joules - frozen.TargetIdleBaseline.MeanWallWatts*costWindowEndedAt.Sub(startedAt).Seconds(),
			ServingSurvived: servingSurvived,
			BeforeNative:    cloneNativeObservation(raw.BeforeNative),
			AfterNative:     cloneNativeObservation(raw.AfterNative),
		},
	}}, nil
}

// verifiedInput can only be constructed after FrozenContract and Policy cross
// their validation boundary.
type verifiedInput struct {
	frozen FrozenContract
	raw    RawObservation
	policy Policy
}

func classified(input verifiedInput, verdict Verdict, reason Reason, detail string) FinalizedObservation {
	return FinalizedObservation{record: FinalizedObservationRecord{
		Verdict: verdict, Reason: reason, Detail: detail, FrozenContract: input.frozen,
	}}
}

func validateFrozen(frozen FrozenContract) error {
	if frozen.SettlingSeconds < 0 || frozen.SettlingSeconds > 3600 {
		return errors.New("settling seconds must be in [0, 3600]")
	}
	if !validState(frozen.SourceState) || !validState(frozen.TargetState) ||
		!validText(frozen.TargetIdleBaseline.EvidenceReference) {
		return errors.New("frozen transition inputs are incomplete or contain surrounding whitespace")
	}
	if frozen.SourceState.DeviceType != frozen.TargetState.DeviceType ||
		frozen.SourceState.Model != frozen.TargetState.Model ||
		frozen.SourceState.PowerMode == frozen.TargetState.PowerMode {
		return errors.New("source and target must be distinct power modes of one exact device type and model")
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
	if !validStateReadBack(raw.SourceReadBack) {
		return "source read-back is malformed"
	}
	if !within(raw.SourceReadBack.Fact.MappedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) {
		return "source read-back is outside raw observation coverage"
	}
	if detail := validateMarkerBrackets(raw); detail != "" {
		return detail
	}
	return ""
}

func validateMarkerBrackets(raw RawObservation) string {
	checks := []struct {
		name    string
		bracket ReceiptTimeBracket
		present bool
	}{
		{name: "applyStartedAt", bracket: raw.MarkerBrackets.ApplyStartedAt,
			present: raw.TransitionStart != nil},
		{name: "targetObservation", bracket: raw.MarkerBrackets.TargetObservation,
			present: raw.TargetReadBack != nil},
		{name: "readyProcessWitness", bracket: raw.MarkerBrackets.ReadyProcessWitness,
			present: raw.PostTransitionProcess != nil},
	}
	for _, check := range checks {
		if !validMarkerBracket(
			check.bracket,
			check.present,
			raw.ObservationStartedAt,
			raw.ObservationEndedAt,
		) {
			return check.name + " receipt-time bracket is malformed or outside coverage"
		}
	}
	apply := raw.MarkerBrackets.ApplyStartedAt.FirstPresentReceivedAt
	target := raw.MarkerBrackets.TargetObservation.FirstPresentReceivedAt
	ready := raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt
	if !apply.IsZero() && apply.Before(raw.SourceReadBack.Fact.MappedAt) {
		return "applyStartedAt receipt precedes source read-back"
	}
	if !target.IsZero() && (apply.IsZero() || target.Before(apply)) {
		return "targetObservation receipt precedes applyStartedAt"
	}
	if !ready.IsZero() && (target.IsZero() || !ready.After(target)) {
		return "ready-process receipt does not follow targetObservation"
	}
	return ""
}

func validMarkerBracket(bracket ReceiptTimeBracket, present bool, start, end time.Time) bool {
	if bracket.LastAbsentReceivedAt.IsZero() ||
		!within(bracket.LastAbsentReceivedAt, start, end) {
		return false
	}
	if !present {
		return bracket.FirstPresentReceivedAt.IsZero()
	}
	return !bracket.FirstPresentReceivedAt.IsZero() &&
		bracket.FirstPresentReceivedAt.After(bracket.LastAbsentReceivedAt) &&
		within(bracket.FirstPresentReceivedAt, start, end)
}

func validateTransitionFacts(raw RawObservation) string {
	if raw.TransitionStart != nil {
		if raw.TransitionStart.OccurredAt.IsZero() {
			return "transition-start fact is malformed"
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
		if raw.TransitionStart == nil || raw.TargetReadBack.Fact.OccurredAt.IsZero() ||
			!validState(raw.TargetReadBack.State) {
			return "target read-back is malformed or unstarted"
		}
	}
	return ""
}

func validateObservedFacts(raw RawObservation) string {
	for _, sample := range raw.PowerSamples {
		if sample.ObservedAt.IsZero() || !finiteNonNegative(sample.RequestLatencySeconds) ||
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
	if raw.PreTransitionProcess != nil &&
		(raw.PreTransitionProcess.ObservedAt.IsZero() ||
			!within(raw.PreTransitionProcess.ObservedAt, raw.ObservationStartedAt, raw.ObservationEndedAt) ||
			!validProcess(raw.PreTransitionProcess.Process)) {
		return "pre-transition process observation is malformed or outside coverage"
	}
	if raw.PostTransitionProcess != nil && !validProcess(*raw.PostTransitionProcess) {
		return "post-transition process observation is malformed"
	}
	return ""
}

func servingSurvival(raw RawObservation, startedAt time.Time) (bool, bool) {
	if raw.PreTransitionProcess == nil || raw.PostTransitionProcess == nil ||
		raw.PreTransitionProcess.ObservedAt.After(startedAt) {
		return false, false
	}
	return raw.PreTransitionProcess.Process == *raw.PostTransitionProcess, true
}

func measuredBoundaryMappingUncertainty(
	brackets TransitionMarkerBrackets,
) (time.Duration, bool) {
	maximum := time.Duration(0)
	for _, bracket := range []ReceiptTimeBracket{
		brackets.ApplyStartedAt,
		brackets.TargetObservation,
		brackets.ReadyProcessWitness,
	} {
		width := bracket.FirstPresentReceivedAt.Sub(bracket.LastAbsentReceivedAt)
		if width > maximum {
			maximum = width
		}
	}
	if remainder := maximum % time.Millisecond; remainder != 0 {
		increment := time.Millisecond - remainder
		if maximum > time.Duration(1<<63-1)-increment {
			return 0, false
		}
		maximum += increment
	}
	return maximum, true
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
		!validText(failure.Message) {
		return "is malformed or outside coverage"
	}
	return ""
}

func validatePowerReadFailure(failure PowerReadFailure, start, end time.Time) string {
	if failure.ObservedAt.IsZero() || !within(failure.ObservedAt, start, end) ||
		!finiteNonNegative(failure.RequestLatencySeconds) || !validText(failure.Reason) ||
		!validText(failure.Message) {
		return "is malformed or outside coverage"
	}
	return ""
}

func validState(state StateIdentity) bool {
	return validText(state.DeviceType) && validText(state.Model) && validText(state.PowerMode)
}

func validProcess(process ProcessIdentity) bool {
	return validText(process.ContainerName) && validText(process.ContainerID) &&
		!process.StartedAt.IsZero() && process.RestartCount >= 0
}

func validText(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func within(value, start, end time.Time) bool {
	return !value.Before(start) && !value.After(end)
}
