package projection

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestProjectVerifiedCurveStateRequiresLoaderEvidence(t *testing.T) {
	if _, err := ProjectVerifiedCurveState(
		profilederivation.CurvePlan{},
		profilerevidence.VerifiedEvidence{}, profilerevidence.VerifiedIdleEvidence{},
	); err == nil {
		t.Fatal("empty Loader evidence was accepted")
	}
}

func TestProjectCurvePlanSpecRoundTripsThroughRunConfigValidation(t *testing.T) {
	active := curvePlanRunConfig()
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
			Tensors: modelcompat.CloneInputTensors(active.Delivery.ArtifactCapability.Tensors),
		},
		profilederivation.CurveConversionContract{
			TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
		},
		[]int{1, 2},
	)
	if err != nil {
		t.Fatalf("project curve plan: %v", err)
	}
	if !reflect.DeepEqual(spec.Tensor.Tensors, active.Delivery.ArtifactCapability.Tensors) {
		t.Fatalf("projected tensor contract = %+v", spec.Tensor)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatalf("construct curve plan: %v", err)
	}
	recorded := plan.Shared()
	if err := validateCurvePlanRunConfig(active, active.TargetNode.UID, recorded, recorded.ActiveProtocol); err != nil {
		t.Fatalf("validate active RunConfig against projected plan: %v", err)
	}
	if err := validateCurvePlanRunConfig(idle, idle.TargetNode.UID, recorded, recorded.IdleProtocol); err != nil {
		t.Fatalf("validate idle RunConfig against projected plan: %v", err)
	}
}

func TestCurvePlanProjectionFreezesCompleteInputs(t *testing.T) {
	active := curvePlanRunConfig()
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
		InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
	}
	capability := *active.Delivery.ArtifactCapability
	spec, err := ProjectCurvePlanSpec(
		"test-curve", active, idle, capability, profilederivation.CurveConversionContract{}, []int{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := modelcompat.CloneInputTensors(capability.Tensors)
	capability.Tensors[0].Name = "mutated"
	capability.Tensors[0].Datatype = "INT32"
	capability.Tensors[0].SampleShape[0]++
	if !reflect.DeepEqual(spec.Tensor.Tensors, want) {
		t.Fatal("capability mutation changed the projected input contract")
	}
	for _, field := range []string{"name", "datatype", "shape", "missing"} {
		t.Run(field, func(t *testing.T) {
			drifted := curvePlanRunConfig()
			switch field {
			case "name":
				drifted.Delivery.ArtifactCapability.Tensors[0].Name = "other-input"
			case "datatype":
				drifted.Delivery.ArtifactCapability.Tensors[0].Datatype = "INT32"
			case "shape":
				drifted.Delivery.ArtifactCapability.Tensors[0].SampleShape[0]++
			case "missing":
				drifted.Delivery.ArtifactCapability = nil
			}
			if err := validateCurvePlanRunConfig(drifted, drifted.TargetNode.UID, spec, spec.ActiveProtocol); err == nil ||
				!strings.Contains(err.Error(), "curve-plan input tensor facts") {
				t.Fatalf("input contract drift error = %v", err)
			}
		})
	}
}

func TestProjectCurvePlanSpecRejectsActiveIdleDisagreement(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*profilingrun.RunConfig)
	}{
		{
			name: "artifact image",
			mutate: func(idle *profilingrun.RunConfig) {
				idle.Images.Artifact = "different-artifact-image"
			},
		},
		{
			name: "Node architecture",
			mutate: func(idle *profilingrun.RunConfig) {
				idle.TargetNode.Architecture = "different-architecture"
			},
		},
		{
			name: "Node allocatable CPU",
			mutate: func(idle *profilingrun.RunConfig) {
				idle.TargetNode.AllocatableCPU = "different-allocatable-cpu"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active := curvePlanRunConfig()
			idle := active
			idle.Measurement = profilingrun.MeasurementContract{
				Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
				WorkloadMode:   profilingrun.IdleModelLoadedWorkloadMode,
				InputGenerator: profilingrun.NoInputGenerator, OutputValidation: profilingrun.NoOutputValidation,
			}
			test.mutate(&idle)
			if _, err := ProjectCurvePlanSpec(
				"test-curve", active, idle,
				profilingrun.CandidateArtifactCapability{
					TargetPlatform: "rk3588", DeclaredBatchSizes: []int32{1, 2},
					Tensors: modelcompat.CloneInputTensors(active.Delivery.ArtifactCapability.Tensors),
				},
				profilederivation.CurveConversionContract{
					TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
				},
				[]int{1, 2},
			); err == nil {
				t.Fatal("active/idle disagreement was accepted")
			}
		})
	}
}

func TestVerifiedCurvePlanRunConfigProjectionBindsObservableFacts(t *testing.T) {
	active := curvePlanRunConfig()
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
		Tensor: profilederivation.CurveTensorContract{
			Tensors: modelcompat.CloneInputTensors(active.Delivery.ArtifactCapability.Tensors),
		},
	}
	if err := validateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}
	drifted := active
	drifted.Images.Artifact = "different"
	if err := validateCurvePlanRunConfigs(drifted, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("artifact-supply drift was accepted")
	}
	drifted = idle
	drifted.TargetNode.UID = "other-node"
	if err := validateCurvePlanRunConfigs(active, drifted, "node-uid", "other-node", shared); err == nil {
		t.Fatal("Node drift was accepted")
	}
	drifted = idle
	drifted.Candidate.ExecutionPath = "different-path"
	if err := validateCurvePlanRunConfigs(active, drifted, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("active/idle execution-path disagreement was accepted")
	}
}

func TestVerifiedCurvePlanUsesFirstPlannedBatchSizeIdleWitness(t *testing.T) {
	active := curvePlanRunConfig()
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
			Tensors: modelcompat.CloneInputTensors(active.Delivery.ArtifactCapability.Tensors),
		},
	}
	if _, err := profilederivation.NewCurvePlan(shared); err != nil {
		t.Fatalf("construct converter-free ORT plan: %v", err)
	}
	if err := validateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}

	idle.Measurement.Batch = 2
	if err := validateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted a non-canonical idle batch")
	}
	idle.Measurement.Batch = 1
	active.Measurement.Batch = 3
	if err := validateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted an unplanned active batch")
	}

	shared.PlannedBatchSizes = []int{2, 4}
	active.Measurement.Batch = 4
	idle.Measurement.Batch = 2
	if err := validateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatalf("first planned batch size was not accepted as the idle witness: %v", err)
	}
	idle.Measurement.Batch = 1
	if err := validateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted an idle batch before the first planned batch size")
	}
}

func curvePlanRunConfig() profilingrun.RunConfig {
	config := projectionRunConfigWithWorkloadContract()
	config.Candidate = profilingrun.CandidateIdentity{
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
	config.State.Artifact = "model-bs1"
	config.State.ArtifactPath = "/artifact/model.rknn"
	config.State.HostContract = &profilingrun.CandidateReportHostContract{}
	config.Images = profilingrun.Images{
		Runtime: "runtime", Artifact: "artifact", MetricObserver: "metric", WorkloadObserver: "workload",
	}
	config.CPU.Request = "3"
	config.Measurement.Batch = 1
	config.Delivery.ArtifactCapability = &profilingrun.CandidateArtifactCapability{
		Tensors: []modelcompat.InputTensor{
			{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}},
		},
	}
	return config
}
