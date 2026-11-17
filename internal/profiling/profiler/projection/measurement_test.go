package projection

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"

	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestProjectionRequiresVerifiedEvidence(t *testing.T) {
	t.Parallel()
	if _, err := IdleTrial(profilerevidence.VerifiedIdleEvidence{}, profilerevidence.VerifiedIdleEvidence{}); err == nil {
		t.Fatal("empty idle evidence was accepted")
	}
}

func TestProjectIdleObservationPreservesRawProvenanceAndRuntimeWindow(t *testing.T) {
	t.Parallel()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	config := projectionRunConfig()
	config.Measurement.SettlingDurationSeconds = 60
	execution := profilerevidence.ExecutionEvidence{
		RunConfig: config,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "chill-evidence", Key: "runs/idle/raw.json",
			Size: 1024,
		},
		Terminal: profilerevidence.TerminalFacts{
			Pod:  profilerevidence.PodTerminalFacts{UID: "idle-pod"},
			Node: profilerevidence.NodeTerminalFacts{UID: "node-uid"},
		},
	}
	raw := profilerevidence.IdleRawMeasurement{
		RunConfig: config,
		Runtime: profilingrun.IdleRuntimeResult{
			Inputs:               config.State.Preprocessor.Inputs,
			ModelLoadCompletedAt: started.Add(-61 * time.Second).Format(time.RFC3339Nano),
			SettlingStartedAt:    started.Add(-60 * time.Second).Format(time.RFC3339Nano),
			SettlingCompletedAt:  started.Format(time.RFC3339Nano),
			MeasurementStartedAt: started.Format(time.RFC3339Nano),
			MeasurementEndedAt:   started.Add(30 * time.Second).Format(time.RFC3339Nano),
			ModelResidentAtEnd:   true,
		},
		Metrics: metricobserver.Result{Samples: []metricobserver.Sample{{ObservedAt: started, Watts: 4}}},
	}

	observation, err := projectIdleObservation(execution, raw)
	if err != nil {
		t.Fatal(err)
	}
	if observation.TrialID != "idle-pod" || observation.RawEvidence.Store != "chill-evidence" ||
		observation.RawEvidence.Key != "runs/idle/raw.json" ||
		!observation.ModelResidentAtEnd || observation.InferenceCountAfterModelLoad != 0 ||
		!observation.WindowStart.Equal(started) || !observation.WindowEnd.Equal(started.Add(30*time.Second)) {
		t.Fatalf("projected idle observation = %#v", observation)
	}
}

func projectionRunConfig() profilingrun.RunConfig {
	return profilingrun.RunConfig{
		State: profilingrun.ExecutionState{
			DeviceClass: "lattepanda", Model: "mobilenet", ArtifactFiles: []string{"/artifact/model.onnx"},
			RuntimeFamily: profilingrun.FirstfloorRuntimeFamily, Backend: profilingrun.FirstfloorORTCPUBackend,
			Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}, PowerMode: "fixed",
			Preprocessor: profilingrun.PreprocessorState{
				Name: "mobilenet-imagenet", UID: "preprocessor-uid", Image: "preprocessor", Architecture: "amd64",
				Path: "/artifact/mobilenet-nchw", Inputs: []string{"image"}, Outputs: []string{"label"},
			},
		},
		Images: profilingrun.Images{Runtime: "runtime", Artifact: "artifact", MetricObserver: "observer"},
		Delivery: profilingrun.CandidateRuntimeDelivery{
			ArtifactCapability: &profilingrun.CandidateArtifactCapability{},
		},
		TargetNode: profilingrun.NodeSnapshot{Name: "lattepanda", UID: "node-uid"},
		Measurement: profilingrun.MeasurementContract{
			Batch: 2, WarmupIterations: 20, DurationSeconds: 30, WorkloadMode: "empirical-cadence-fixed-batch",
			OperatingPoints: &profilingrun.OperatingPointsContract{
				Utilizations: []float64{.5}, SparseMeanGapMilliseconds: 1000, PhaseDurationSeconds: 30, PostLoadDurationSeconds: 30,
				RequestLifetimeSeconds: 30, DeliveryMarginMilliseconds: 100,
				FramedDecodedRequestUpperBytes: 1024, QueueBytesCap: 32768,
			},
			Dataset: &profilingrun.DatasetContract{
				Name: "vqav2-val-subset", UID: "dataset-uid", Image: "dataset", Path: "/artifact/vqav2-val-subset",
			},
		},
		Metrics: profilingrun.MetricContract{
			SourceKind: "edge-metrics", NodeName: "lattepanda",
			Endpoint: "http://central-meter/prometheus?hostname=lattepanda", PowerMetric: "watts",
			MetricLabels:    map[string]string{"hostname": "lattepanda", "device_type": "test_device"},
			ThermalEndpoint: "http://thermal/metrics",
			ThermalMetrics: profilingrun.ThermalMetricContract{
				ObservationAvailable: "thermal_observation_available", Active: "thermal_active",
				CPUCoreThrottleEvents: "thermal_cpu_core_events", CPUPackageThrottleEvents: "thermal_cpu_package_events",
				Latched: "thermal_latched", MaximumTemperatureCelsius: "thermal_max_celsius",
			},
			IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31,
		},
	}
}
