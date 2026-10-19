package nodeactuator

import (
	"fmt"
	"testing"
	"time"
)

func TestDecideHappyPathKeepsApplyAndTargetReadBackSeparate(t *testing.T) {
	t.Parallel()
	base := validSnapshot()
	previous := observation(1, base.Now.Add(-time.Second))
	target := observation(2, base.Now)
	verifiedAt := base.Now
	settlingAt := base.Now

	tests := []struct {
		name   string
		mutate func(*Snapshot)
		want   Action
	}{
		{name: "acquire Node Lease", mutate: func(s *Snapshot) { s.Lock = LockObservation{State: LockNotHeld} }, want: ActionAcquireLease},
		{name: "observe previous after Lease", mutate: func(s *Snapshot) { s.Phase = PhasePending }, want: ActionObservePrevious},
		{name: "repeat previous observation", mutate: func(s *Snapshot) { s.Phase = PhaseObservingPrevious }, want: ActionObservePrevious},
		{name: "apply retained target", mutate: func(s *Snapshot) { s.Phase = PhaseObservingPrevious; s.PreviousObservation = &previous }, want: ActionApplyTarget},
		{name: "settle without apply when target is already active", mutate: func(s *Snapshot) {
			s.Phase = PhaseObservingPrevious
			s.PreviousObservation = &target
		}, want: ActionStartSettling},
		{name: "idempotent apply", mutate: func(s *Snapshot) { s.Phase = PhaseApplying; s.PreviousObservation = &previous }, want: ActionApplyTarget},
		{name: "independent target readback", mutate: func(s *Snapshot) { s.Phase = PhaseVerifying; s.PreviousObservation = &previous }, want: ActionObserveTarget},
		{name: "start settling after exact readback", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifying
			s.PreviousObservation = &previous
			s.TargetObservation = &target
		}, want: ActionStartSettling},
		{name: "wait full settling interval", mutate: func(s *Snapshot) {
			s.Phase = PhaseSettling
			s.PreviousObservation = &previous
			s.TargetObservation = &target
			s.VerifiedAt = &verifiedAt
			s.SettlingStartedAt = &settlingAt
		}, want: ActionWaitForSettling},
		{name: "mark ready after settling", mutate: func(s *Snapshot) {
			s.Phase = PhaseSettling
			s.PreviousObservation = &previous
			s.TargetObservation = &target
			past := s.Now.Add(-time.Duration(s.Request.SettlingDurationSeconds) * time.Second)
			s.VerifiedAt = &past
			s.SettlingStartedAt = &past
		}, want: ActionMarkReady},
		{name: "release Lease at ready", mutate: func(s *Snapshot) {
			s.Phase = PhaseReady
			s.PreviousObservation = &previous
			s.TargetObservation = &target
		}, want: ActionReleaseLease},
		{name: "complete after Lease release", mutate: func(s *Snapshot) {
			s.Phase = PhaseReady
			s.PreviousObservation = &previous
			s.TargetObservation = &target
			s.Lock = LockObservation{State: LockNotHeld}
		}, want: ActionComplete},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			test.mutate(&snapshot)
			decision, err := Decide(snapshot)
			if err != nil || decision.Action != test.want {
				t.Fatalf("decision = %#v, error=%v, want %s", decision, err, test.want)
			}
		})
	}
}

func TestDecideCountsSettlingFromTheRecordedStart(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	target := observation(2, snapshot.Now.Add(-time.Hour))
	settlingStartedAt := snapshot.Now
	snapshot.Phase = PhaseSettling
	snapshot.PreviousObservation = &target
	snapshot.TargetObservation = &target
	snapshot.VerifiedAt = &target.ObservedAt
	snapshot.SettlingStartedAt = &settlingStartedAt

	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionWaitForSettling {
		t.Fatalf("decision = %#v, error=%v, want %s", decision, err, ActionWaitForSettling)
	}
}

func TestDecideFailsClosedOnReadBackAndDeadline(t *testing.T) {
	t.Parallel()
	base := validSnapshot()
	previous := observation(1, base.Now.Add(-time.Second))
	wrong := observation(3, base.Now)
	wrongName := observation(2, base.Now)
	wrongName.PowerMode = "MODE_15W_6CORE_DRIFTED"

	tests := []struct {
		name       string
		mutate     func(*Snapshot)
		want       Action
		wantStage  FailureStage
		wantReason string
	}{
		{name: "target mismatch fails", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifying
			s.PreviousObservation = &previous
			s.TargetObservation = &wrong
		}, want: ActionMarkFailed, wantStage: FailureStageVerify, wantReason: "TargetReadBackMismatch"},
		{name: "target name mismatch fails", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifying
			s.PreviousObservation = &previous
			s.TargetObservation = &wrongName
		}, want: ActionMarkFailed, wantStage: FailureStageVerify, wantReason: "TargetReadBackMismatch"},
		{name: "deadline fails transition", mutate: func(s *Snapshot) {
			s.Phase = PhaseSettling
			s.PreviousObservation = &previous
			s.TargetObservation = &wrong
			s.VerifiedAt = &s.Now
			s.SettlingStartedAt = &s.Now
			s.Now = s.Request.Deadline
		}, want: ActionMarkFailed, wantStage: FailureStageSettle, wantReason: "DeadlineExceeded"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := base
			test.mutate(&snapshot)
			decision, err := Decide(snapshot)
			if err != nil || decision.Action != test.want || decision.Failure == nil ||
				decision.Failure.Stage != test.wantStage || decision.Failure.Reason != test.wantReason {
				t.Fatalf("decision = %#v, error=%v", decision, err)
			}
		})
	}
}

func TestDecideRenewsHeldLeaseAndReleasesItAfterFailure(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	snapshot.Phase = PhaseApplying
	snapshot.Lock.RetryAfter = 0
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionRenewLease {
		t.Fatalf("renew decision = %#v, error=%v", decision, err)
	}

	snapshot.Phase = PhaseFailed
	snapshot.Failure = &Failure{
		Stage: FailureStageApply, Reason: "TargetApplyFailed", Message: "apply did not complete",
	}
	snapshot.Lock.RetryAfter = time.Second
	decision, err = Decide(snapshot)
	if err != nil || decision.Action != ActionReleaseLease || decision.Failure != snapshot.Failure {
		t.Fatalf("failure decision = %#v, error=%v", decision, err)
	}
}

func TestDecideCompletesFailureWithoutReacquiringLease(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	previous := observation(1, snapshot.Now.Add(-time.Second))
	snapshot.Phase = PhaseFailed
	snapshot.PreviousObservation = &previous
	snapshot.Failure = &Failure{
		Stage: FailureStageApply, Reason: "ApplyFailed", Message: "target command failed",
	}
	snapshot.Lock = LockObservation{State: LockLost}
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionComplete || decision.Failure != snapshot.Failure {
		t.Fatalf("terminal failure decision = %#v, error=%v", decision, err)
	}
}

func TestDecideWaitsOnConflictingLeaseWithoutCrossingHostBoundary(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	snapshot.Lock = LockObservation{State: LockConflict, RetryAfter: 7 * time.Second}
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionWaitForLease || decision.RequeueAfter != 7*time.Second {
		t.Fatalf("conflict decision = %#v, error=%v", decision, err)
	}
}

func TestSettlingWaitNeverOutlivesLeaseRenewal(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	snapshot.Phase = PhaseSettling
	verified := snapshot.Now
	snapshot.VerifiedAt = &verified
	snapshot.SettlingStartedAt = &verified
	snapshot.Lock.RetryAfter = 10 * time.Second
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionWaitForSettling || decision.RequeueAfter != 10*time.Second {
		t.Fatalf("settling decision = %#v, error=%v", decision, err)
	}
}

func validSnapshot() Snapshot {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return Snapshot{
		Request: Request{
			Transition: ObjectIdentity{Name: "transition", UID: "transition-uid"}, Generation: 1,
			Node:        ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
			DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
			Target:                  Target{Kind: ActuatorKindJetsonNVPModel, JetsonNVPModel: &JetsonNVPModelTarget{ID: 2}},
			PolicyName:              "actuation-policy-v1",
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
			Deadline: now.Add(15 * time.Minute), StageRequestedAt: now,
		},
		Now: now, Phase: PhasePending,
		Lock: LockObservation{State: LockHeld, RetryAfter: 10 * time.Second},
	}
}

func observation(id int32, observedAt time.Time) Observation {
	powerMode := fmt.Sprintf("mode-%d", id)
	if id == 2 {
		powerMode = "MODE_15W_6CORE"
	}
	return Observation{
		PowerMode:  powerMode,
		Target:     Target{Kind: ActuatorKindJetsonNVPModel, JetsonNVPModel: &JetsonNVPModelTarget{ID: id}},
		ObservedAt: observedAt,
	}
}
