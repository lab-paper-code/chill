package transitioncost_test

import (
	"math"
	"strings"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

const mode30W = "MODE_30W"

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
	if observation.BoundaryMappingUncertainty() != 30*time.Millisecond {
		t.Fatalf("boundary uncertainty = %v, want 30ms", observation.BoundaryMappingUncertainty())
	}
	if result.FrozenContract() != frozen {
		t.Fatalf("frozen contract changed: %#v", result.FrozenContract())
	}
}

func TestDeriveUsesObserverReceiptsDespiteTargetClockOffset(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.TransitionStart.OccurredAt = testTime(200)
	raw.TargetReadBack.Fact.OccurredAt = testTime(400)

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	observation, ok := result.AcceptedObservation()
	if !ok {
		t.Fatalf("outcome = %s/%s, want accepted", result.Verdict(), result.Reason())
	}
	if !observation.CompletedAt().Equal(testTime(6)) {
		t.Fatalf("completedAt = %v, want bracketed witness at %v", observation.CompletedAt(), testTime(6))
	}
	if observation.TauMilliseconds() != 4000 {
		t.Fatalf("tauMilliseconds = %v, want 4000", observation.TauMilliseconds())
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
				raw.MarkerBrackets.TargetObservation.FirstPresentReceivedAt = time.Time{}
				raw.PostTransitionProcess = nil
				raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt = time.Time{}
				raw.ActuationFailure = &transitioncost.Failure{ObservedAt: testTime(3), Message: "apply failed"}
			},
			reason: transitioncost.ReasonActuationFailed,
		},
		{
			name: "target read-back failed",
			alter: func(raw *transitioncost.RawObservation) {
				raw.TargetReadBack = nil
				raw.MarkerBrackets.TargetObservation.FirstPresentReceivedAt = time.Time{}
				raw.PostTransitionProcess = nil
				raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt = time.Time{}
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

func TestDeriveRequiresFreshTimelyReadyProcessWitness(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*transitioncost.RawObservation)
	}{
		{
			name: "no fresh witness",
			alter: func(raw *transitioncost.RawObservation) {
				raw.PostTransitionProcess = nil
				raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt = time.Time{}
			},
		},
		{
			name: "witness exceeds timing bound",
			alter: func(raw *transitioncost.RawObservation) {
				raw.MarkerBrackets.ReadyProcessWitness = transitioncost.ReceiptTimeBracket{
					LastAbsentReceivedAt:   testTimeMillis(7990),
					FirstPresentReceivedAt: testTime(8),
				}
			},
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
			test.alter(raw.PostTransitionProcess)
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, result, transitioncost.VerdictInsufficient, transitioncost.ReasonNativeStateUnverified)
			assertNoAcceptedObservation(t, result)
		})
	}
}

func TestDeriveRequiresTrustworthyServingSurvivalEvidence(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.PreTransitionProcess = nil

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
		{
			name: "non-finite integral from finite samples",
			alter: func(raw *transitioncost.RawObservation, _ *transitioncost.Policy) {
				for index := range raw.PowerSamples {
					raw.PowerSamples[index].Watts = math.MaxFloat64
				}
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
	wantFailure := transitioncost.PowerReadFailure{
		ObservedAt: testTime(5), RequestLatencySeconds: 0.125,
		Reason: "RequestTimeout", Message: "one meter timeout",
	}
	raw.PowerFailures = []transitioncost.PowerReadFailure{wantFailure}

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictAccepted, transitioncost.ReasonAccepted)
	if len(raw.PowerFailures) != 1 ||
		raw.PowerFailures[0] != wantFailure {
		t.Fatalf("failed reads not retained: %#v", raw.PowerFailures)
	}
}

func TestDeriveRejectsExcessiveBoundaryUncertainty(t *testing.T) {
	frozen, raw, policy := validInputs()
	raw.MarkerBrackets.ApplyStartedAt.LastAbsentReceivedAt = testTimeMillis(1949)

	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	assertOutcome(t, result, transitioncost.VerdictInsufficient,
		transitioncost.ReasonBoundaryUncertaintyExceeded)
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
			name: "source state is incomplete",
			alter: func(frozen *transitioncost.FrozenContract, _ *transitioncost.Policy) {
				frozen.SourceState.DeviceType = ""
			},
			match: "frozen transition inputs",
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
			name: "negative target idle baseline",
			alter: func(frozen *transitioncost.FrozenContract, _ *transitioncost.Policy) {
				frozen.TargetIdleBaseline.MeanWallWatts = -1
			},
			match: "baseline watts",
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
	tests := []struct {
		name  string
		alter func(*transitioncost.RawObservation)
	}{
		{
			name:  "invalid coverage window",
			alter: func(raw *transitioncost.RawObservation) { raw.ObservationEndedAt = raw.ObservationStartedAt },
		},
		{
			name: "negative power sample latency",
			alter: func(raw *transitioncost.RawObservation) {
				raw.PowerSamples[0].RequestLatencySeconds = -0.001
			},
		},
		{
			name: "non-finite power sample latency",
			alter: func(raw *transitioncost.RawObservation) {
				raw.PowerSamples[0].RequestLatencySeconds = math.NaN()
			},
		},
		{
			name:  "non-finite power sample",
			alter: func(raw *transitioncost.RawObservation) { raw.PowerSamples[0].Watts = math.NaN() },
		},
		{
			name:  "negative power sample",
			alter: func(raw *transitioncost.RawObservation) { raw.PowerSamples[0].Watts = -1 },
		},
		{
			name: "empty power failure reason",
			alter: func(raw *transitioncost.RawObservation) {
				raw.PowerFailures = []transitioncost.PowerReadFailure{{
					ObservedAt: testTime(5), RequestLatencySeconds: 0.001, Message: "meter unavailable",
				}}
			},
		},
		{
			name: "padded power failure message",
			alter: func(raw *transitioncost.RawObservation) {
				raw.PowerFailures = []transitioncost.PowerReadFailure{{
					ObservedAt: testTime(5), RequestLatencySeconds: 0.001,
					Reason: "Unavailable", Message: " meter unavailable",
				}}
			},
		},
		{
			name: "negative power failure latency",
			alter: func(raw *transitioncost.RawObservation) {
				raw.PowerFailures = []transitioncost.PowerReadFailure{{
					ObservedAt: testTime(5), RequestLatencySeconds: -0.000000001,
					Reason: "Unavailable", Message: "meter unavailable",
				}}
			},
		},
		{
			name: "missing marker last-absent receipt",
			alter: func(raw *transitioncost.RawObservation) {
				raw.MarkerBrackets.ApplyStartedAt.LastAbsentReceivedAt = time.Time{}
			},
		},
		{
			name: "reversed marker bracket",
			alter: func(raw *transitioncost.RawObservation) {
				raw.MarkerBrackets.TargetObservation.LastAbsentReceivedAt = testTimeMillis(4010)
			},
		},
		{
			name: "ready marker precedes target marker",
			alter: func(raw *transitioncost.RawObservation) {
				raw.MarkerBrackets.ReadyProcessWitness = transitioncost.ReceiptTimeBracket{
					LastAbsentReceivedAt:   testTimeMillis(3900),
					FirstPresentReceivedAt: testTimeMillis(3990),
				}
			},
		},
		{
			name: "process without ready receipt",
			alter: func(raw *transitioncost.RawObservation) {
				raw.MarkerBrackets.ReadyProcessWitness.FirstPresentReceivedAt = time.Time{}
			},
		},
		{
			name: "non-finite power failure latency",
			alter: func(raw *transitioncost.RawObservation) {
				raw.PowerFailures = []transitioncost.PowerReadFailure{{
					ObservedAt: testTime(5), RequestLatencySeconds: math.Inf(1),
					Reason: "Unavailable", Message: "meter unavailable",
				}}
			},
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
				transitioncost.ReasonObservationMalformed)
		})
	}
}

func TestDerivedResultDoesNotAliasRawEvidence(t *testing.T) {
	frozen, raw, policy := validInputs()
	result, err := transitioncost.Derive(frozen, raw, policy)
	if err != nil {
		t.Fatal(err)
	}
	raw.PowerSamples[0].Watts = 999
	raw.PostTransitionProcess.ContainerID = "changed"
	raw.MarkerBrackets.ApplyStartedAt.FirstPresentReceivedAt = testTime(7)
	observation, ok := result.AcceptedObservation()
	if !ok || observation.TauMilliseconds() != 4000 || observation.TransitionWallJoules() != 40 ||
		!observation.ServingSurvived() || !observation.StartedAt().Equal(testTime(2)) {
		t.Fatalf("derived values changed with raw input: %+v", result.Record())
	}
}

func assertOutcome(t *testing.T, result transitioncost.FinalizedObservation, verdict transitioncost.Verdict, reason transitioncost.Reason) {
	t.Helper()
	if result.Verdict() != verdict || result.Reason() != reason {
		t.Fatalf("outcome = %s/%s (%s), want %s/%s",
			result.Verdict(), result.Reason(), result.Detail(), verdict, reason)
	}
}

func assertNoAcceptedObservation(t *testing.T, result transitioncost.FinalizedObservation) {
	t.Helper()
	if _, ok := result.AcceptedObservation(); ok {
		t.Fatalf("%s/%s unexpectedly contains accepted values", result.Verdict(), result.Reason())
	}
}

func validInputs() (transitioncost.FrozenContract, transitioncost.RawObservation, transitioncost.Policy) {
	source := transitioncost.StateIdentity{DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MODE_15W"}
	target := transitioncost.StateIdentity{DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MAXN"}
	frozen := transitioncost.FrozenContract{
		SourceState: source,
		TargetState: target,
		TargetIdleBaseline: transitioncost.IdleBaseline{
			EvidenceReference: "evidence://idle-maxn", State: target, MeanWallWatts: 12,
		},
	}
	process := transitioncost.ProcessIdentity{
		ContainerName: "firstfloor", ContainerID: "container-a", StartedAt: testTime(-20), RestartCount: 0,
	}
	raw := transitioncost.RawObservation{
		ObservationStartedAt: testTime(0), ObservationEndedAt: testTime(8),
		SourceReadBack: transitioncost.StateReadBack{
			Fact: transitioncost.MappedFact{OccurredAt: testTime(1), MappedAt: testTime(1)}, State: source,
		},
		TransitionStart: &transitioncost.OccurredFact{OccurredAt: testTimeMillis(1500)},
		TargetReadBack: &transitioncost.TargetStateReadBack{
			Fact: transitioncost.OccurredFact{OccurredAt: testTimeMillis(3500)}, State: target,
		},
		MarkerBrackets: transitioncost.TransitionMarkerBrackets{
			ApplyStartedAt: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   testTimeMillis(1990),
				FirstPresentReceivedAt: testTime(2),
			},
			TargetObservation: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   testTimeMillis(3980),
				FirstPresentReceivedAt: testTime(4),
			},
			ReadyProcessWitness: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   testTime(6).Add(-29500 * time.Microsecond),
				FirstPresentReceivedAt: testTime(6),
			},
		},
		PowerSamples: []transitioncost.PowerSample{
			{ObservedAt: testTimeMillis(2100), RequestLatencySeconds: 0.01, Watts: 10},
			{ObservedAt: testTime(3), RequestLatencySeconds: 0.01, Watts: 10},
			{ObservedAt: testTime(4), RequestLatencySeconds: 0.01, Watts: 10},
			{ObservedAt: testTimeMillis(4900), RequestLatencySeconds: 0.01, Watts: 10},
			{ObservedAt: testTimeMillis(5900), RequestLatencySeconds: 0.01, Watts: 10},
		},
		PreTransitionProcess: &transitioncost.ProcessObservation{
			ObservedAt: testTime(1), Process: process,
		},
		PostTransitionProcess: &process,
	}
	nativeWitness := func(at time.Time) *transitioncost.NativeNodeObservation {
		return &transitioncost.NativeNodeObservation{NodeName: "target", NodeUID: "target-uid", ReceivedAt: at, Pods: []string{"measure/serving/pod"}, Instances: []transitioncost.NativeInstanceObservation{{Namespace: "measure", PodName: "serving", PodUID: "pod", ContainerID: process.ContainerID, StartedAt: process.StartedAt, Image: "fixture-image", ImageID: "fixture-image-id", CPURequestMilli: 1000, CPULimitMilli: 1000, MemoryRequestBytes: 128, MemoryLimitBytes: 128, State: native.State{At: testTime(1), Ready: true, Metadata: native.Metadata{Mode: "serving", ProfileID: "profile", Runtime: native.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "fixture", ModelPath: "/model/model.onnx", CPUAffinity: []int{0}, IntraOpThreads: 1}}}}}}
	}
	raw.BeforeNative = nativeWitness(testTime(1))
	raw.AfterNative = nativeWitness(testTime(6))
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
func TestDeriveRequiresActualNativeZeroInferenceAndAllocationInvariance(t *testing.T) {
	for _, name := range []string{"missing native", "inference between empty queues", "allocation changed", "node replaced"} {
		t.Run(name, func(t *testing.T) {
			frozen, raw, policy := validInputs()
			switch name {
			case "missing native":
				raw.AfterNative = nil
			case "inference between empty queues":
				raw.AfterNative.Instances[0].State.AcceptedRequests++
				raw.AfterNative.Instances[0].State.CompletedRequests++
				raw.AfterNative.Instances[0].State.Executions++
			case "allocation changed":
				raw.AfterNative.Instances[0].MemoryLimitBytes++
			case "node replaced":
				raw.AfterNative.NodeUID = "replacement"
			}
			result, err := transitioncost.Derive(frozen, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			assertOutcome(t, result, transitioncost.VerdictInsufficient, transitioncost.ReasonNativeStateUnverified)
			assertNoAcceptedObservation(t, result)
		})
	}
}
