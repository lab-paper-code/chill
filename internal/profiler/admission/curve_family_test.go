package admission

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestProjectVerifiedCurveFamilyStateRequiresLoaderEvidence(t *testing.T) {
	if _, err := ProjectVerifiedCurveFamilyState(
		profilederivation.CurveFamilyContract{},
		profilerevidence.VerifiedEvidence{}, profilerevidence.VerifiedIdleEvidence{},
	); err == nil {
		t.Fatal("empty Loader evidence was accepted")
	}
}

func TestCurveFamilyIntentProjectionBindsObservableContractFacts(t *testing.T) {
	active := curveFamilyIntent()
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
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: active.Candidate.ExecutionPath, Name: active.State.Artifact,
			Path: active.State.ArtifactPath, Image: active.Images.Artifact,
		},
		PlannedBatchSizes: []int{1},
	}
	if err := validateCurveFamilyIntents(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}
	drifted := active
	drifted.Images.Artifact = "different"
	if err := validateCurveFamilyIntents(drifted, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("artifact-supply drift was accepted")
	}
	drifted = idle
	drifted.TargetNode.UID = "other-node"
	if err := validateCurveFamilyIntents(active, drifted, "node-uid", "other-node", shared); err == nil {
		t.Fatal("Node drift was accepted")
	}
	drifted = idle
	drifted.Candidate.ExecutionPath = "different-path"
	if err := validateCurveFamilyIntents(active, drifted, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("active/idle execution-path disagreement was accepted")
	}
}

func TestCurveFamilyUsesFirstPlannedBatchSizeIdleWitness(t *testing.T) {
	active := curveFamilyIntent()
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
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: active.Candidate.ExecutionPath, Name: active.State.Artifact,
			Path: active.State.ArtifactPath, Image: active.Images.Artifact,
		},
		PlannedBatchSizes: []int{1, 2},
		Tensor: profilederivation.CurveTensorContract{
			Layout: "NCHW", DataType: "float32", InputSampleShape: []int{3, 224, 224},
		},
	}
	if _, err := profilederivation.FreezeCurveFamilyContract(shared); err != nil {
		t.Fatalf("freeze converter-free ORT family: %v", err)
	}
	if err := validateCurveFamilyIntents(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}

	idle.Measurement.Batch = 2
	if err := validateCurveFamilyIntents(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve family accepted a non-canonical idle batch")
	}
	idle.Measurement.Batch = 1
	active.Measurement.Batch = 3
	if err := validateCurveFamilyIntents(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve family accepted an ineligible active batch")
	}

	shared.PlannedBatchSizes = []int{2, 4}
	active.Measurement.Batch = 4
	idle.Measurement.Batch = 2
	if err := validateCurveFamilyIntents(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatalf("first planned batch size was not accepted as the idle witness: %v", err)
	}
	idle.Measurement.Batch = 1
	if err := validateCurveFamilyIntents(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve family accepted an idle batch before the first planned batch size")
	}
}

func curveFamilyIntent() profilingrun.Intent {
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
