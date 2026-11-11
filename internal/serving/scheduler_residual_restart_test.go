package serving

import (
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestSchedulerRunningResidualBeyondSupportRestartsSameGap(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	settings.DefaultDispatchOccupancyNanos = 0
	gap, elapsed := int64(0), int64(50*time.Millisecond)
	batch := &snapshot.Instances[0].Profile.Batches[0]
	*batch = schedulerTestPairedBatch(*batch, []firstfloor.ServiceSample{
		{TrialID: "physical", ServiceDurationNanos: int64(30 * time.Millisecond)},
		{TrialID: "physical", PrecedingIdleNanos: &gap, ServiceDurationNanos: int64(10 * time.Millisecond)},
	})
	d := snapshot.Workloads[0]
	snapshot.Outstanding = []SchedulerRequest{{
		ID: "running", Definition: d, Instance: "i1", Phase: phaseRunning, BatchID: "batch-1",
		Elapsed: time.Duration(elapsed), ReceivedAt: snapshot.At.Add(-time.Duration(elapsed)),
		PrecedingIdleNanos: &gap, ExchangeElapsedNanos: &elapsed,
	}}
	candidate := SchedulerCandidate{Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances}
	result := schedulerReplayForTest(settings, snapshot, candidate)
	if len(result.reasons) != 0 {
		t.Fatalf("exhausted residual support invalidated replay: %v", result.reasons)
	}
	if got := result.samples[d.Identity()]; len(got) != 1 || got[0] != float64(60*time.Millisecond) {
		t.Fatalf("running batch must complete after fresh 10ms, without subtracting elapsed: %v", got)
	}
	if !slices.ContainsFunc(result.assumptions, func(s string) bool {
		return strings.Contains(s, "RunningResidualBeyondSupport") && strings.Contains(s, "bidirectional, not a bound")
	}) {
		t.Fatalf("restart bias assumption missing: %v", result.assumptions)
	}
	records := result.conditional.RunningResidualBeyondSupport
	if len(records) != 1 || records[0] != (RunningResidualBeyondSupport{
		InstanceID: "i1", BatchSize: 1, ElapsedNanos: elapsed,
		ProfileMaxNanos: int64(30 * time.Millisecond), Model: "same-gap-service-restart",
	}) {
		t.Fatalf("restart evidence missing or wrong: %+v", records)
	}
	var merged ConditionalServiceEvidence
	merged.merge(result.conditional)
	if len(merged.RunningResidualBeyondSupport) != 1 || merged.MinimumTrialCount != 1 {
		t.Fatalf("merged evidence lost restart or original support: %+v", merged)
	}

	// Isolate running-batch energy from idle baseline and reply accounting.
	request := &schedulerReplayRequest{source: snapshot.Outstanding[0], identity: d.Identity()}
	instance := &schedulerReplayInstance{config: snapshot.Instances[0], batch: []*schedulerReplayRequest{request}}
	state := schedulerReplayState{
		instances: []*schedulerReplayInstance{instance}, horizon: float64(5 * time.Millisecond),
		rng: rand.New(rand.NewPCG(schedulerTestSeed, 0)),
	}
	if !state.initializeRunning() || instance.nativeFinish != float64(10*time.Millisecond) {
		t.Fatalf("restart finish must use whole fresh service: %+v", state.out)
	}
	wantJoules := batch.EnergyPerInputJoules * 5 / 60
	if math.Abs(state.out.requestJoules-wantJoules) > 1e-12 || state.out.energy != state.out.requestJoules {
		t.Fatalf("restart energy must use remaining/(elapsed+remaining): got %g want %g", state.out.requestJoules, wantJoules)
	}
}

func TestSchedulerRunningResidualMissingGapSupportStaysInvalid(t *testing.T) {
	settings, snapshot := schedulerFixture()
	settings.AutonomousWorkloads = nil
	gap, elapsed := int64(0), int64(50*time.Millisecond)
	batch := &snapshot.Instances[0].Profile.Batches[0]
	*batch = schedulerTestPairedBatch(*batch, []firstfloor.ServiceSample{
		{TrialID: "startup-only", ServiceDurationNanos: int64(10 * time.Millisecond)},
	})
	snapshot.Outstanding = []SchedulerRequest{{
		ID: "running", Definition: snapshot.Workloads[0], Instance: "i1", Phase: phaseRunning, BatchID: "batch-1",
		PrecedingIdleNanos: &gap, ExchangeElapsedNanos: &elapsed,
	}}
	result := schedulerReplayForTest(settings, snapshot, SchedulerCandidate{
		Keep: true, Nodes: snapshot.Nodes, Instances: snapshot.Instances,
	})
	schedulerRequireReason(t, result.reasons, "RunningElapsedOutsideProfileRange:i1")
	if len(result.conditional.RunningResidualBeyondSupport) != 0 {
		t.Fatalf("missing gap support fabricated a restart: %+v", result.conditional)
	}
}
