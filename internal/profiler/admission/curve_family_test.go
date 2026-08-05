package admission

import (
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestProjectVerifiedCurveFamilyMemberRequiresLoaderEvidence(t *testing.T) {
	if _, err := ProjectVerifiedCurveFamilyMember(
		profilederivation.CurveFamilyContract{}, 1,
		profilerevidence.VerifiedEvidence{}, profilerevidence.VerifiedIdleEvidence{},
	); err == nil {
		t.Fatal("empty Loader evidence was accepted")
	}
}

func TestCurveMemberIntentProjectionBindsObservableContractFacts(t *testing.T) {
	active := curveMemberIntent()
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, Repetitions: 1,
		WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	activeProtocol, err := projectCurveProtocol(active)
	if err != nil {
		t.Fatal(err)
	}
	idleProtocol, err := projectCurveProtocol(idle)
	if err != nil {
		t.Fatal(err)
	}
	shared := profilederivation.CurveFamilySharedContract{
		LogicalModel: active.State.Model, ModelSpecContentDigest: active.Candidate.ModelSpecContentDigest,
		DeviceClassContentDigest: active.Candidate.DeviceClassContentDigest,
		RuntimeDeclarationDigest: active.Candidate.RuntimeDeclarationDigest,
		DeviceClass:              active.State.DeviceClass, NodeUID: active.TargetNode.UID,
		RuntimeFamily: active.State.RuntimeFamily, Backend: active.State.Backend,
		RuntimeImage: active.Images.Runtime, PowerMode: active.State.PowerMode,
		HostContractDigest: active.State.HostContractDigest, CPU: projectCurveCPU(active.CPU),
		MetricSource:   projectCurveMetricSource(active.Metrics),
		ActiveProtocol: activeProtocol, IdleProtocol: idleProtocol,
	}
	member := profilederivation.CurveFamilyMemberContract{
		Batch: 1, ExecutionPath: active.Candidate.ExecutionPath,
		ArtifactName: active.State.Artifact, ArtifactDigest: active.State.ArtifactDigest,
		ArtifactPath: active.State.ArtifactPath, ArtifactImage: active.Images.Artifact,
	}
	if err := validateCurveMemberIntents(active, idle, "node-uid", "node-uid", shared, member); err != nil {
		t.Fatal(err)
	}
	drifted := active
	drifted.Images.Artifact = "different"
	if err := validateCurveMemberIntents(drifted, idle, "node-uid", "node-uid", shared, member); err == nil {
		t.Fatal("artifact-supply drift was accepted")
	}
	drifted = idle
	drifted.TargetNode.UID = "other-node"
	if err := validateCurveMemberIntents(active, drifted, "node-uid", "other-node", shared, member); err == nil {
		t.Fatal("Node drift was accepted")
	}
	drifted = idle
	drifted.Candidate.ReportDigest = strings.Repeat("x", 64)
	if err := validateCurveMemberIntents(active, drifted, "node-uid", "node-uid", shared, member); err == nil {
		t.Fatal("active/idle candidate disagreement was accepted")
	}
}

func curveMemberIntent() profilingrun.Intent {
	intent := admissionIntentWithWorkloadContract()
	intent.Candidate = profilingrun.CandidateIdentity{
		ReportDigest:             "sha256:" + strings.Repeat("1", 64),
		ModelSpecContentDigest:   "sha256:" + strings.Repeat("2", 64),
		DeviceClassContentDigest: "sha256:" + strings.Repeat("3", 64),
		RuntimeDeclarationDigest: "sha256:" + strings.Repeat("4", 64),
		ExecutionPath:            "rknnlite", Verdict: "Compatible",
	}
	intent.State.Artifact = "model-bs1"
	intent.State.ArtifactPath = "/artifact/model.rknn"
	intent.State.HostContractDigest = "sha256:" + strings.Repeat("5", 64)
	intent.Images = profilingrun.Images{
		Runtime: "runtime", Artifact: "artifact", MetricObserver: "metric", WorkloadObserver: "workload",
	}
	intent.CPU.Request = "3"
	intent.Metrics.ConfigurationContentDigest = "sha256:" + strings.Repeat("6", 64)
	intent.Measurement.Batch = 1
	return intent
}
