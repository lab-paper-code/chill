package worker_test

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	transitionworker "github.com/lab-paper-code/chill/internal/profiling/transitioncost/worker"
)

func TestObserveRetainsPowerAcrossSimulatedTargetLossAndDerivesCost(t *testing.T) {
	base := time.Date(2026, time.August, 20, 0, 0, 0, 0, time.UTC)
	target := &fakeTargetExecutor{}
	target.online.Store(true)
	attempts := make(chan powerAttempt)
	acknowledged := make(chan struct{})
	power := &streamingPowerObserver{
		startedAt: base,
		endedAt:   base.Add(8 * time.Second),
		attempts:  attempts,
		ack:       acknowledged,
		target:    target,
	}
	factPathReady := make(chan struct{})
	factResult := make(chan transitioncost.RawObservation)
	facts := factObserverFunc(func(ctx context.Context, ready func()) (transitioncost.RawObservation, error) {
		select {
		case <-factPathReady:
			ready()
		case <-ctx.Done():
			return transitioncost.RawObservation{}, ctx.Err()
		}
		select {
		case raw := <-factResult:
			return raw, nil
		case <-ctx.Done():
			return transitioncost.RawObservation{}, ctx.Err()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{}, 1)
	var readyCalls atomic.Int32
	result := make(chan observationResult, 1)
	go func() {
		raw, err := transitionworker.Observe(ctx, power, validPowerRequest(), facts, func() {
			readyCalls.Add(1)
			ready <- struct{}{}
		})
		result <- observationResult{raw: raw, err: err}
	}()

	latency := 0.123456789
	sendPowerAttempt(t, ctx, attempts, acknowledged, powerAttempt{sample: &metricobserver.Sample{
		ObservedAt: base.Add(time.Second), Watts: 20, RequestLatencySeconds: latency,
	}})
	select {
	case <-ready:
		t.Fatal("observation became ready before the transition-fact path")
	default:
	}
	close(factPathReady)
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("observation did not become ready after both paths")
	}

	// This is an offline executor-loss simulation, not a live restart proof.
	target.online.Store(false)
	for _, offset := range []time.Duration{2100 * time.Millisecond, 3 * time.Second, 4 * time.Second} {
		sendPowerAttempt(t, ctx, attempts, acknowledged, powerAttempt{sample: &metricobserver.Sample{
			ObservedAt: base.Add(offset), Watts: 20, RequestLatencySeconds: 0.01,
		}})
	}
	sendPowerAttempt(t, ctx, attempts, acknowledged, powerAttempt{failure: &metricobserver.Failure{
		ObservedAt: base.Add(3500 * time.Millisecond), RequestLatencySeconds: 0.02,
		Reason: metricobserver.FailureReasonSourceRead, Message: "one failed read",
	}})
	target.online.Store(true)
	for _, offset := range []time.Duration{4900 * time.Millisecond, 5900 * time.Millisecond, 7 * time.Second} {
		sendPowerAttempt(t, ctx, attempts, acknowledged, powerAttempt{sample: &metricobserver.Sample{
			ObservedAt: base.Add(offset), Watts: 20, RequestLatencySeconds: 0.01,
		}})
	}

	source := transitioncost.StateIdentity{
		DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MODE_15W",
	}
	targetState := transitioncost.StateIdentity{
		DeviceType: "jetson-orin", Model: "mobilenet-v2", PowerMode: "MAXN",
	}
	factResult <- completedRestartFacts(base, source, targetState)

	var observed observationResult
	select {
	case observed = <-result:
	case <-ctx.Done():
		t.Fatal("worker did not return after transition facts completed")
	}
	if observed.err != nil {
		t.Fatal(observed.err)
	}
	if readyCalls.Load() != 1 {
		t.Fatalf("observation-ready callbacks = %d, want 1", readyCalls.Load())
	}
	if power.offlineSamples.Load() != 3 {
		t.Fatalf("samples retained while target was unavailable = %d, want 3", power.offlineSamples.Load())
	}
	if !containsPowerSampleAt(observed.raw.PowerSamples, base.Add(3*time.Second)) {
		t.Fatal("raw observation does not retain the sample captured while the target executor was unavailable")
	}
	if len(observed.raw.PowerSamples) != 7 || len(observed.raw.PowerFailures) != 1 {
		t.Fatalf("combined power samples/failures = %d/%d, want 7/1",
			len(observed.raw.PowerSamples), len(observed.raw.PowerFailures))
	}
	if observed.raw.PowerSamples[0].RequestLatencySeconds != latency ||
		observed.raw.PowerFailures[0].RequestLatencySeconds != 0.02 {
		t.Fatalf("request latency was not passed through exactly: %#v %#v",
			observed.raw.PowerSamples[0], observed.raw.PowerFailures[0])
	}
	if !observed.raw.ObservationStartedAt.Equal(base) ||
		!observed.raw.ObservationEndedAt.Equal(base.Add(8*time.Second)) {
		t.Fatalf("combined coverage = %v..%v, want %v..%v",
			observed.raw.ObservationStartedAt, observed.raw.ObservationEndedAt,
			base, base.Add(8*time.Second))
	}

	derived, err := transitioncost.Derive(transitioncost.FrozenContract{
		SourceState: source, TargetState: targetState,
		TargetIdleBaseline: transitioncost.IdleBaseline{
			EvidenceReference: "evidence://idle-maxn", State: targetState, MeanWallWatts: 12,
		},
	}, observed.raw, transitioncost.Policy{
		MaximumBoundaryDistance:      200 * time.Millisecond,
		MinimumCoverage:              0.9,
		MaximumSampleGap:             1100 * time.Millisecond,
		MaximumBoundaryUncertainty:   50 * time.Millisecond,
		MaximumReadyObservationDelay: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, ok := derived.AcceptedObservation()
	if derived.Verdict() != transitioncost.VerdictAccepted || !ok {
		t.Fatalf("derived outcome = %s/%s: %s", derived.Verdict(), derived.Reason(), derived.Detail())
	}
	if accepted.TauMilliseconds() != 4000 || math.Abs(accepted.TransitionWallJoules()-80) > 1e-9 ||
		math.Abs(accepted.EtaJoules()-32) > 1e-9 || accepted.ServingSurvived() {
		t.Fatalf("accepted transition = tau %v ms, wall %v J, eta %v J, survived %v",
			accepted.TauMilliseconds(), accepted.TransitionWallJoules(),
			accepted.EtaJoules(), accepted.ServingSurvived())
	}
}

func TestObserveRejectsPathEndingBeforeBothAreReadyAndJoinsPeer(t *testing.T) {
	peerStopped := make(chan struct{})
	power := powerObserverFunc(func(ctx context.Context, _ metricobserver.Request, ready func()) (metricobserver.Result, error) {
		defer close(peerStopped)
		ready()
		<-ctx.Done()
		return metricobserver.Result{}, nil
	})
	facts := factObserverFunc(func(context.Context, func()) (transitioncost.RawObservation, error) {
		return transitioncost.RawObservation{}, nil
	})
	readyCalls := 0

	_, err := transitionworker.Observe(context.Background(), power, validPowerRequest(), facts, func() {
		readyCalls++
	})
	if err == nil || !strings.Contains(err.Error(), "before observation became ready") {
		t.Fatalf("Observe() error = %v, want not-ready error", err)
	}
	if readyCalls != 0 {
		t.Fatalf("observation-ready callbacks = %d, want 0", readyCalls)
	}
	select {
	case <-peerStopped:
	default:
		t.Fatal("power observer was not joined after fact-path termination")
	}
}

func TestObserveJoinsBothObserversWhenReadyCallbackCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	powerStopped := make(chan struct{})
	factsStopped := make(chan struct{})
	power := powerObserverFunc(func(ctx context.Context, _ metricobserver.Request, ready func()) (metricobserver.Result, error) {
		defer close(powerStopped)
		ready()
		<-ctx.Done()
		return metricobserver.Result{}, nil
	})
	facts := factObserverFunc(func(ctx context.Context, ready func()) (transitioncost.RawObservation, error) {
		defer close(factsStopped)
		ready()
		<-ctx.Done()
		return transitioncost.RawObservation{}, nil
	})

	_, err := transitionworker.Observe(ctx, power, validPowerRequest(), facts, cancel)
	if err != context.Canceled {
		t.Fatalf("Observe() error = %v, want context.Canceled", err)
	}
	assertStopped(t, powerStopped, "power observer")
	assertStopped(t, factsStopped, "transition fact observer")
}

func TestObserveRejectsPowerEndingBeforeFactsAfterReadyAndJoinsPeer(t *testing.T) {
	releasePower := make(chan struct{})
	factsStopped := make(chan struct{})
	power := powerObserverFunc(func(_ context.Context, _ metricobserver.Request, ready func()) (metricobserver.Result, error) {
		ready()
		<-releasePower
		return metricobserver.Result{}, nil
	})
	facts := factObserverFunc(func(ctx context.Context, ready func()) (transitioncost.RawObservation, error) {
		defer close(factsStopped)
		ready()
		<-ctx.Done()
		return transitioncost.RawObservation{}, nil
	})

	_, err := transitionworker.Observe(context.Background(), power, validPowerRequest(), facts, func() {
		close(releasePower)
	})
	if err == nil || !strings.Contains(err.Error(), "power observation ended before transition facts") {
		t.Fatalf("Observe() error = %v, want premature power-end error", err)
	}
	assertStopped(t, factsStopped, "transition fact observer")
}

func completedRestartFacts(
	base time.Time,
	source, target transitioncost.StateIdentity,
) transitioncost.RawObservation {
	pre := transitioncost.ProcessIdentity{
		ContainerName: "server", ContainerID: "container-a",
		StartedAt: base.Add(-time.Minute), RestartCount: 0,
	}
	post := transitioncost.ProcessIdentity{
		ContainerName: "server", ContainerID: "container-b",
		StartedAt: base.Add(5 * time.Second), RestartCount: 1,
	}
	return transitioncost.RawObservation{
		ObservationStartedAt: base.Add(500 * time.Millisecond),
		ObservationEndedAt:   base.Add(7500 * time.Millisecond),
		SourceReadBack: transitioncost.StateReadBack{
			Fact: transitioncost.MappedFact{
				OccurredAt: base.Add(time.Second), MappedAt: base.Add(time.Second),
			},
			State: source,
		},
		TransitionStart: &transitioncost.OccurredFact{
			OccurredAt: base.Add(2 * time.Second),
		},
		TargetReadBack: &transitioncost.TargetStateReadBack{
			Fact: transitioncost.OccurredFact{
				OccurredAt: base.Add(4 * time.Second),
			},
			State: target,
		},
		MarkerBrackets: transitioncost.TransitionMarkerBrackets{
			ApplyStartedAt: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   base.Add(1990 * time.Millisecond),
				FirstPresentReceivedAt: base.Add(2 * time.Second),
			},
			TargetObservation: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   base.Add(3980 * time.Millisecond),
				FirstPresentReceivedAt: base.Add(4 * time.Second),
			},
			ReadyProcessWitness: transitioncost.ReceiptTimeBracket{
				LastAbsentReceivedAt:   base.Add(5970 * time.Millisecond),
				FirstPresentReceivedAt: base.Add(6 * time.Second),
			},
		},
		Readiness: []transitioncost.ReadyObservation{
			{ObservedAt: base.Add(5 * time.Second), Ready: false},
			{ObservedAt: base.Add(6 * time.Second), Ready: true},
		},
		PreTransitionProcess: &transitioncost.ProcessObservation{
			ObservedAt: base.Add(1500 * time.Millisecond), Process: pre,
		},
		PostTransitionProcess: &post,
	}
}

type observationResult struct {
	raw transitioncost.RawObservation
	err error
}

type powerObserverFunc func(
	context.Context,
	metricobserver.Request,
	func(),
) (metricobserver.Result, error)

func (f powerObserverFunc) Observe(
	ctx context.Context,
	request metricobserver.Request,
	ready func(),
) (metricobserver.Result, error) {
	return f(ctx, request, ready)
}

type factObserverFunc func(context.Context, func()) (transitioncost.RawObservation, error)

func (f factObserverFunc) Observe(
	ctx context.Context,
	ready func(),
) (transitioncost.RawObservation, error) {
	return f(ctx, ready)
}

type powerAttempt struct {
	sample  *metricobserver.Sample
	failure *metricobserver.Failure
}

type streamingPowerObserver struct {
	startedAt      time.Time
	endedAt        time.Time
	attempts       <-chan powerAttempt
	ack            chan<- struct{}
	target         *fakeTargetExecutor
	offlineSamples atomic.Int32
}

func (o *streamingPowerObserver) Observe(
	ctx context.Context,
	_ metricobserver.Request,
	ready func(),
) (metricobserver.Result, error) {
	result := metricobserver.Result{StartedAt: o.startedAt}
	firstSample := true
	for {
		select {
		case attempt := <-o.attempts:
			if attempt.sample != nil {
				result.Samples = append(result.Samples, *attempt.sample)
				if !o.target.online.Load() {
					o.offlineSamples.Add(1)
				}
				if firstSample {
					firstSample = false
					ready()
				}
			}
			if attempt.failure != nil {
				result.Failures = append(result.Failures, *attempt.failure)
			}
			o.ack <- struct{}{}
		case <-ctx.Done():
			result.EndedAt = o.endedAt
			return result, nil
		}
	}
}

type fakeTargetExecutor struct {
	online atomic.Bool
}

func sendPowerAttempt(
	t *testing.T,
	ctx context.Context,
	attempts chan<- powerAttempt,
	acknowledged <-chan struct{},
	attempt powerAttempt,
) {
	t.Helper()
	select {
	case attempts <- attempt:
	case <-ctx.Done():
		t.Fatal("power observer did not accept attempt")
	}
	select {
	case <-acknowledged:
	case <-ctx.Done():
		t.Fatal("power observer did not retain attempt")
	}
}

func validPowerRequest() metricobserver.Request {
	return metricobserver.Request{
		Interval: time.Second, Duration: time.Minute,
		RequestTimeout: 500 * time.Millisecond, MaxEvents: 62,
	}
}

func containsPowerSampleAt(samples []transitioncost.PowerSample, observedAt time.Time) bool {
	for _, sample := range samples {
		if sample.ObservedAt.Equal(observedAt) {
			return true
		}
	}
	return false
}

func assertStopped(t *testing.T, stopped <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-stopped:
	default:
		t.Fatalf("%s was not joined", name)
	}
}
