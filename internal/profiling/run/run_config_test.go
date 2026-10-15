package profilingrun

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeTestRunConfig(payload []byte) (RunConfig, error) {
	var config RunConfig
	err := json.Unmarshal(payload, &config)
	return config, err
}

func TestRunConfigDecoderRejectsExplicitNullActuation(t *testing.T) {
	payload, err := json.Marshal(validRunConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.TrimSuffix(string(payload), "}") + `,"actuation":null}`)
	if _, err := decodeTestRunConfig(payload); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("explicit null actuation was not rejected: %v", err)
	}
}

func TestCurrentRunConfigBindsOptionalHostContract(t *testing.T) {
	config := validRunConfig()
	contract := rknnHostContractForTest()
	config.State.HostContract = &contract
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentRKNNLiteRunConfigOmitsORTThreadSettings(t *testing.T) {
	config := firstfloorFrozenConfig(FirstfloorRKNNLiteBackend)
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "ortIntraOpThreads") || strings.Contains(string(payload), "ortInterOpThreads") {
		t.Fatalf("RKNNLite config retained ORT-only CPU settings: %s", payload)
	}
	withORTSettings := config
	withORTSettings.CPU.ORTIntraOpThreads = 4
	withORTSettings.CPU.ORTInterOpThreads = 1
	if err := withORTSettings.Validate(); err == nil || !strings.Contains(err.Error(), "ONNX Runtime thread") {
		t.Fatalf("RKNNLite config accepted ORT-only settings: %v", err)
	}
}

func TestCurrentRunConfigValidatesTypedActuation(t *testing.T) {
	policyName := "node-power-default"
	config := validRunConfig()
	config.State.PowerMode = testActuatedNVPMode
	config.Actuation = &ActuationContract{
		PowerMode: config.State.PowerMode, PolicyName: policyName,
		Actuator: ActuatorTarget{
			Kind:           ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &JetsonNVPModelTarget{ID: 2},
		},
	}
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := config.CanonicalJSON()
	if err != nil || !strings.Contains(string(payload), `"actuation":{"powerMode":"`+testActuatedNVPMode+`"`) {
		t.Fatalf("canonical actuation = %s, error = %v", payload, err)
	}

	clockMode := int32(1)
	clock := validRunConfig()
	clock.State.PowerMode = "gpu-1200"
	clock.Actuation = &ActuationContract{
		PowerMode: clock.State.PowerMode, PolicyName: policyName,
		Actuator: ActuatorTarget{
			Kind:                 ActuatorKindNvidiaLockedGPUClock,
			NvidiaLockedGPUClock: &NvidiaLockedGPUClockTarget{MinMHz: 1200, MaxMHz: 1200, Mode: &clockMode},
		},
	}
	if err := clock.Validate(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*RunConfig)
	}{
		{name: "missing contract", mutate: func(candidate *RunConfig) { candidate.Actuation = nil }},
		{name: "mode mismatch", mutate: func(candidate *RunConfig) { candidate.Actuation.PowerMode = "different-mode" }},
		{name: "missing policy identity", mutate: func(candidate *RunConfig) { candidate.Actuation.PolicyName = "" }},
		{name: "negative NVPModel ID", mutate: func(candidate *RunConfig) { candidate.Actuation.Actuator.JetsonNVPModel.ID = -1 }},
		{name: "mixed actuator union", mutate: func(candidate *RunConfig) {
			candidate.Actuation.Actuator.NvidiaLockedGPUClock = &NvidiaLockedGPUClockTarget{MinMHz: 1, MaxMHz: 2}
		}},
		{name: "unsupported schema", mutate: func(candidate *RunConfig) { candidate.SchemaVersion = "foreign" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := config
			actuation := *config.Actuation
			candidate.Actuation = &actuation
			actuator := config.Actuation.Actuator
			candidate.Actuation.Actuator = actuator
			if actuator.JetsonNVPModel != nil {
				target := *actuator.JetsonNVPModel
				candidate.Actuation.Actuator.JetsonNVPModel = &target
			}
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("invalid actuation contract was accepted")
			}
		})
	}

	fixed := validRunConfig()
	fixed.Actuation = config.Actuation
	if err := fixed.Validate(); err == nil || !strings.Contains(err.Error(), "fixed power mode") {
		t.Fatalf("fixed RunConfig accepted actuation: %v", err)
	}
}

func TestRunConfigAcceptsConfiguredImageReference(t *testing.T) {
	config := validRunConfig()
	config.Images.Runtime = "registry/runtime:latest"
	if err := config.Validate(); err != nil {
		t.Fatalf("configured image reference rejected: %v", err)
	}
}

func TestCurrentRunConfigFreezesGenericWorkloadObservationContract(t *testing.T) {
	config := validRunConfig()
	keys, err := config.Workload.LabelKeys()
	if err != nil || strings.Join(keys, ",") !=
		"app.kubernetes.io/component,app.kubernetes.io/name,edge.dacs.io/workload-role" {
		t.Fatalf("contract label keys = %#v, error = %v", keys, err)
	}
	config.Workload.SchemaVersion = "unknown"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "current workload") {
		t.Fatalf("invalid workload contract error = %v", err)
	}
}

func TestMeasurementWorkloadRequiresCHILLRoleAndJobOwner(t *testing.T) {
	labels := map[string]string{MeasurementWorkloadLabelKey: MeasurementWorkloadLabelValue}
	if !IsMeasurementWorkload("Job", labels) || IsMeasurementWorkload("DaemonSet", labels) ||
		IsMeasurementWorkload("Job", map[string]string{}) {
		t.Fatal("measurement workload identity was not exact")
	}
}

func TestCurrentRunConfigValidatesModelLoadedIdleProtocol(t *testing.T) {
	idle := validRunConfig()
	idle.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: IdleModelLoadedWorkloadMode,
	}
	if err := idle.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RunConfig){
		func(i *RunConfig) { i.Measurement.Batch = 2 },
		func(i *RunConfig) { i.Measurement.WarmupIterations = 1 },
		func(i *RunConfig) { i.Measurement.SettlingDurationSeconds = 0 },
		func(i *RunConfig) { i.Measurement.Dataset = validRunConfig().Measurement.Dataset },
	} {
		candidate := idle
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid model-loaded idle protocol was accepted")
		}
	}
}

func TestCurrentRKNNLiteRunConfigAllowsArtifactBatchForIdle(t *testing.T) {
	idle := firstfloorFrozenConfig(FirstfloorRKNNLiteBackend)
	idle.Measurement = MeasurementContract{
		Batch: 2, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: IdleModelLoadedWorkloadMode,
	}
	if err := idle.Validate(); err != nil {
		t.Fatalf("fixed-batch RKNNLite idle protocol was rejected: %v", err)
	}
}

func rknnDeliveryForTest(batch int32, host *CandidateReportHostContract) CandidateRuntimeDelivery {
	capability := rknnArtifactCapabilityForTest()
	capability.DeclaredBatchSizes = []int32{batch}
	return CandidateRuntimeDelivery{
		ArtifactCapability: capability,
		RuntimePackages:    []CandidateReportRuntimePackage{{Name: "rknn-toolkit-lite2", Version: "2.3.2"}},
		HostContract:       host,
		RuntimeAccess:      &CandidateReportRuntimeAccess{},
	}
}

func TestCurrentRunConfigRequiresCleanUniqueArtifactFiles(t *testing.T) {
	for _, files := range [][]string{
		nil, {"/"}, {"artifact/model.onnx"}, {"/artifact/../model.onnx"}, {"/a/model.onnx", "/b/model.onnx"},
	} {
		config := validRunConfig()
		config.State.ArtifactFiles = files
		if err := config.Validate(); err == nil {
			t.Fatalf("expected artifact files %q rejection", files)
		}
	}
}

func TestCurrentRunConfigRequiresDatasetExactlyForActiveMeasurement(t *testing.T) {
	config := validRunConfig()
	config.Measurement.Dataset = nil
	if err := config.Validate(); err == nil {
		t.Fatal("active measurement without a dataset was accepted")
	}
}

func TestRunConfigRejectsUnknownImageField(t *testing.T) {
	payload, err := validRunConfig().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), `"metricObserver":`, `"unknown":true,"metricObserver":`, 1))
	if _, err := decodeTestRunConfig(payload); err == nil {
		t.Fatal("expected unknown image field rejection")
	}
}

func TestRunConfigRejectsCPURequestAboveSelectedLimit(t *testing.T) {
	config := validRunConfig()
	config.CPU.Request = "5"
	if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("expected CPU request/limit rejection, got %v", err)
	}
}

func TestRunConfigAcceptsConfiguredCPUQuotaBelowAllocatable(t *testing.T) {
	config := validRunConfig()
	config.TargetNode.AllocatableCPU = "12"
	config.CPU.Policy = ConfiguredCPUQuotaPolicy
	config.CPU.Limit = "4"
	if err := config.Validate(); err != nil {
		t.Fatalf("configured CPU quota was rejected: %v", err)
	}
}

func TestRunConfigRejectsCompatibilityAndPolicyDrift(t *testing.T) {
	tests := []func(*RunConfig){
		func(i *RunConfig) { i.Candidate.Verdict = "Unknown" },
		func(i *RunConfig) { i.TargetNode.ResourceVersion = "" },
		func(i *RunConfig) { i.CPU.Policy = "unknown" },
		func(i *RunConfig) { i.CPU.PolicyVersion = "v2" },
		func(i *RunConfig) { i.CPU.Policy = ConfiguredCPUQuotaPolicy; i.CPU.Limit = "500m" },
		func(i *RunConfig) { i.CPU.Policy = ConfiguredCPUQuotaPolicy; i.CPU.Limit = "5" },
		func(i *RunConfig) { i.CPU.ORTIntraOpThreads = 2 },
		func(i *RunConfig) { i.CPU.ExclusivityClaimed = true },
		func(i *RunConfig) { i.Metrics.NodeName = testDifferentIdentity },
	}
	for _, mutate := range tests {
		config := validRunConfig()
		mutate(&config)
		if err := config.Validate(); err == nil {
			t.Fatal("expected validation failure")
		}
	}
}

func TestCurrentRunConfigRequiresDistinctPrometheusMetricIdentities(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*RunConfig)
	}{
		{name: "missing thermal mapping", mutate: func(config *RunConfig) {
			config.Metrics.ThermalMetrics = ThermalMetricContract{}
		}},
		{name: "invalid metric name", mutate: func(config *RunConfig) {
			config.Metrics.ThermalMetrics.Active = "not-valid"
		}},
		{name: "duplicate metric name", mutate: func(config *RunConfig) {
			config.Metrics.ThermalMetrics.Active = config.Metrics.PowerMetric
		}},
		{name: "missing metric labels", mutate: func(config *RunConfig) {
			config.Metrics.MetricLabels = nil
		}},
		{name: "invalid metric label name", mutate: func(config *RunConfig) {
			config.Metrics.MetricLabels = map[string]string{"not-valid": "value"}
		}},
		{name: "empty metric label value", mutate: func(config *RunConfig) {
			config.Metrics.MetricLabels = map[string]string{"hostname": ""}
		}},
		{name: "unclean thermal endpoint", mutate: func(config *RunConfig) {
			config.Metrics.ThermalEndpoint = " http://thermal.test/metrics "
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validRunConfig()
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("invalid metric identities were accepted")
			}
		})
	}
}

func TestVerifiedRunConfigOwnsMetricLabels(t *testing.T) {
	config := validRunConfig()
	verified, err := ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Metrics.MetricLabels["hostname"] = "caller-mutation"
	first, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if first.Metrics.MetricLabels["hostname"] != "lattepanda" {
		t.Fatalf("verified config retained caller labels: %#v", first.Metrics.MetricLabels)
	}
	first.Metrics.MetricLabels["hostname"] = "record-mutation"
	second, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if second.Metrics.MetricLabels["hostname"] != "lattepanda" {
		t.Fatalf("verified config retained returned labels: %#v", second.Metrics.MetricLabels)
	}
}

func TestFrozenRunConfigOwnsArtifactAndPreprocessorContract(t *testing.T) {
	config := firstfloorFrozenConfig(FirstfloorRKNNLiteBackend)
	verified, err := ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	config.Delivery.ArtifactCapability.Tensors[0].SampleShape[0] = 999
	config.State.ArtifactFiles[0] = testMutatedPath
	config.State.Preprocessor.Inputs[0] = testMutatedValue
	config.Measurement.Dataset.Name = testMutatedValue
	record, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if record.Delivery.ArtifactCapability.Tensors[0].SampleShape[0] != 224 ||
		record.State.ArtifactFiles[0] != "/artifact/mobilenet-v2-050.onnx" ||
		record.State.Preprocessor.Inputs[0] != "image" || record.Measurement.Dataset.Name != "vqav2-val-subset" {
		t.Fatalf("frozen RunConfig was mutated through caller state: %+v", record)
	}
	record.State.Preprocessor.Outputs[0] = testMutatedValue
	second, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if second.State.Preprocessor.Outputs[0] != "label" {
		t.Fatal("returned RunConfig mutated the frozen record")
	}
}

func TestRunConfigRejectsInvalidInputDeclarations(t *testing.T) {
	for _, name := range []string{"missing", "blank-name", testDatatypeField, "empty-shape", "nonpositive-shape"} {
		t.Run(name, func(t *testing.T) {
			config := firstfloorFrozenConfig(FirstfloorRKNNLiteBackend)
			capability := config.Delivery.ArtifactCapability
			switch name {
			case "missing":
				capability.Tensors = nil
			case "blank-name":
				capability.Tensors[0].Name = " "
			case testDatatypeField:
				capability.Tensors[0].Datatype = "float32"
			case "empty-shape":
				capability.Tensors[0].SampleShape = nil
			case "nonpositive-shape":
				capability.Tensors[0].SampleShape[0] = 0
			}
			if err := config.Validate(); err == nil || !strings.Contains(err.Error(), "input") {
				t.Fatalf("invalid input contract accepted: %v", err)
			}
		})
	}
}

func TestFirstfloorRunConfigFreezesInputTensorsForRKNNOnly(t *testing.T) {
	config := firstfloorFrozenConfig(FirstfloorORTCPUBackend)
	config.Delivery.ArtifactCapability.Tensors = rknnArtifactCapabilityForTest().Tensors
	if err := config.Validate(); err == nil {
		t.Fatal("ONNX firstfloor RunConfig accepted artifact input tensors")
	}
}

func validRunConfig() RunConfig {
	d := "sha256:" + strings.Repeat("a", 64)
	image := "registry.local/chill/image@" + d
	return RunConfig{
		SchemaVersion: RunConfigSchemaVersion,
		Candidate: CandidateIdentity{
			Inputs: CandidateReportInputs{
				ModelSpec: CandidateReportObjectInput{
					Name: "mobilenet-v2-050", UID: "model-uid", ResourceVersion: "1",
				},
				DeviceClass: CandidateReportObjectInput{
					Name: "lattepanda-3-delta-8g", UID: "class-uid", ResourceVersion: "1",
				},
				RuntimeDeclaration: CandidateReportRuntimeInput{
					Namespace: "chill-system", Name: "profiling-execution-facts", UID: "facts-uid",
					ResourceVersion: "1", Key: "execution-facts.yaml", Image: image,
				},
			},
			ExecutionPath: "ort-cpu",
			Verdict:       "Compatible",
		},
		State: ExecutionState{
			DeviceClass:   "lattepanda-3-delta-8g",
			Model:         "mobilenet-v2-050",
			Artifact:      "canonical-onnx",
			ArtifactFiles: []string{"/artifact/mobilenet-v2-050.onnx"},
			RuntimeFamily: "onnxruntime",
			Backend:       cpuExecutionProvider,
			PowerMode:     "fixed",
			Preprocessor: PreprocessorState{
				Name: "mobilenet-imagenet", UID: "preprocessor-uid", Image: image, Architecture: "amd64",
				Path: "/artifact/mobilenet-nchw", Inputs: []string{"image"}, Outputs: []string{"label"},
			},
		},
		Images: Images{
			Runtime: image, Artifact: image, MetricObserver: image, WorkloadObserver: image,
		},
		Delivery: CandidateRuntimeDelivery{ArtifactCapability: &CandidateArtifactCapability{}},
		TargetNode: NodeSnapshot{
			Name:            "lattepanda",
			UID:             "uid",
			ResourceVersion: "1",
			Architecture:    "amd64",
			AllocatableCPU:  "4",
		},
		CPU: CPUContract{
			Policy:            AllocatableCPUQuotaPolicy,
			PolicyVersion:     CPUQuotaPolicyVersion,
			Request:           "100m",
			Limit:             "4",
			ORTIntraOpThreads: 4,
			ORTInterOpThreads: 1,
		},
		Measurement: MeasurementContract{
			Batch:            1,
			WarmupIterations: 20,
			DurationSeconds:  30,
			WorkloadMode:     "saturated-serial-fixed-batch",
			Dataset: &DatasetContract{
				Name: "vqav2-val-subset", UID: "dataset-uid", Image: image, Path: "/artifact/vqav2-val-subset",
			},
		},
		Metrics: MetricContract{
			SourceKind:           "edge-metrics",
			NodeName:             "lattepanda",
			Endpoint:             "http://central-meter.test:18766/prometheus?hostname=lattepanda",
			PowerMetric:          "shelly_power_total_watts",
			MetricLabels:         map[string]string{"hostname": "lattepanda", "device_type": "test_device"},
			ThermalEndpoint:      "http://edge-metric.monitoring.svc:9102/metrics",
			ThermalMetrics:       testThermalMetricContract(),
			IntervalMilliseconds: 1000,
			TimeoutMilliseconds:  500,
			DurationSeconds:      31,
		},
		Workload: CurrentWorkloadContract(),
	}
}

func testThermalMetricContract() ThermalMetricContract {
	return ThermalMetricContract{
		ObservationAvailable:      "device_thermal_throttle_observation_available",
		Active:                    "device_thermal_throttle_active",
		CPUCoreThrottleEvents:     "device_thermal_cpu_core_throttle_event_count",
		CPUPackageThrottleEvents:  "device_thermal_cpu_package_throttle_event_count",
		Latched:                   "device_thermal_throttle_latched",
		MaximumTemperatureCelsius: "device_thermal_max_celsius",
	}
}
