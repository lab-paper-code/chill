package projection

import (
	"slices"
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
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
	}
	spec, err := ProjectCurvePlanSpec(
		"test-curve", active, idle,
		profilederivation.CurveConversionContract{
			TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
		},
		[]int{1, 2},
	)
	if err != nil {
		t.Fatalf("project curve plan: %v", err)
	}
	if !slices.Equal(spec.Artifact.Files, active.State.ArtifactFiles) ||
		spec.Preprocessor.Name != active.State.Preprocessor.Name ||
		!slices.Equal(spec.Preprocessor.Inputs, active.State.Preprocessor.Inputs) ||
		spec.ActiveProtocol.Dataset == nil || spec.ActiveProtocol.Dataset.UID != active.Measurement.Dataset.UID ||
		spec.IdleProtocol.Dataset != nil {
		t.Fatalf("projected preprocessing contract = %+v %+v", spec.Artifact, spec.Preprocessor)
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

func TestCurveCPUProjectionPreservesExactDetachedAffinityAndStateIdentity(t *testing.T) {
	active := curvePlanRunConfig()
	active.State.RuntimeFamily, active.State.Backend = profilingrun.FirstfloorRuntimeFamily, profilingrun.FirstfloorORTCPUBackend
	active.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	active.CPU.AffinityCPUs = []int{8, 10, 12, 31}
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
	}
	spec, err := ProjectCurvePlanSpec(
		"test-curve", active, idle,
		profilederivation.CurveConversionContract{}, []int{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(spec.CPU.AffinityCPUs, []int{8, 10, 12, 31}) {
		t.Fatal("curve CPU projection lost affinity")
	}
	before, err := ProjectRunIdentities(active, active.TargetNode.UID)
	if err != nil {
		t.Fatal(err)
	}
	active.CPU.AffinityCPUs[0] = 9
	if spec.CPU.AffinityCPUs[0] != 8 {
		t.Fatal("curve CPU projection aliases frozen RunConfig")
	}
	after, err := ProjectRunIdentities(active, active.TargetNode.UID)
	if err != nil {
		t.Fatal(err)
	}
	if before.StateIdentity == after.StateIdentity || before.MeasurementProtocolIdentity != after.MeasurementProtocolIdentity {
		t.Fatal("affinity did not change only the execution-state identity")
	}
	if err := validateCurvePlanRunConfig(active, active.TargetNode.UID, spec, spec.ActiveProtocol); err == nil {
		t.Fatal("curve accepted a different exact CPU mask")
	}
}

func TestCurvePlanProjectionFreezesPreprocessingInputs(t *testing.T) {
	active := curvePlanRunConfig()
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
	}
	spec, err := ProjectCurvePlanSpec(
		"test-curve", active, idle, profilederivation.CurveConversionContract{}, []int{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	active.State.Preprocessor.Inputs[0] = "mutated"
	active.State.ArtifactFiles[0] = "/mutated"
	if spec.Preprocessor.Inputs[0] != "image" || spec.Artifact.Files[0] != "/artifact/model.rknn" {
		t.Fatal("RunConfig mutation changed the projected preprocessing contract")
	}
	for name, test := range map[string]struct {
		mutate func(*profilingrun.RunConfig)
		match  string
	}{
		"preprocessor image": {func(c *profilingrun.RunConfig) { c.State.Preprocessor.Image = "other" }, "preprocessor facts"},
		"preprocessor outputs": {
			func(c *profilingrun.RunConfig) { c.State.Preprocessor.Outputs = []string{"answer"} }, "preprocessor facts",
		},
		"artifact files": {func(c *profilingrun.RunConfig) { c.State.ArtifactFiles = []string{"/artifact/a.rknn"} }, "artifact facts"},
		"dataset UID": {func(c *profilingrun.RunConfig) {
			dataset := *c.Measurement.Dataset
			dataset.UID = "other-dataset-uid"
			c.Measurement.Dataset = &dataset
		}, "measurement-protocol facts"},
	} {
		t.Run(name, func(t *testing.T) {
			drifted := curvePlanRunConfig()
			test.mutate(&drifted)
			if err := validateCurvePlanRunConfig(drifted, drifted.TargetNode.UID, spec, spec.ActiveProtocol); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("preprocessing drift error = %v", err)
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
		{
			name: "wall-power labels",
			mutate: func(idle *profilingrun.RunConfig) {
				idle.Metrics.MetricLabels = map[string]string{"meter": "different"}
			},
		},
		{
			name: "model identity",
			mutate: func(idle *profilingrun.RunConfig) {
				idle.Candidate.Inputs.ModelSpec.UID = "different-model-uid"
			},
		},
		{
			name: "preprocessor",
			mutate: func(idle *profilingrun.RunConfig) {
				idle.State.Preprocessor.Path = "/artifact/other"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			active := curvePlanRunConfig()
			idle := active
			idle.Measurement = profilingrun.MeasurementContract{
				Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
				WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
			}
			test.mutate(&idle)
			if _, err := ProjectCurvePlanSpec(
				"test-curve", active, idle,
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
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
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
		Firstfloor:   modelcompat.CloneFirstfloorRuntimeSettings(active.State.Firstfloor),
		RuntimeImage: active.Images.Runtime, PowerMode: active.State.PowerMode,
		CPU:            projectCurveCPU(active.CPU),
		MetricSource:   projectCurveMetricSource(active.Metrics),
		ActiveProtocol: activeProtocol, IdleProtocol: idleProtocol,
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: active.Candidate.ExecutionPath, Name: active.State.Artifact,
			Files: active.State.ArtifactFiles, Image: active.Images.Artifact,
		},
		Preprocessor:      projectCurvePreprocessor(active.State.Preprocessor),
		PlannedBatchSizes: []int{1},
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
	active.State.ArtifactFiles = []string{"/artifact/model.onnx"}
	active.State.RuntimeFamily = profilingrun.FirstfloorRuntimeFamily
	active.State.Backend = profilingrun.FirstfloorORTCPUBackend
	active.State.HostContract = nil
	active.Measurement.Batch = 2
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
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
		Firstfloor:   modelcompat.CloneFirstfloorRuntimeSettings(active.State.Firstfloor),
		RuntimeImage: active.Images.Runtime, PowerMode: active.State.PowerMode,
		CPU: projectCurveCPU(active.CPU), MetricSource: projectCurveMetricSource(active.Metrics),
		ActiveProtocol: activeProtocol, IdleProtocol: idleProtocol,
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: active.Candidate.ExecutionPath, Name: active.State.Artifact,
			Files: active.State.ArtifactFiles, Image: active.Images.Artifact,
		},
		Preprocessor:      projectCurvePreprocessor(active.State.Preprocessor),
		PlannedBatchSizes: []int{1, 2},
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
	config.State.ArtifactFiles = []string{"/artifact/model.rknn"}
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
