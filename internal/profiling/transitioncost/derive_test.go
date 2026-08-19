package transitioncost_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

const (
	mode30W       = "MODE_30W"
	otherWorkload = "other-workload"
)

func TestDeriveUsesOneCommonWindowAndKeepsSignedEta(t *testing.T) {
	frozen, raw, policy := validInputs()

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictAccepted, transitioncost.ReasonAccepted)
	observation, ok := result.AcceptedObservation()
	if !ok {
		t.Fatal("accepted result has no observation")
	}
	wantStarted := testTime(2)
	wantCompleted := testTime(6)
	if !observation.StartedAt().Equal(wantStarted) || !observation.CompletedAt().Equal(wantCompleted) {
		t.Fatalf("window = [%v, %v], want [%v, %v]",
			observation.StartedAt(), observation.CompletedAt(), wantStarted, wantCompleted)
	}
	if observation.TauMilliseconds() != 4000 {
		t.Fatalf("tauMilliseconds = %v, want 4000", observation.TauMilliseconds())
	}
	if math.Abs(observation.TransitionWallJoules()-40) > 1e-9 {
		t.Fatalf("transitionWallJoules = %v, want 40", observation.TransitionWallJoules())
	}
	if math.Abs(observation.EtaJoules()-(-8)) > 1e-9 {
		t.Fatalf("etaJoules = %v, want -8", observation.EtaJoules())
	}
	if observation.TargetIdleBaseline() != frozen.TargetIdleBaseline {
		t.Fatalf("target baseline = %#v, want %#v", observation.TargetIdleBaseline(), frozen.TargetIdleBaseline)
	}
	if !observation.ServingSurvived() {
		t.Fatal("servingSurvived = false, want true")
	}
	if observation.BoundaryMappingUncertainty() != 20*time.Millisecond {
		t.Fatalf("boundary uncertainty = %v, want 20ms", observation.BoundaryMappingUncertainty())
	}
	if result.FrozenContract() != frozen {
		t.Fatalf("frozen contract changed: %#v", result.FrozenContract())
	}
	if result.RawObservation().BaselineReference != frozen.TargetIdleBaseline.EvidenceReference {
		t.Fatalf("raw baseline reference = %q, want %q",
			result.RawObservation().BaselineReference, frozen.TargetIdleBaseline.EvidenceReference)
	}
}

func TestDeriveSelectsEarliestFreshReadyObservation(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.Readiness = []transitioncost.ReadyObservation{
		{ObservedAt: testTime(6), Workload: frozen.Workload, Ready: true},
		{ObservedAt: testTime(3), Workload: frozen.Workload, Ready: true},
		{ObservedAt: testTime(5), Workload: frozen.Workload, Ready: true},
	}
	raw.PostTransitionProcess.ObservedAt = testTime(6)

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	observation, ok := result.AcceptedObservation()
	if !ok {
		t.Fatalf("outcome = %s/%s, want accepted", result.Verdict(), result.Reason())
	}
	if !observation.CompletedAt().Equal(testTime(5)) {
		t.Fatalf("completedAt = %v, want earliest valid Ready at %v", observation.CompletedAt(), testTime(5))
	}
	if observation.TauMilliseconds() != 3000 {
		t.Fatalf("tauMilliseconds = %v, want 3000", observation.TauMilliseconds())
	}
}

func TestDeriveClassifiesSourceMismatchInvalid(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.SourceReadBack.State.PowerMode = mode30W

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictInvalid, transitioncost.ReasonSourceStateMismatch)
	assertNoAcceptedObservation(t, result)
}

func TestDeriveClassifiesOperationFailures(t *testing.T) {
	tests := []struct {
		name   string
		alter  func(*transitioncost.RawObservation)
		reason transitioncost.Reason
	}{
		{
			name: "actuation failed",
			alter: func(raw *transitioncost.RawObservation) {
				raw.TargetReadBack = nil
				raw.ActuationFailure = &transitioncost.Failure{ObservedAt: testTime(3), Message: "apply failed"}
			},
			reason: transitioncost.ReasonActuationFailed,
		},
		{
			name: "target read-back failed",
			alter: func(raw *transitioncost.RawObservation) {
				raw.TargetReadBack = nil
				raw.TargetReadBackFailure = &transitioncost.Failure{
					ObservedAt: testTime(4), Message: "read-back failed",
				}
			},
			reason: transitioncost.ReasonTargetReadBackFailed,
		},
		{
			name: "wrong target read-back",
			alter: func(raw *transitioncost.RawObservation) {
				raw.TargetReadBack.State.PowerMode = mode30W
			},
			reason: transitioncost.ReasonTargetStateMismatch,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			test.alter(&raw)
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, result, transitioncost.VerdictFailed, test.reason)
			assertNoAcceptedObservation(t, result)
		})
	}
}

func TestDeriveNeverClosesOnStaleFailedOrLateReadiness(t *testing.T) {
	tests := []struct {
		name      string
		readiness []transitioncost.ReadyObservation
	}{
		{
			name: "pre-transition Ready is stale",
			readiness: []transitioncost.ReadyObservation{{
				ObservedAt: testTime(3), Ready: true,
			}},
		},
		{
			name: "post-read-back Ready false",
			readiness: []transitioncost.ReadyObservation{{
				ObservedAt: testTime(5), Ready: false,
			}},
		},
		{
			name: "Ready observation exceeds timing bound",
			readiness: []transitioncost.ReadyObservation{{
				ObservedAt: testTime(8), Ready: true,
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			for index := range test.readiness {
				test.readiness[index].Workload = frozen.Workload
			}
			raw.Readiness = test.readiness
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, result, transitioncost.VerdictFailed, transitioncost.ReasonReadinessNotReached)
			assertNoAcceptedObservation(t, result)
		})
	}
}

func TestDeriveUsesExactProcessIdentityForMeasuredServingInterruption(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*transitioncost.ProcessIdentity)
	}{
		{name: "container ID", alter: func(process *transitioncost.ProcessIdentity) { process.ContainerID = "container-b" }},
		{name: "process start", alter: func(process *transitioncost.ProcessIdentity) { process.StartedAt = testTime(5) }},
		{name: "restart count", alter: func(process *transitioncost.ProcessIdentity) { process.RestartCount++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			test.alter(&raw.PostTransitionProcess.Process)
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			observation, ok := result.AcceptedObservation()
			if !ok {
				t.Fatalf("outcome = %s/%s, want accepted", result.Verdict(), result.Reason())
			}
			if observation.ServingSurvived() {
				t.Fatalf("servingSurvived = true after %s changed, want false", test.name)
			}
		})
	}
}

func TestDeriveRequiresTrustworthyServingSurvivalEvidence(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.PostTransitionProcess = nil

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictInsufficient, transitioncost.ReasonServingSurvivalUnknown)
	assertNoAcceptedObservation(t, result)
}

func TestDeriveClassifiesPowerQualityInsufficient(t *testing.T) {
	tests := []struct {
		name   string
		alter  func(*transitioncost.RawObservation, *transitioncost.Policy)
		reason transitioncost.Reason
	}{
		{
			name: "sample gap",
			alter: func(raw *transitioncost.RawObservation, policy *transitioncost.Policy) {
				raw.PowerSamples = []transitioncost.PowerSample{
					{ObservedAt: testTimeMillis(2100), Watts: 10},
					{ObservedAt: testTime(3), Watts: 10},
					{ObservedAt: testTimeMillis(5900), Watts: 10},
				}
				policy.MaximumSampleGap = 2 * time.Second
			},
			reason: transitioncost.ReasonPowerWindowInsufficient,
		},
		{
			name: "coverage",
			alter: func(raw *transitioncost.RawObservation, policy *transitioncost.Policy) {
				raw.PowerSamples = []transitioncost.PowerSample{
					{ObservedAt: testTimeMillis(2500), Watts: 10},
					{ObservedAt: testTimeMillis(5500), Watts: 10},
				}
				policy.MaximumBoundaryDistance = 500 * time.Millisecond
				policy.MinimumCoverage = 0.9
				policy.MaximumSampleGap = 4 * time.Second
			},
			reason: transitioncost.ReasonPowerWindowInsufficient,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			test.alter(&raw, &policy)
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, result, transitioncost.VerdictInsufficient, test.reason)
			assertNoAcceptedObservation(t, result)
		})
	}
}

func TestDeriveRetainsFailedPowerReadsWhenWindowQualityIsSufficient(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.PowerFailures = []transitioncost.Failure{{ObservedAt: testTime(5), Message: "one meter timeout"}}

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictAccepted, transitioncost.ReasonAccepted)
	if len(result.RawObservation().PowerFailures) != 1 ||
		result.RawObservation().PowerFailures[0].Message != "one meter timeout" {
		t.Fatalf("failed reads not retained: %#v", result.RawObservation().PowerFailures)
	}
}

func TestDeriveRejectsExcessiveBoundaryUncertainty(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.BoundaryMappingUncertainty = 51 * time.Millisecond

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictInsufficient,
		transitioncost.ReasonBoundaryUncertaintyExceeded)
}

func TestDeriveRejectsEveryFrozenIdentityContradiction(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*transitioncost.RawObservation)
	}{
		{name: "Node", alter: func(raw *transitioncost.RawObservation) { raw.Node.UID = "other-node" }},
		{
			name:  "NodeActuation",
			alter: func(raw *transitioncost.RawObservation) { raw.NodeActuation.UID = "other-actuation" },
		},
		{
			name:  "source state",
			alter: func(raw *transitioncost.RawObservation) { raw.SourceState.PowerMode = mode30W },
		},
		{
			name:  "target state",
			alter: func(raw *transitioncost.RawObservation) { raw.TargetState.PowerMode = mode30W },
		},
		{name: "workload", alter: func(raw *transitioncost.RawObservation) { raw.Workload.UID = otherWorkload }},
		{name: "protocol", alter: func(raw *transitioncost.RawObservation) { raw.ProtocolIdentity = "other-protocol" }},
		{name: "meter", alter: func(raw *transitioncost.RawObservation) { raw.MeterIdentity = "other-meter" }},
		{
			name:  "baseline evidence",
			alter: func(raw *transitioncost.RawObservation) { raw.BaselineReference = "evidence://other" },
		},
		{
			name:  "baseline state",
			alter: func(raw *transitioncost.RawObservation) { raw.BaselineState.PowerMode = "MODE_15W" },
		},
		{
			name:  "readiness workload",
			alter: func(raw *transitioncost.RawObservation) { raw.Readiness[0].Workload.UID = otherWorkload },
		},
		{
			name:  "process workload",
			alter: func(raw *transitioncost.RawObservation) { raw.PostTransitionProcess.Workload.UID = otherWorkload },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			test.alter(&raw)
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, result, transitioncost.VerdictInvalid,
				transitioncost.ReasonIdentityContradiction)
			assertNoAcceptedObservation(t, result)
		})
	}
}

func TestDeriveRejectsMalformedFrozenInputsAsErrors(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*transitioncost.FrozenContract, *transitioncost.Policy)
		match string
	}{
		{
			name: "baseline belongs to another state",
			alter: func(frozen *transitioncost.FrozenContract, _ *transitioncost.Policy) {
				frozen.TargetIdleBaseline.State.PowerMode = "MODE_15W"
			},
			match: "baseline state",
		},
		{
			name: "same source and target",
			alter: func(frozen *transitioncost.FrozenContract, _ *transitioncost.Policy) {
				frozen.TargetState = frozen.SourceState
				frozen.TargetIdleBaseline.State = frozen.SourceState
			},
			match: "distinct power modes",
		},
		{
			name: "invalid policy",
			alter: func(_ *transitioncost.FrozenContract, policy *transitioncost.Policy) {
				policy.MinimumCoverage = 1.1
			},
			match: "policy bounds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			test.alter(&frozen, &policy)
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Derive() error = %v, want containing %q", err, test.match)
			}
			if result.Verdict() != "" {
				t.Fatalf("result verdict = %q on caller error, want empty", result.Verdict())
			}
		})
	}
}

func TestDeriveReturnsMalformedRawAsTypedInvalid(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.ObservationEndedAt = raw.ObservationStartedAt

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictInvalid, transitioncost.ReasonObservationMalformed)
}

func TestResultOwnsRawEvidenceCopy(t *testing.T) {
	frozen, raw, policy := validInputs()
	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	raw.PowerSamples[0].Watts = 999
	raw.Readiness[0].Ready = false
	raw.TransitionStart.MappedAt = testTime(7)

	retained := result.RawObservation()
	if retained.PowerSamples[0].Watts != 10 || !retained.Readiness[0].Ready ||
		!retained.TransitionStart.MappedAt.Equal(testTime(2)) {
		t.Fatalf("retained raw observation aliases caller input: %#v", retained)
	}
	retained.PowerSamples[0].Watts = 777
	if result.RawObservation().PowerSamples[0].Watts != 10 {
		t.Fatal("RawObservation accessor aliases retained evidence")
	}
}

func assertOutcome(t *testing.T, result transitioncost.Result, verdict transitioncost.Verdict, reason transitioncost.Reason) {
	t.Helper()
	if result.Verdict() != verdict || result.Reason() != reason {
		t.Fatalf("outcome = %s/%s (%s), want %s/%s",
			result.Verdict(), result.Reason(), result.Detail(), verdict, reason)
	}
}

func assertNoAcceptedObservation(t *testing.T, result transitioncost.Result) {
	t.Helper()
	if _, ok := result.AcceptedObservation(); ok {
		t.Fatalf("%s/%s unexpectedly contains accepted values", result.Verdict(), result.Reason())
	}
}

func validInputs() (transitioncost.FrozenContract, transitioncost.RawObservation, transitioncost.Policy) {
	source := transitioncost.StateIdentity{DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MODE_15W"}
	target := transitioncost.StateIdentity{DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MAXN"}
	workload := transitioncost.WorkloadIdentity{Namespace: "serving", Name: "mobilenet", UID: "workload-uid"}
	frozen := transitioncost.FrozenContract{
		Node:          transitioncost.NodeIdentity{Name: "orin", UID: "node-uid"},
		NodeActuation: transitioncost.NodeActuationIdentity{Namespace: "chill-system", Name: "to-maxn", UID: "actuation-uid"},
		SourceState:   source, TargetState: target, Workload: workload,
		ProtocolIdentity: "transition-cost-v1", MeterIdentity: "wall-meter-1",
		TargetIdleBaseline: transitioncost.IdleBaseline{
			EvidenceReference: "evidence://idle-maxn", State: target, MeanWallWatts: 12,
		},
	}
	process := transitioncost.ProcessIdentity{
		ContainerName: "server", ContainerID: "container-a", StartedAt: testTime(-20), RestartCount: 0,
	}
	raw := transitioncost.RawObservation{
		ObservationStartedAt: testTime(0), ObservationEndedAt: testTime(8),
		Node: frozen.Node, NodeActuation: frozen.NodeActuation,
		SourceState: source, TargetState: target, Workload: workload,
		ProtocolIdentity: frozen.ProtocolIdentity, MeterIdentity: frozen.MeterIdentity,
		BaselineReference: frozen.TargetIdleBaseline.EvidenceReference,
		BaselineState:     frozen.TargetIdleBaseline.State,
		SourceReadBack: transitioncost.StateReadBack{
			Fact: transitioncost.MappedFact{OccurredAt: testTime(1), MappedAt: testTime(1)}, State: source,
		},
		TransitionStart: &transitioncost.MappedFact{OccurredAt: testTime(2), MappedAt: testTime(2)},
		TargetReadBack: &transitioncost.StateReadBack{
			Fact: transitioncost.MappedFact{OccurredAt: testTime(4), MappedAt: testTime(4)}, State: target,
		},
		PowerSamples: []transitioncost.PowerSample{
			{ObservedAt: testTimeMillis(2100), Watts: 10},
			{ObservedAt: testTime(3), Watts: 10},
			{ObservedAt: testTime(4), Watts: 10},
			{ObservedAt: testTimeMillis(4900), Watts: 10},
			{ObservedAt: testTimeMillis(5900), Watts: 10},
		},
		Readiness: []transitioncost.ReadyObservation{
			{ObservedAt: testTime(6), Workload: workload, Ready: true},
		},
		PreTransitionProcess: &transitioncost.ProcessObservation{
			ObservedAt: testTime(1), Workload: workload, Process: process,
		},
		PostTransitionProcess: &transitioncost.ProcessObservation{
			ObservedAt: testTime(7), Workload: workload, Process: process,
		},
		BoundaryMappingUncertainty: 20 * time.Millisecond,
	}
	policy := transitioncost.Policy{
		MaximumBoundaryDistance:      200 * time.Millisecond,
		MinimumCoverage:              0.9,
		MaximumSampleGap:             1100 * time.Millisecond,
		MaximumBoundaryUncertainty:   50 * time.Millisecond,
		MaximumReadyObservationDelay: 3 * time.Second,
	}
	return frozen, raw, policy
}

func testTime(seconds int) time.Time {
	return time.Date(2026, 8, 20, 0, 0, seconds, 0, time.UTC)
}

func testTimeMillis(milliseconds int) time.Time {
	return testTime(0).Add(time.Duration(milliseconds) * time.Millisecond)
}
