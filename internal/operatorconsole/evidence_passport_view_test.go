package operatorconsole

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/powerobserver"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func TestBuildEvidencePassportProjectsBoundedMeasuredFacts(t *testing.T) {
	t.Parallel()
	run, _, execution := passportFixture()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	execution.FinalizedAt = started.Add(time.Minute)
	execution.RawMeasurement.Store = "chill-evidence"
	execution.RawMeasurement.Key = "raw.json"
	execution.RawMeasurement.Digest = run.Status.Evidence.Digest
	execution.RawMeasurement.Size = 512
	measurement := profilerevidence.RawMeasurement{
		Runtime: profilingrun.RuntimeResult{
			Runtime: "onnxruntime", RuntimeVersion: "1.16.0", Provider: "CPUExecutionProvider",
			Architecture: "x86_64", BatchSize: 4, WarmupIterations: 20,
			MeasurementStartedAt:      started.Format(time.RFC3339Nano),
			MeasurementEndedAt:        started.Add(30 * time.Second).Format(time.RFC3339Nano),
			MeasurementElapsedSeconds: 30.001, InferenceCount: 3,
			ThroughputItemsPerSecond: 400,
			RepetitionResults:        []profilingrun.Repetition{{LatencyMilliseconds: []float64{3, 1, 2}}},
			OutputValidation:         profilingrun.OutputValidation{Method: "shape", Passed: true},
		},
		Power: powerobserver.Result{
			Source:    powerobserver.SourceIdentity{Kind: "edge-metrics", NodeName: "lattepanda", Metric: "shelly_watts", Endpoint: "http://internal"},
			StartedAt: started, EndedAt: started.Add(31 * time.Second),
			Samples: []powerobserver.Sample{{Watts: 8}, {Watts: 10}, {Watts: 18}},
			Summary: powerobserver.Summary{
				Attempts: 3, SuccessfulSamples: 3, ObservationDurationSeconds: 31,
				MeanRequestLatencySeconds: 0.03, P95RequestLatencySeconds: 0.04,
				MaximumSampleGapSeconds: 1.1, SourceTimestampAbsent: true,
			},
		},
	}

	got, err := BuildEvidencePassport(EvidencePassportSnapshot{
		ReadAt: started.Add(2 * time.Minute), Run: *run, Execution: execution, Measurement: measurement,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.APIVersion != EvidencePassportAPIVersion || got.Kind != "EvidencePassport" ||
		got.Run.Name != run.Name || got.Evidence.Raw.Key != "raw.json" {
		t.Fatalf("passport identity = %#v", got)
	}
	runtime := got.Measurement.Runtime
	if runtime.CompletedCalls != 3 || runtime.CompletedItems != 12 || runtime.Latency.Count != 3 ||
		runtime.Latency.Mean != 2 || runtime.Latency.P50 != 2 || runtime.Latency.P95 != 3 || runtime.Latency.P99 != 3 {
		t.Fatalf("runtime facts = %#v", runtime)
	}
	power := got.Measurement.Power
	if power.Watts == nil || power.Watts.Minimum != 8 || power.Watts.Mean != 12 || power.Watts.Maximum != 18 ||
		power.Source.Kind != "edge-metrics" || power.Source.Metric != "shelly_watts" || power.Samples != 3 ||
		!power.SourceTimestampAbsent {
		t.Fatalf("power facts = %#v", power)
	}
}

func TestBuildEvidencePassportRepresentsNoSuccessfulPowerSamples(t *testing.T) {
	t.Parallel()
	run, _, execution := passportFixture()
	started := time.Date(2026, 7, 20, 1, 2, 3, 0, time.UTC)
	passport, err := BuildEvidencePassport(EvidencePassportSnapshot{
		ReadAt:    started,
		Run:       *run,
		Execution: execution,
		Measurement: profilerevidence.RawMeasurement{
			Runtime: profilingrun.RuntimeResult{
				MeasurementStartedAt: started.Format(time.RFC3339Nano),
				MeasurementEndedAt:   started.Add(time.Second).Format(time.RFC3339Nano),
				InferenceCount:       1,
				RepetitionResults:    []profilingrun.Repetition{{LatencyMilliseconds: []float64{1}}},
			},
			Power: powerobserver.Result{Summary: powerobserver.Summary{Failures: 1}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if passport.Measurement.Power.Watts != nil || passport.Measurement.Power.Failures != 1 {
		t.Fatalf("power facts = %#v", passport.Measurement.Power)
	}
}
