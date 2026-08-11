package admission

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestProjectVerifiedCurveStateRequiresLoaderEvidence(t *testing.T) {
	if _, err := ProjectVerifiedCurveState(
		profilederivation.CurvePlan{},
		profilerevidence.VerifiedEvidence{}, profilerevidence.VerifiedIdleEvidence{},
	); err == nil {
		t.Fatal("empty Loader evidence was accepted")
	}
}

func TestProjectCurvePlanSpecRoundTripsThroughIntentValidation(t *testing.T) {
	active := curvePlanIntent()
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	spec, err := ProjectCurvePlanSpec(
		"test-curve", active, idle,
		profilingrun.CandidateArtifactCapability{
			TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{1, 2},
			InputSampleShape: []int32{224, 224, 3}, InputLayout: "NHWC", InputDataType: "uint8",
		},
		profilederivation.CurveConversionContract{
			TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
		},
		[]int{1, 2},
	)
	if err != nil {
		t.Fatalf("project curve plan: %v", err)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatalf("construct curve plan: %v", err)
	}
	recorded := plan.Shared()
	if err := validateCurvePlanIntent(active, active.TargetNode.UID, recorded, recorded.ActiveProtocol); err != nil {
		t.Fatalf("validate active intent against projected plan: %v", err)
	}
	if err := validateCurvePlanIntent(idle, idle.TargetNode.UID, recorded, recorded.IdleProtocol); err != nil {
		t.Fatalf("validate idle intent against projected plan: %v", err)
	}
}

func TestVerifiedCurvePlanIntentProjectionBindsObservableFacts(t *testing.T) {
	active := curvePlanIntent()
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
	shared := profilederivation.CurvePlanSpec{
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
	if err := validateCurvePlanIntents(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}
	drifted := active
	drifted.Images.Artifact = "different"
	if err := validateCurvePlanIntents(drifted, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("artifact-supply drift was accepted")
	}
	drifted = idle
	drifted.TargetNode.UID = "other-node"
	if err := validateCurvePlanIntents(active, drifted, "node-uid", "other-node", shared); err == nil {
		t.Fatal("Node drift was accepted")
	}
	drifted = idle
	drifted.Candidate.ExecutionPath = "different-path"
	if err := validateCurvePlanIntents(active, drifted, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("active/idle execution-path disagreement was accepted")
	}
}

func TestVerifiedCurvePlanUsesFirstPlannedBatchSizeIdleWitness(t *testing.T) {
	active := curvePlanIntent()
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
	shared := profilederivation.CurvePlanSpec{
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
	if _, err := profilederivation.NewCurvePlan(shared); err != nil {
		t.Fatalf("construct converter-free ORT plan: %v", err)
	}
	if err := validateCurvePlanIntents(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}

	idle.Measurement.Batch = 2
	if err := validateCurvePlanIntents(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted a non-canonical idle batch")
	}
	idle.Measurement.Batch = 1
	active.Measurement.Batch = 3
	if err := validateCurvePlanIntents(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted an unplanned active batch")
	}

	shared.PlannedBatchSizes = []int{2, 4}
	active.Measurement.Batch = 4
	idle.Measurement.Batch = 2
	if err := validateCurvePlanIntents(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatalf("first planned batch size was not accepted as the idle witness: %v", err)
	}
	idle.Measurement.Batch = 1
	if err := validateCurvePlanIntents(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted an idle batch before the first planned batch size")
	}
}

func curvePlanIntent() profilingrun.Intent {
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
