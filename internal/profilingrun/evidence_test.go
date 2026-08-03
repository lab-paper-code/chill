package profilingrun

import (
	"testing"
	"time"
)

func TestValidateRuntimeRejectsIdentityAndOutputDrift(t *testing.T) {
	intent := validIntent()
	valid := validRuntimeResult(intent)
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

func TestValidateRuntimeRejectsInvalidRepetitionEvidence(t *testing.T) {
	intent := validIntent()
	base := validRuntimeResult(intent)

	for _, test := range []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{name: "wrong sequence", mutate: func(result *RuntimeResult) {
			result.RepetitionResults[0].Repetition = 2
		}},
		{name: "window outside measurement", mutate: func(result *RuntimeResult) {
			result.RepetitionResults[0].MeasurementStartedAt = "2025-12-31T23:59:59Z"
		}},
		{name: "zero latency", mutate: func(result *RuntimeResult) {
			result.RepetitionResults[0].LatencyMilliseconds[0] = 0
		}},
		{name: "target duration drift", mutate: func(result *RuntimeResult) {
			other := 10.0
			result.TargetDurationSeconds = &other
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.RepetitionResults = append([]Repetition(nil), base.RepetitionResults...)
			candidate.RepetitionResults[0].LatencyMilliseconds = append(
				[]float64(nil), base.RepetitionResults[0].LatencyMilliseconds...,
			)
			test.mutate(&candidate)
			if err := ValidateRuntime(intent, "pod-uid", candidate); err == nil {
				t.Fatal("expected invalid repetition evidence rejection")
			}
		})
	}
}

func validRuntimeResult(intent Intent) RuntimeResult {
	digest, _ := intent.Digest()
	targetDuration := float64(intent.Measurement.DurationSeconds)
	return RuntimeResult{
		SchemaVersion: RuntimeResultSchemaVersion, Status: "Succeeded", SweepID: digest,
		ExperimentID: "pod-uid", NodeName: intent.TargetNode.Name, Model: intent.State.Model,
		ArtifactDigest: intent.State.ArtifactDigest, Runtime: intent.State.RuntimeFamily,
		Architecture: "x86_64", RuntimeVersion: "1", Provider: intent.State.Backend,
		AvailableProviders: []string{intent.State.Backend},
		BatchSize:          1, WarmupIterations: 20, WarmupCompletedAt: "2026-01-01T00:00:00Z",
		InferenceCount: 1, MeasurementMode: "duration", Repetitions: 1,
		MeasurementStartedAt: "2026-01-01T00:00:01Z", MeasurementEndedAt: "2026-01-01T00:00:31Z",
		MeasurementElapsedSeconds: 30, TargetDurationSeconds: &targetDuration,
		OutputValidation: OutputValidation{
			Method: intent.Measurement.OutputValidation, Passed: true, OutputShapes: [][]int{{1, 1000}},
		},
		CPUContract: RuntimeCPU{
			CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4, ORTInterOpThreads: 1,
			AffinityCPUCount: 4, CPUSetEffective: "0-3",
		},
		DerivedContract: DerivedContract{RunIntentDigest: digest, NodeSnapshot: intent.TargetNode},
		RepetitionResults: []Repetition{{
			Repetition: 1, MeasurementStartedAt: "2026-01-01T00:00:01Z",
			MeasurementEndedAt: "2026-01-01T00:00:31Z", LatencyMilliseconds: []float64{1},
		}},
	}
}

func TestValidateRuntimeRejectsCPUExecutionDrift(t *testing.T) {
	intent := validIntent()
	base := validRuntimeResult(intent)
	for _, test := range []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{name: "architecture", mutate: func(result *RuntimeResult) { result.Architecture = "aarch64" }},
		{name: "smaller quota", mutate: func(result *RuntimeResult) {
			result.CPUContract.CgroupCPUMax = "200000 100000"
		}},
		{name: "unlimited quota", mutate: func(result *RuntimeResult) {
			result.CPUContract.CgroupCPUMax = "max 100000"
		}},
		{name: "malformed quota", mutate: func(result *RuntimeResult) {
			result.CPUContract.CgroupCPUMax = "400000"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			test.mutate(&candidate)
			if err := ValidateRuntime(intent, "pod-uid", candidate); err == nil {
				t.Fatal("expected CPU execution drift rejection")
			}
		})
	}
}

func TestNormalizeRuntimeArchitecture(t *testing.T) {
	for runtime, want := range map[string]string{"x86_64": "amd64", "aarch64": "arm64"} {
		if got := normalizeArchitecture(runtime); got != want {
			t.Fatalf("normalizeArchitecture(%q) = %q, want %q", runtime, got, want)
		}
	}
}

func TestValidateIdleRuntimeRequiresSettlingZeroInferenceAndResidency(t *testing.T) {
	intent := validIntent()
	intent.Measurement = MeasurementContract{
		Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, Repetitions: 1,
		WorkloadMode: IdleModelLoadedWorkloadMode, InputGenerator: NoInputGenerator,
		OutputValidation: NoOutputValidation,
	}
	digest, err := intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result := IdleRuntimeResult{
		SchemaVersion: IdleRuntimeResultSchemaVersion, Status: "Succeeded", IntentDigest: digest,
		PodUID: "pod-uid", NodeName: intent.TargetNode.Name, Model: intent.State.Model,
		ArtifactDigest: intent.State.ArtifactDigest, Architecture: "x86_64",
		Runtime: intent.State.RuntimeFamily, RuntimeVersion: "1", Provider: intent.State.Backend,
		AvailableProviders:   []string{intent.State.Backend},
		ModelLoadCompletedAt: "2026-01-01T00:00:00Z", SettlingStartedAt: "2026-01-01T00:00:01Z",
		SettlingCompletedAt: "2026-01-01T00:01:01Z", MeasurementStartedAt: "2026-01-01T00:01:01Z",
		MeasurementEndedAt: "2026-01-01T00:01:31Z", MeasurementElapsedSeconds: 30,
		ModelResidentAtEnd: true,
		CPUContract: RuntimeCPU{CgroupCPUMax: "400000 100000", ORTIntraOpThreads: 4,
			ORTInterOpThreads: 1, AffinityCPUCount: 4, CPUSetEffective: "0-3"},
		DerivedContract: DerivedContract{RunIntentDigest: digest, NodeSnapshot: intent.TargetNode},
	}
	if err := ValidateIdleRuntime(intent, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*IdleRuntimeResult){
		func(r *IdleRuntimeResult) { r.SettlingCompletedAt = "2026-01-01T00:00:30Z" },
		func(r *IdleRuntimeResult) { r.InferenceCountAfterModelLoad = 1 },
		func(r *IdleRuntimeResult) { r.ModelResidentAtEnd = false },
		func(r *IdleRuntimeResult) { r.MeasurementStartedAt = "2026-01-01T00:00:30Z" },
	} {
		candidate := result
		mutate(&candidate)
		if err := ValidateIdleRuntime(intent, "pod-uid", candidate); err == nil {
			t.Fatal("invalid idle runtime evidence was accepted")
		}
	}
}
