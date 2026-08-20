package controller

import (
	"fmt"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func testBaselineParent() *edgev1alpha1.ModelLoadedIdleBaseline {
	return &edgev1alpha1.ModelLoadedIdleBaseline{
		ObjectMeta: metav1.ObjectMeta{
			Name: "idle-baseline", Namespace: "default", UID: types.UID("idle-baseline-uid"), Generation: 1,
		},
		Spec: edgev1alpha1.ModelLoadedIdleBaselineSpec{
			State: edgev1alpha1.MeasurementStateSpec{
				Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
					ModelSpec: "model-a", DeviceClass: "class-a", ExecutionPath: "runtime-a", PowerMode: "mode-a",
				},
				TargetNode: "node-a", CPURequest: resource.MustParse("100m"),
			},
			Batch: 1,
			Protocol: edgev1alpha1.IdleProtocolSpec{
				AdmissionPolicy: "idle-admission-v1", SettlingDurationSeconds: 60, DurationSeconds: 30,
				MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{
					IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
				},
			},
			PointPolicy: edgev1alpha1.ModelLoadedIdleBaselinePointPolicySpec{
				Version: "idle-point-v1", MinimumAcceptedRuns: 3, ConfidenceLevel: "0.95",
			},
			AttemptLimit: 5,
		},
	}
}

func testDerivedBaseline(t *testing.T, count int) profilederivation.IdleBaseline {
	t.Helper()
	trials := make([]profilederivation.IdleTrial, count)
	for index := range trials {
		trials[index] = profilederivation.IdleTrial{
			StateIdentity: "state", EnvironmentIdentity: "environment", MeasurementProtocolIdentity: "protocol",
			TrialID: fmt.Sprintf("trial-%d", index),
			RawEvidence: profilederivation.RawEvidenceIdentity{
				Store: "evidence", Key: fmt.Sprintf("runs/%d/raw.json", index), Size: 10,
			},
			AdmissionPolicyVersion: "idle-admission-v1", ModelLoadedIdleMeanWallWatts: float64(10 + index),
		}
	}
	baseline, err := profilederivation.DeriveIdleBaseline(trials, profilederivation.PointPolicy{
		Version: "idle-point-v1", MinimumTrials: count, ConfidenceLevel: .95,
	})
	if err != nil {
		t.Fatal(err)
	}
	return baseline
}

func testFinalReferences(count int) idlebaseline.AcceptedFinalEvidence {
	references := make(idlebaseline.AcceptedFinalEvidence, count)
	for index := 0; index < count; index++ {
		references[fmt.Sprintf("trial-%d", index)] = idlebaseline.StoredObjectReference{
			Store: "evidence", Key: fmt.Sprintf("runs/%d/final.json", index),
			SchemaVersion: "final.v1", Size: 20,
		}
	}
	return references
}

func acceptedRun(t *testing.T, parent *edgev1alpha1.ModelLoadedIdleBaseline, attempt int32) edgev1alpha1.ProfilingRun {
	t.Helper()
	run, err := buildRun(parent, attempt)
	if err != nil {
		t.Fatal(err)
	}
	run.UID = types.UID(fmt.Sprintf("run-%d", attempt))
	run.Status.Admission = &edgev1alpha1.ProfilingRunAdmissionStatus{Verdict: "Accepted", PolicyVersion: "idle-admission-v1"}
	run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{
		Store: "evidence", Key: fmt.Sprintf("runs/%d/final.json", attempt-1),
		SchemaVersion: "final.v1", Size: 20,
	}
	return *run
}
