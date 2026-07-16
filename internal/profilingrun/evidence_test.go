package profilingrun

import (
	"testing"
	"time"
)

func TestValidateRuntimeRejectsIdentityAndOutputDrift(t *testing.T) {
	intent := validIntent()
	digest, _ := intent.Digest()
	valid := RuntimeResult{
		SchemaVersion:             RuntimeResultSchemaVersion,
		Status:                    "Succeeded",
		SweepID:                   digest,
		ExperimentID:              "pod-uid",
		NodeName:                  intent.TargetNode.Name,
		Model:                     intent.State.Model,
		ArtifactDigest:            intent.State.ArtifactDigest,
		Runtime:                   intent.State.RuntimeFamily,
		RuntimeVersion:            "1",
		Provider:                  intent.State.Backend,
		AvailableProviders:        []string{intent.State.Backend},
		BatchSize:                 1,
		WarmupIterations:          20,
		WarmupCompletedAt:         "2026-01-01T00:00:00Z",
		InferenceCount:            1,
		MeasurementMode:           "duration",
		Repetitions:               1,
		MeasurementStartedAt:      "2026-01-01T00:00:01Z",
		MeasurementEndedAt:        "2026-01-01T00:00:31Z",
		MeasurementElapsedSeconds: 30,
		OutputValidation: OutputValidation{
			Method:       intent.Measurement.OutputValidation,
			Passed:       true,
			OutputShapes: [][]int{{1, 1000}},
		},
		CPUContract: RuntimeCPU{
			ORTIntraOpThreads: 4,
			ORTInterOpThreads: 1,
			AffinityCPUCount:  4,
			CPUSetEffective:   "0-3",
		},
		DerivedContract:   DerivedContract{RunIntentDigest: digest, NodeSnapshot: intent.TargetNode},
		RepetitionResults: []Repetition{{LatencyMilliseconds: []float64{1}}},
	}
	if err := ValidateRuntime(intent, "pod-uid", valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.OutputValidation.Passed = false
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected output drift rejection")
	}
	invalid = valid
	invalid.SweepID = "sha256:bad"
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected identity drift rejection")
	}
	invalid = valid
	invalid.MeasurementEndedAt = invalid.MeasurementStartedAt
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected empty measurement window rejection")
	}
	invalid = valid
	invalid.MeasurementEndedAt = "not-a-time"
	if err := ValidateRuntime(intent, "pod-uid", invalid); err == nil {
		t.Fatal("expected invalid measurement end rejection")
	}

	started, ended, err := valid.MeasurementWindow()
	if err != nil {
		t.Fatal(err)
	}
	if !started.Equal(time.Date(2026, time.January, 1, 0, 0, 1, 0, time.UTC)) ||
		!ended.Equal(time.Date(2026, time.January, 1, 0, 0, 31, 0, time.UTC)) {
		t.Fatalf("measurement window = %s..%s", started, ended)
	}
}
