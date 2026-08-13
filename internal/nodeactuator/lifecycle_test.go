package nodeactuator

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDecideHappyPathKeepsApplyAndRestoreReadBackSeparate(t *testing.T) {
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
		{name: "acquire Node Lease", mutate: func(s *Snapshot) { s.Lease = LeaseObservation{State: LeaseNotHeld} }, want: ActionAcquireLease},
		{name: "check safety after Lease", mutate: func(s *Snapshot) { s.Phase = PhasePending }, want: ActionCheckSafety},
		{name: "repeat unknown safety", mutate: func(s *Snapshot) {
			s.Phase = PhaseCheckingSafety
			s.Safety = SafetyUnknown
			s.SafetyObservedAt = nil
		}, want: ActionCheckSafety},
		{name: "observe previous after safety", mutate: func(s *Snapshot) { s.Phase = PhaseCheckingSafety; s.Safety = SafetyAllowed }, want: ActionObservePrevious},
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
		{name: "hold ready state", mutate: func(s *Snapshot) {
			s.Phase = PhaseReady
			s.PreviousObservation = &previous
			s.TargetObservation = &target
		}, want: ActionHold},
		{name: "begin requested restore", mutate: func(s *Snapshot) {
			s.Phase = PhaseReady
			s.PreviousObservation = &previous
			s.TargetObservation = &target
			s.Request.DesiredStage = DesiredStageRestore
		}, want: ActionBeginRestore},
		{name: "apply retained previous state", mutate: func(s *Snapshot) {
			s.Phase = PhaseRestoring
			s.PreviousObservation = &previous
			s.Request.DesiredStage = DesiredStageRestore
		}, want: ActionRestorePrevious},
		{name: "observe restoration without apply when CHILL did not change state", mutate: func(s *Snapshot) {
			s.Phase = PhaseRestoring
			s.PreviousObservation = &target
			s.TargetObservation = &target
			s.Request.DesiredStage = DesiredStageRestore
		}, want: ActionObserveRestored},
		{name: "read back restored state", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifyingRestore
			s.PreviousObservation = &previous
			s.Request.DesiredStage = DesiredStageRestore
		}, want: ActionObserveRestored},
		{name: "mark exact restoration", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifyingRestore
			s.PreviousObservation = &previous
			s.RestoredObservation = &previous
			s.Request.DesiredStage = DesiredStageRestore
		}, want: ActionMarkRestored},
		{name: "release only after restored", mutate: func(s *Snapshot) {
			s.Phase = PhaseRestored
			s.PreviousObservation = &previous
			s.RestoredObservation = &previous
			s.Request.DesiredStage = DesiredStageRestore
		}, want: ActionReleaseLease},
		{name: "complete after Lease release", mutate: func(s *Snapshot) {
			s.Phase = PhaseRestored
			s.PreviousObservation = &previous
			s.RestoredObservation = &previous
			s.Request.DesiredStage = DesiredStageRestore
			s.Lease = LeaseObservation{State: LeaseNotHeld}
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

func TestDecideFailsClosedOnSafetyReadBackDeadlineAndRestoreMismatch(t *testing.T) {
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
		{name: "unsafe workload", mutate: func(s *Snapshot) {
			s.Phase = PhaseCheckingSafety
			s.Safety = SafetyBlocked
		}, want: ActionMarkFailed, wantStage: FailureStageSafety, wantReason: "WorkloadSafetyBlocked"},
		{name: "target mismatch restores", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifying
			s.PreviousObservation = &previous
			s.TargetObservation = &wrong
		}, want: ActionBeginRestore, wantStage: FailureStageVerify, wantReason: "TargetReadBackMismatch"},
		{name: "target name mismatch restores", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifying
			s.PreviousObservation = &previous
			s.TargetObservation = &wrongName
		}, want: ActionBeginRestore, wantStage: FailureStageVerify, wantReason: "TargetReadBackMismatch"},
		{name: "deadline restores", mutate: func(s *Snapshot) {
			s.Phase = PhaseReady
			s.PreviousObservation = &previous
			s.Now = s.Request.Deadline
		}, want: ActionBeginRestore, wantStage: FailureStageHold, wantReason: "DeadlineExceeded"},
		{name: "restore mismatch terminal", mutate: func(s *Snapshot) {
			s.Phase = PhaseVerifyingRestore
			s.PreviousObservation = &previous
			s.RestoredObservation = &wrong
			s.Request.DesiredStage = DesiredStageRestore
		}, want: ActionMarkFailed, wantStage: FailureStageRestore, wantReason: "RestoreReadBackMismatch"},
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

func TestDecideRenewsHeldLeaseAndRetainsItAfterRestoreFailure(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	snapshot.Phase = PhaseReady
	snapshot.Lease.RetryAfter = 0
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionRenewLease {
		t.Fatalf("renew decision = %#v, error=%v", decision, err)
	}

	snapshot.Phase = PhaseFailed
	snapshot.Failure = &Failure{
		Stage: FailureStageRestore, Reason: "RestoreReadBackMismatch", Message: "restore did not converge",
	}
	snapshot.Lease.RetryAfter = time.Second
	decision, err = Decide(snapshot)
	if err != nil || decision.Action != ActionHold || decision.Failure != snapshot.Failure {
		t.Fatalf("restore failure decision = %#v, error=%v", decision, err)
	}
}

func TestDecideReacquiresLeaseBeforeRecoveringFromPrimaryFailure(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	previous := observation(1, snapshot.Now.Add(-time.Second))
	snapshot.Phase = PhaseFailed
	snapshot.PreviousObservation = &previous
	snapshot.Failure = &Failure{
		Stage: FailureStageApply, Reason: "ApplyFailed", Message: "target command failed",
	}
	snapshot.Lease = LeaseObservation{State: LeaseLost}
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionAcquireLease || decision.Failure != snapshot.Failure {
		t.Fatalf("recovery decision = %#v, error=%v", decision, err)
	}
}

func TestDecideWaitsOnConflictingLeaseWithoutCrossingHostBoundary(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	snapshot.Lease = LeaseObservation{State: LeaseConflict, RetryAfter: 7 * time.Second}
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
	snapshot.Lease.RetryAfter = 10 * time.Second
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionWaitForSettling || decision.RequeueAfter != 10*time.Second {
		t.Fatalf("settling decision = %#v, error=%v", decision, err)
	}
}

func TestReadyStateRechecksWorkloadSafetyAndRestoresWhenBlocked(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	previous := observation(1, snapshot.Now.Add(-time.Second))
	snapshot.Phase = PhaseReady
	snapshot.PreviousObservation = &previous
	stale := snapshot.Now.Add(-snapshot.SafetyValidFor)
	snapshot.SafetyObservedAt = &stale
	decision, err := Decide(snapshot)
	if err != nil || decision.Action != ActionCheckSafety {
		t.Fatalf("stale safety decision = %#v, error=%v", decision, err)
	}

	snapshot.Safety = SafetyBlocked
	snapshot.SafetyObservedAt = &snapshot.Now
	decision, err = Decide(snapshot)
	if err != nil || decision.Action != ActionBeginRestore || decision.Failure == nil ||
		decision.Failure.Stage != FailureStageSafety {
		t.Fatalf("blocked safety decision = %#v, error=%v", decision, err)
	}
}

func TestSnapshotRejectsRestoreVerificationWithoutPreviousState(t *testing.T) {
	t.Parallel()
	snapshot := validSnapshot()
	snapshot.Phase = PhaseVerifyingRestore
	snapshot.Request.DesiredStage = DesiredStageRestore
	if _, err := Decide(snapshot); err == nil || !strings.Contains(err.Error(), "previous observation") {
		t.Fatalf("invalid restore snapshot error = %v", err)
	}
}

func validSnapshot() Snapshot {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	return Snapshot{
		Request: Request{
			Handoff: ObjectIdentity{Name: "handoff", UID: "handoff-uid"}, Generation: 1,
			Run:         ObjectIdentity{Name: "run", UID: "run-uid"},
			Node:        ObjectIdentity{Name: "jetsonx", UID: "node-uid"},
			DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
			Target:                  Target{Kind: ActuatorKindJetsonNVPModel, JetsonNVPModel: &JetsonNVPModelTarget{ID: 2}},
			PolicyName:              "actuation-policy-v1",
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
			Deadline: now.Add(15 * time.Minute), DesiredStage: DesiredStageApply, StageRequestedAt: now,
		},
		Now: now, Phase: PhasePending,
		Lease:  LeaseObservation{State: LeaseHeld, RetryAfter: 10 * time.Second},
		Safety: SafetyAllowed, SafetyObservedAt: &now, SafetyValidFor: 30 * time.Second,
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
