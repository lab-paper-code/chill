package projection

import (
	"slices"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/firstfloor"
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

func TestCurvePlanIdleBatchPolicy(t *testing.T) {
	for _, backend := range []string{profilingrun.FirstfloorORTCPUBackend, profilingrun.FirstfloorORTCUDABackend, profilingrun.FirstfloorRKNNLiteBackend} {
		t.Run(backend, func(t *testing.T) {
			active := curvePlanRunConfig()
			active.State.RuntimeFamily, active.State.Backend = profilingrun.FirstfloorRuntimeFamily, backend
			active.Candidate.ExecutionPath = backend
			if backend == profilingrun.FirstfloorRKNNLiteBackend {
				active.Candidate.ExecutionPath = "rknn-bs4"
			}
			active.Measurement.Batch = 4
			idle := active
			wantBatch := 1
			if backend == profilingrun.FirstfloorRKNNLiteBackend {
				wantBatch = 4
			}
			idle.Measurement = profilingrun.MeasurementContract{
				Batch: wantBatch, SettlingDurationSeconds: 60, DurationSeconds: 30,
				WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
			}
			spec, err := ProjectCurvePlanSpec("test-curve", active, idle, profilederivation.CurveConversionContract{}, []int{4})
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCurvePlanRunConfigs(active, idle, active.TargetNode.UID, idle.TargetNode.UID, spec); err != nil {
				t.Fatal(err)
			}
			idle.Measurement.Batch = 5 - wantBatch
			if err := ValidateCurvePlanRunConfigs(active, idle, active.TargetNode.UID, idle.TargetNode.UID, spec); err == nil {
				t.Fatal("wrong idle measurement batch was accepted")
			}
		})
	}
}

func TestProjectCurvePlanSpecRoundTripsThroughRunConfigValidation(t *testing.T) {
	active := curvePlanRunConfig()
	active.Actor = firstfloor.Config{
		RuntimeImage: active.Images.Runtime, Backend: active.State.Backend,
		MemoryRequestBytes: 1024, MemoryLimitBytes: 1024,
		BatchSizes: []int{1, 2}, WarmupBatchSizes: []int{1, 2}, WarmupIterations: 1,
	}
	active.Actor.BuildRunnerCommand()
	active.Measurement.SLOPercentile = 95
	active.Measurement.StartupWarmupIterations = 1
	active.Measurement.MinServiceSamples = 59
	active.Measurement.SLOTargetLatencyNanos = 100000000
	active.Measurement.WorkloadName = "workload"
	active.Measurement.WorkloadUID = "workload-uid"
	active.Measurement.WorkloadResourceVersion = "version"
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
	}
	spec, err := ProjectCurvePlanSpec("test-curve", active, idle, profilederivation.CurveConversionContract{
		TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
	}, []int{1, 2})
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
	if spec.Actor.MemoryLimitBytes != active.Actor.MemoryLimitBytes ||
		!slices.Equal(spec.Actor.WarmupBatchSizes, active.Actor.WarmupBatchSizes) ||
		spec.ActiveProtocol.MinServiceSamples != active.Measurement.MinServiceSamples ||
		spec.ActiveProtocol.StartupWarmupIterations != active.Measurement.StartupWarmupIterations ||
		spec.ActiveProtocol.SLOPercentile != active.Measurement.SLOPercentile ||
		spec.ActiveProtocol.SLOTargetLatencyNanos != active.Measurement.SLOTargetLatencyNanos ||
		spec.ActiveProtocol.WorkloadUID != active.Measurement.WorkloadUID ||
		spec.ActiveProtocol.WorkloadResourceVersion != active.Measurement.WorkloadResourceVersion {
		t.Fatalf("actor or sample-stop provenance lost: %+v", spec)
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
	before, err := ProjectRunIdentities(active, active.TargetNode.UID)
	if err != nil {
		t.Fatal(err)
	}
	active.Measurement.WorkloadResourceVersion = "status-only-new-version"
	active.Actor.WarmupIterations = 2
	active.Actor.WarmupBatchSizes = []int{1}
	active.Actor.BatchSizes = []int{1}
	active.Actor.RuntimePackages = map[string]string{"runtime": "updated-observation"}
	active.Actor.BuildRunnerCommand()
	active.Measurement.StartupWarmupIterations = 2
	active.Measurement.WarmupBatchSizes = []int{1}
	if err := validateCurvePlanRunConfig(active, active.TargetNode.UID, recorded, recorded.ActiveProtocol); err != nil {
		t.Fatalf("Workload observation provenance blocked curve admission: %v", err)
	}
	after, err := ProjectRunIdentities(active, active.TargetNode.UID)
	if err != nil {
		t.Fatal(err)
	}
	if before.MeasurementProtocolIdentity != after.MeasurementProtocolIdentity ||
		recorded.ActiveProtocol.WorkloadResourceVersion != "version" {
		t.Fatal("ResourceVersion changed selected identity or lost raw provenance")
	}
}

func TestCurveAndStateIdentityIgnoreObservationMetadata(t *testing.T) {
	active := curvePlanRunConfig()
	active.State.RuntimeFamily, active.State.Backend = profilingrun.FirstfloorRuntimeFamily, profilingrun.FirstfloorORTCPUBackend
	active.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	idle := active
	active.TargetNode.AllocatableCPU = ""
	idle.TargetNode.AllocatableCPU = "12"
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
	}
	spec, err := ProjectCurvePlanSpec("test-curve", active, idle, profilederivation.CurveConversionContract{}, []int{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	before, err := ProjectRunIdentities(active, active.TargetNode.UID)
	if err != nil {
		t.Fatal(err)
	}
	active.TargetNode.AllocatableCPU = "2"
	active.Candidate.Inputs.ModelSpec.UID = "recreated-model"
	active.Candidate.Inputs.DeviceClass.UID = "recreated-class"
	active.Metrics.Endpoint = "http://moved-meter.test/metrics"
	active.Metrics.MetricLabels = map[string]string{"meter": "updated-label"}
	active.State.Preprocessor.UID = "recreated-preprocessor"
	active.State.Preprocessor.Name = "renamed-preprocessor"
	active.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 64}
	active.State.HostContract = &profilingrun.CandidateReportHostContract{
		Driver: profilingrun.CandidateReportHostDriver{Version: "updated-driver-observation"},
	}
	active.Delivery.HostContract = active.State.HostContract
	active.Delivery.RuntimePackages = []profilingrun.CandidateReportRuntimePackage{{Name: "runtime", Version: "updated-observation"}}
	active.Measurement.WorkloadUID = "recreated-workload"
	dataset := *active.Measurement.Dataset
	dataset.UID = "recreated-dataset"
	active.Measurement.Dataset = &dataset
	after, err := ProjectRunIdentities(active, active.TargetNode.UID)
	if err != nil {
		t.Fatal(err)
	}
	if before.StateIdentity != after.StateIdentity || before.MeasurementProtocolIdentity != after.MeasurementProtocolIdentity {
		t.Fatal("observation metadata changed frozen execution identity")
	}
	if err := validateCurvePlanRunConfig(active, active.TargetNode.UID, spec, spec.ActiveProtocol); err != nil {
		t.Fatalf("observation metadata blocked curve execution: %v", err)
	}
}

func TestCurvePlanProjectionFreezesPreprocessingInputs(t *testing.T) {
	active := curvePlanRunConfig()
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
	}
	spec, err := ProjectCurvePlanSpec("test-curve", active, idle, profilederivation.CurveConversionContract{}, []int{1, 2})
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
			if _, err := ProjectCurvePlanSpec("test-curve", active, idle, profilederivation.CurveConversionContract{
				TargetPlatform: "rk3588", Tool: "rknn-toolkit2", ToolVersion: "2.3.2",
			}, []int{1, 2}); err == nil {
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
	activeProtocol := projectCurveProtocol(active)
	idleProtocol := projectCurveProtocol(idle)
	shared := profilederivation.CurvePlanSpec{
		Name: "test-curve", LogicalModel: active.State.Model,
		DeviceClass: active.State.DeviceClass, NodeUID: active.TargetNode.UID,
		RuntimeFamily: active.State.RuntimeFamily, Backend: active.State.Backend,
		Firstfloor:   modelcompat.CloneFirstfloorRuntimeSettings(active.State.Firstfloor),
		RuntimeImage: active.Images.Runtime, PowerMode: active.State.PowerMode,
		MetricSource:   projectCurveMetricSource(active.Metrics),
		ActiveProtocol: activeProtocol, IdleProtocol: idleProtocol,
		Artifact: profilederivation.CurveArtifactContract{
			ExecutionPath: active.Candidate.ExecutionPath, Name: active.State.Artifact,
			Files: active.State.ArtifactFiles, Image: active.Images.Artifact,
		},
		Preprocessor:      projectCurvePreprocessor(active.State.Preprocessor),
		PlannedBatchSizes: []int{1},
	}
	if err := ValidateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}
	drifted := active
	drifted.Images.Artifact = "different"
	if err := ValidateCurvePlanRunConfigs(drifted, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("artifact-supply drift was accepted")
	}
	drifted = idle
	drifted.TargetNode.UID = "other-node"
	if err := ValidateCurvePlanRunConfigs(active, drifted, "node-uid", "other-node", shared); err == nil {
		t.Fatal("Node drift was accepted")
	}
	drifted = idle
	drifted.Candidate.ExecutionPath = "different-path"
	if err := ValidateCurvePlanRunConfigs(active, drifted, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("active/idle execution-path disagreement was accepted")
	}
	for name, mutate := range map[string]func(*profilingrun.RunConfig){
		"model":          func(c *profilingrun.RunConfig) { c.State.Model = "other-model" },
		"backend":        func(c *profilingrun.RunConfig) { c.State.Backend = "other-backend" },
		"power":          func(c *profilingrun.RunConfig) { c.State.PowerMode = "other-power-mode" },
		"native inputs":  func(c *profilingrun.RunConfig) { c.Actor.Inputs = []string{"other-input"} },
		"native command": func(c *profilingrun.RunConfig) { c.Actor.RunnerCommand = []string{"/other-runner"} },
	} {
		t.Run(name, func(t *testing.T) {
			drifted := active
			mutate(&drifted)
			if err := ValidateCurvePlanRunConfigs(drifted, idle, "node-uid", "node-uid", shared); err == nil {
				t.Fatalf("different actual %s entered the frozen curve", name)
			}
		})
	}
}

func TestVerifiedCurvePlanUsesBatchOneORTIdleWitness(t *testing.T) {
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
	activeProtocol := projectCurveProtocol(active)
	idleProtocol := projectCurveProtocol(idle)
	shared := profilederivation.CurvePlanSpec{
		Name: "test-curve", LogicalModel: active.State.Model,
		DeviceClass: active.State.DeviceClass, NodeUID: active.TargetNode.UID,
		RuntimeFamily: active.State.RuntimeFamily, Backend: active.State.Backend,
		Firstfloor:   modelcompat.CloneFirstfloorRuntimeSettings(active.State.Firstfloor),
		RuntimeImage: active.Images.Runtime, PowerMode: active.State.PowerMode,
		MetricSource:   projectCurveMetricSource(active.Metrics),
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
	if err := ValidateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatal(err)
	}

	idle.Measurement.Batch = 2
	if err := ValidateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted a non-canonical idle batch")
	}
	idle.Measurement.Batch = 1
	active.Measurement.Batch = 3
	if err := ValidateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted an unplanned active batch")
	}

	shared.PlannedBatchSizes = []int{2, 4}
	active.Measurement.Batch = 4
	idle.Measurement.Batch = 1
	if err := ValidateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err != nil {
		t.Fatalf("batch one was not accepted as the ORT idle witness: %v", err)
	}
	idle.Measurement.Batch = 2
	if err := ValidateCurvePlanRunConfigs(active, idle, "node-uid", "node-uid", shared); err == nil {
		t.Fatal("curve plan accepted a non-canonical ORT idle batch")
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
	config.Measurement.Batch = 1
	config.Delivery.ArtifactCapability = &profilingrun.CandidateArtifactCapability{
		Tensors: []modelcompat.InputTensor{
			{Name: "input", Datatype: "FP32", SampleShape: []int32{224, 224, 3}},
		},
	}
	return config
}
