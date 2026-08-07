package admission

import (
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
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
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
		Name: "test-curve", LogicalModel: active.State.Model,
		DeviceClass: active.State.DeviceClass, NodeUID: active.TargetNode.UID,
		RuntimeFamily: active.State.RuntimeFamily, Backend: active.State.Backend,
		RuntimeImage: active.Images.Runtime, PowerMode: active.State.PowerMode,
		CPU:            projectCurveCPU(active.CPU),
		MetricSource:   projectCurveMetricSource(active.Metrics),
		ActiveProtocol: activeProtocol, IdleProtocol: idleProtocol,
	}
	member := profilederivation.CurveFamilyMemberContract{
		Batch: 1, ExecutionPath: active.Candidate.ExecutionPath,
		ArtifactName: active.State.Artifact,
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
	drifted.Candidate.ExecutionPath = "different-path"
	if err := validateCurveMemberIntents(active, drifted, "node-uid", "node-uid", shared, member); err == nil {
		t.Fatal("active/idle execution-path disagreement was accepted")
	}
}

func TestDynamicCurveMemberUsesBatchOneIdleWitness(t *testing.T) {
	active := curveMemberIntent()
	active.Candidate.ExecutionPath = "ort-cpu"
	active.State.Artifact = "canonical-onnx"
	active.State.ArtifactPath = "/artifact/model.onnx"
	active.State.RuntimeFamily = "onnxruntime"
	active.State.Backend = "CPUExecutionProvider"
	active.State.HostContract = nil
	active.Measurement.Batch = 2
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
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
		Name: "test-curve", LogicalModel: active.State.Model,
		DeviceClass: active.State.DeviceClass, NodeUID: active.TargetNode.UID,
		RuntimeFamily: active.State.RuntimeFamily, Backend: active.State.Backend,
		RuntimeImage: active.Images.Runtime, PowerMode: active.State.PowerMode,
		CPU: projectCurveCPU(active.CPU), MetricSource: projectCurveMetricSource(active.Metrics),
		ActiveProtocol: activeProtocol, IdleProtocol: idleProtocol,
	}
	member := profilederivation.CurveFamilyMemberContract{
		Batch: 2, ExecutionPath: active.Candidate.ExecutionPath,
		ArtifactName: active.State.Artifact,
		ArtifactPath: active.State.ArtifactPath, ArtifactImage: active.Images.Artifact,
		FixedBatchCapability: 0,
	}
	if err := validateCurveMemberIntents(active, idle, "node-uid", "node-uid", shared, member); err != nil {
		t.Fatal(err)
	}

	idle.Measurement.Batch = 2
	if err := validateCurveMemberIntents(active, idle, "node-uid", "node-uid", shared, member); err == nil {
		t.Fatal("dynamic member accepted a non-canonical idle batch")
	}
	member.FixedBatchCapability = 2
	shared.Conversion.Derived = true
	idle.Measurement.Batch = 1
	if err := validateCurveMemberIntents(active, idle, "node-uid", "node-uid", shared, member); err == nil {
		t.Fatal("fixed-batch member accepted a different idle batch")
	}
}

func curveMemberIntent() profilingrun.Intent {
	intent := admissionIntentWithWorkloadContract()
	intent.Candidate = profilingrun.CandidateIdentity{
		Inputs: profilingrun.CandidateReportInputs{
			ModelSpec:   profilingrun.CandidateReportObjectInput{Name: "mobilenet", UID: "model-uid", ResourceVersion: "1"},
			DeviceClass: profilingrun.CandidateReportObjectInput{Name: "lattepanda", UID: "class-uid", ResourceVersion: "1"},
			RuntimeDeclaration: profilingrun.CandidateReportRuntimeInput{
				Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "1",
				Key: "execution-facts.yaml", Image: "runtime",
			},
		},
		ExecutionPath: "rknnlite", Verdict: "Compatible",
	}
	intent.State.Artifact = "model-bs1"
	intent.State.ArtifactPath = "/artifact/model.rknn"
	intent.State.HostContract = &profilingrun.CandidateReportHostContract{}
	intent.Images = profilingrun.Images{
		Runtime: "runtime", Artifact: "artifact", MetricObserver: "metric", WorkloadObserver: "workload",
	}
	intent.CPU.Request = "3"
	intent.Measurement.Batch = 1
	return intent
}
