package profilingrun

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeTestIntent(payload []byte) (Intent, error) {
	var intent Intent
	err := json.Unmarshal(payload, &intent)
	return intent, err
}

func TestIntentDecoderRejectsExplicitNullActuation(t *testing.T) {
	payload, err := json.Marshal(validIntent())
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.TrimSuffix(string(payload), "}") + `,"actuation":null}`)
	if _, err := decodeTestIntent(payload); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("explicit null actuation was not rejected: %v", err)
	}
}

func TestCurrentIntentBindsOptionalHostContract(t *testing.T) {
	intent := validIntent()
	contract := rknnHostContractForTest()
	intent.State.HostContract = &contract
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentRKNNLiteIntentOmitsORTThreadSettings(t *testing.T) {
	intent := validIntent()
	intent.Candidate.ExecutionPath = "rknnlite-bs2"
	intent.State.RuntimeFamily = RKNNLiteRuntimeFamily
	intent.State.Backend = RKNNLiteBackend
	contract := rknnHostContractForTest()
	intent.State.HostContract = &contract
	intent.CPU.ORTIntraOpThreads = 0
	intent.CPU.ORTInterOpThreads = 0
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := intent.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "ortIntraOpThreads") || strings.Contains(string(payload), "ortInterOpThreads") {
		t.Fatalf("RKNNLite intent retained ORT-only CPU settings: %s", payload)
	}
	if IsSupportedCPUORTIntent(intent) {
		t.Fatal("RKNNLite intent was classified as CPU ORT")
	}
	if !IsSupportedRKNNLiteIntent(intent) {
		t.Fatal("current host-bound RKNNLite intent was not classified as supported")
	}

	withORTSettings := intent
	withORTSettings.CPU.ORTIntraOpThreads = 4
	withORTSettings.CPU.ORTInterOpThreads = 1
	if err := withORTSettings.Validate(); err == nil || !strings.Contains(err.Error(), "ONNX Runtime thread") {
		t.Fatalf("RKNNLite intent accepted ORT-only settings: %v", err)
	}
}

func TestCurrentIntentValidatesTypedActuation(t *testing.T) {
	policyName := "node-power-default"
	intent := validIntent()
	intent.State.PowerMode = testActuatedNVPMode
	intent.Actuation = &ActuationContract{
		PowerMode: intent.State.PowerMode, PolicyName: policyName,
		Actuator: ActuatorTarget{
			Kind:           ActuatorKindJetsonNVPModel,
			JetsonNVPModel: &JetsonNVPModelTarget{ID: 2},
		},
	}
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	payload, err := intent.CanonicalJSON()
	if err != nil || !strings.Contains(string(payload), `"actuation":{"powerMode":"`+testActuatedNVPMode+`"`) {
		t.Fatalf("canonical actuation = %s, error = %v", payload, err)
	}

	clockMode := int32(1)
	clock := validIntent()
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
		mutate func(*Intent)
	}{
		{name: "missing contract", mutate: func(candidate *Intent) { candidate.Actuation = nil }},
		{name: "mode mismatch", mutate: func(candidate *Intent) { candidate.Actuation.PowerMode = "different-mode" }},
		{name: "missing policy identity", mutate: func(candidate *Intent) { candidate.Actuation.PolicyName = "" }},
		{name: "negative NVPModel ID", mutate: func(candidate *Intent) { candidate.Actuation.Actuator.JetsonNVPModel.ID = -1 }},
		{name: "mixed actuator union", mutate: func(candidate *Intent) {
			candidate.Actuation.Actuator.NvidiaLockedGPUClock = &NvidiaLockedGPUClockTarget{MinMHz: 1, MaxMHz: 2}
		}},
		{name: "retired predecessor schema", mutate: func(candidate *Intent) {
			candidate.SchemaVersion = "chill.dacs.io/profiling-run-intent.v1alpha12"
		}},
		{name: "unsupported schema", mutate: func(candidate *Intent) { candidate.SchemaVersion = "foreign" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := intent
			actuation := *intent.Actuation
			candidate.Actuation = &actuation
			actuator := intent.Actuation.Actuator
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

	fixed := validIntent()
	fixed.Actuation = intent.Actuation
	if err := fixed.Validate(); err == nil || !strings.Contains(err.Error(), "fixed power mode") {
		t.Fatalf("fixed intent accepted actuation: %v", err)
	}
}

func TestSupportedCPUORTIntentUsesRuntimeContractNotCatalogPathName(t *testing.T) {
	intent := validIntent()
	intent.Candidate.ExecutionPath = "ort-cpu-derived"
	if err := intent.Validate(); err != nil {
		t.Fatal(err)
	}
	if !IsSupportedCPUORTIntent(intent) || IsSupportedRKNNLiteIntent(intent) {
		t.Fatal("CPU ORT adapter selection did not follow the frozen runtime contract")
	}
}

func TestIntentAcceptsConfiguredImageReference(t *testing.T) {
	intent := validIntent()
	intent.Images.Runtime = "registry/runtime:latest"
	if err := intent.Validate(); err != nil {
		t.Fatalf("configured image reference rejected: %v", err)
	}
}

func TestCurrentIntentFreezesGenericWorkloadObservationContract(t *testing.T) {
	intent := validIntent()
	keys, err := intent.Workload.LabelKeys()
	if err != nil || strings.Join(keys, ",") !=
		"app.kubernetes.io/component,app.kubernetes.io/name,edge.dacs.io/workload-role" {
		t.Fatalf("contract label keys = %#v, error = %v", keys, err)
	}
	intent.Workload.SchemaVersion = "unknown"
	if err := intent.Validate(); err == nil || !strings.Contains(err.Error(), "current workload") {
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

func TestCurrentIntentValidatesModelLoadedIdleProtocol(t *testing.T) {
	idle := validIntent()
	idle.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
		OutputValidation: NoOutputValidation,
	}
	if err := idle.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Intent){
		func(i *Intent) { i.Measurement.Batch = 2 },
		func(i *Intent) { i.Measurement.WarmupIterations = 1 },
		func(i *Intent) { i.Measurement.SettlingDurationSeconds = 0 },
		func(i *Intent) { i.Measurement.InputGenerator = "seeded" },
		func(i *Intent) { i.Measurement.OutputValidation = "shape" },
	} {
		candidate := idle
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatal("invalid model-loaded idle protocol was accepted")
		}
	}
}

func TestCurrentRKNNLiteIntentAllowsArtifactBatchForIdle(t *testing.T) {
	idle := validIntent()
	idle.Candidate.ExecutionPath = "rknnlite-bs2"
	idle.State.RuntimeFamily = RKNNLiteRuntimeFamily
	idle.State.Backend = RKNNLiteBackend
	contract := rknnHostContractForTest()
	idle.State.HostContract = &contract
	idle.CPU.ORTIntraOpThreads = 0
	idle.CPU.ORTInterOpThreads = 0
	idle.Measurement = MeasurementContract{
		Batch: 2, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
		OutputValidation: NoOutputValidation,
	}
	if err := idle.Validate(); err != nil {
		t.Fatalf("fixed-batch RKNNLite idle protocol was rejected: %v", err)
	}
}

func TestCurrentIntentRequiresCleanArtifactPath(t *testing.T) {
	for _, artifactPath := range []string{"", "/", "artifact/model.onnx", "/artifact/../model.onnx"} {
		intent := validIntent()
		intent.State.ArtifactPath = artifactPath
		if err := intent.Validate(); err == nil {
			t.Fatalf("expected artifact path %q rejection", artifactPath)
		}
	}
}

func TestIntentRejectsUnknownImageField(t *testing.T) {
	payload, err := validIntent().CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), `"metricObserver":`, `"unknown":true,"metricObserver":`, 1))
	if _, err := decodeTestIntent(payload); err == nil {
		t.Fatal("expected unknown image field rejection")
	}
}

func TestIntentRejectsCPURequestAboveFrozenLimit(t *testing.T) {
	intent := validIntent()
	intent.CPU.Request = "5"
	if err := intent.Validate(); err == nil || !strings.Contains(err.Error(), "must not exceed") {
		t.Fatalf("expected CPU request/limit rejection, got %v", err)
	}
}

func TestIntentAcceptsConfiguredCPUQuotaBelowAllocatable(t *testing.T) {
	intent := validIntent()
	intent.TargetNode.AllocatableCPU = "12"
	intent.CPU.Policy = ConfiguredCPUQuotaPolicy
	intent.CPU.Limit = "4"
	if err := intent.Validate(); err != nil {
		t.Fatalf("configured CPU quota was rejected: %v", err)
	}
}

func TestIntentRejectsCompatibilityAndPolicyDrift(t *testing.T) {
	tests := []func(*Intent){
		func(i *Intent) { i.Candidate.Verdict = "Unknown" },
		func(i *Intent) { i.TargetNode.ResourceVersion = "" },
		func(i *Intent) { i.CPU.Policy = "unknown" },
		func(i *Intent) { i.CPU.PolicyVersion = "v2" },
		func(i *Intent) { i.CPU.Policy = ConfiguredCPUQuotaPolicy; i.CPU.Limit = "500m" },
		func(i *Intent) { i.CPU.Policy = ConfiguredCPUQuotaPolicy; i.CPU.Limit = "5" },
		func(i *Intent) { i.CPU.ORTIntraOpThreads = 2 },
		func(i *Intent) { i.CPU.ExclusivityClaimed = true },
		func(i *Intent) { i.Metrics.NodeName = "other" },
	}
	for _, mutate := range tests {
		intent := validIntent()
		mutate(&intent)
		if err := intent.Validate(); err == nil {
			t.Fatal("expected validation failure")
		}
	}
}

func TestCurrentIntentRequiresDistinctPrometheusMetricIdentities(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Intent)
	}{
		{name: "missing thermal mapping", mutate: func(intent *Intent) {
			intent.Metrics.ThermalMetrics = ThermalMetricContract{}
		}},
		{name: "invalid metric name", mutate: func(intent *Intent) {
			intent.Metrics.ThermalMetrics.Active = "not-valid"
		}},
		{name: "duplicate metric name", mutate: func(intent *Intent) {
			intent.Metrics.ThermalMetrics.Active = intent.Metrics.PowerMetric
		}},
		{name: "missing metric labels", mutate: func(intent *Intent) {
			intent.Metrics.MetricLabels = nil
		}},
		{name: "invalid metric label name", mutate: func(intent *Intent) {
			intent.Metrics.MetricLabels = map[string]string{"not-valid": "value"}
		}},
		{name: "empty metric label value", mutate: func(intent *Intent) {
			intent.Metrics.MetricLabels = map[string]string{"hostname": ""}
		}},
		{name: "unclean thermal endpoint", mutate: func(intent *Intent) {
			intent.Metrics.ThermalEndpoint = " http://thermal.test/metrics "
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			intent := validIntent()
			test.mutate(&intent)
			if err := intent.Validate(); err == nil {
				t.Fatal("invalid metric identities were accepted")
			}
		})
	}
}

func TestVerifiedIntentOwnsMetricLabels(t *testing.T) {
	intent := validIntent()
	verified, err := ValidateIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	intent.Metrics.MetricLabels["hostname"] = "caller-mutation"
	first, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if first.Metrics.MetricLabels["hostname"] != "lattepanda" {
		t.Fatalf("verified intent retained caller labels: %#v", first.Metrics.MetricLabels)
	}
	first.Metrics.MetricLabels["hostname"] = "record-mutation"
	second, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if second.Metrics.MetricLabels["hostname"] != "lattepanda" {
		t.Fatalf("verified intent retained returned labels: %#v", second.Metrics.MetricLabels)
	}
}

func validIntent() Intent {
	d := "sha256:" + strings.Repeat("a", 64)
	image := "registry.local/chill/image@" + d
	return Intent{
		SchemaVersion: SchemaVersion,
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
			ArtifactPath:  "/artifact/mobilenet-v2-050.onnx",
			RuntimeFamily: "onnxruntime",
			Backend:       "CPUExecutionProvider",
			PowerMode:     "fixed",
		},
		Images: Images{
			Runtime: image, Artifact: image, MetricObserver: image, WorkloadObserver: image,
		},
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
			InputGenerator:   "numpy-default-rng-seed-0-fp32-v1",
			OutputValidation: "nonempty-shape-batch-v1",
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
