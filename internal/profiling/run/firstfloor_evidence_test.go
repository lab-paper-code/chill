package profilingrun

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/test/fixtures"
)

func TestActiveTrialRequiresBothElapsedWindowAndNativeSampleCoverage(t *testing.T) {
	config := firstfloorRunConfig()
	config.Measurement.DurationToleranceSeconds = .5
	for _, test := range []struct {
		name    string
		elapsed float64
		count   int
		valid   bool
	}{
		{"sample limited complete", 118, 60, true},
		{"time limited complete", 30, 120, true},
		{"time alone is insufficient", 118, 58, false},
		{"samples alone are insufficient", 29.9, 60, false},
		{"startup does not count as a known gap", 118, 59, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := firstfloorResult(config)
			populateFirstfloorPhases(config, &result, test.count, time.Duration(test.elapsed*float64(time.Second)))
			if err := ValidateRuntime(config, "pod-uid", result); (err == nil) != test.valid {
				t.Fatalf("valid=%t, error=%v", test.valid, err)
			}
		})
	}
	// A complete aggregate never hides an under-sampled individual phase.
	for phase := range 3 {
		result := firstfloorResult(config)
		result.Phases[phase].NativeSamples = result.Phases[phase].NativeSamples[:58]
		result.Phases[phase].LatencyMilliseconds = result.Phases[phase].LatencyMilliseconds[:58]
		result.Phases[phase].Requests = result.Phases[phase].Requests[:58]
		result.Phases[phase].CompletedInputs = 58
		result.InferenceCount -= 2
		result.Firstfloor.ExecutionCount -= 2
		result.Firstfloor.InferenceCount -= 2
		result.Firstfloor.BatchExecutionCounts["1"] -= 2
		if phase == 1 {
			result.Phases[2].ArrivalRatePerSecond = .5 * 58 / 30
		}
		if err := ValidateRuntime(config, "pod-uid", result); err == nil {
			t.Fatalf("phase %d borrowed another phase's known-gap samples", phase)
		}
	}
}

func TestActiveTrialPreservesInitiallyKnownNativeGap(t *testing.T) {
	config := firstfloorRunConfig()
	result := firstfloorResult(config)
	repetition := &result.Phases[0]
	observed := int64(123)
	repetition.NativeSamples[0].PrecedingIdleNanos = &observed
	repetition.NativeSamples = repetition.NativeSamples[:config.Measurement.MinServiceSamples]
	repetition.LatencyMilliseconds = repetition.LatencyMilliseconds[:config.Measurement.MinServiceSamples]
	repetition.Requests = repetition.Requests[:config.Measurement.MinServiceSamples]
	repetition.CompletedInputs = config.Measurement.MinServiceSamples
	repetition.IssueLag.Count = repetition.CompletedInputs
	repetition.AchievedArrivalRatePerSecond = float64(repetition.CompletedInputs) / repetition.IssueElapsedSeconds
	result.InferenceCount--
	result.Firstfloor.ExecutionCount = uint64(result.InferenceCount)
	result.Firstfloor.InferenceCount = uint64(result.InferenceCount)
	result.Firstfloor.BatchExecutionCounts["1"] = uint64(result.InferenceCount)
	if err := ValidateRuntime(config, "pod-uid", result); err != nil {
		t.Fatalf("observed initial native gap must count without donor authority: %v", err)
	}
	if got := repetition.NativeSamples[0].PrecedingIdleNanos; got == nil || *got != observed {
		t.Fatal("observed initial gap was discarded or changed")
	}
}

func firstfloorRunConfig() RunConfig {
	config := validRunConfig()
	config.State.RuntimeFamily = FirstfloorRuntimeFamily
	config.State.Backend = FirstfloorORTCPUBackend
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	config.Actor.MaxQueueRequests = 32
	config.Actor.MaxQueueBytes = int(config.Measurement.OperatingPoints.QueueBytesCap)
	return config
}

func firstfloorResult(config RunConfig) RuntimeResult {
	result := validRuntimeResult(config)
	populateFirstfloorPhases(config, &result, config.Measurement.MinServiceSamples+1, 30*time.Second)
	result.Provider = cpuExecutionProvider
	result.AvailableProviders = []string{cpuExecutionProvider}
	result.DerivedContract.Provider = cpuExecutionProvider
	result.MemoryBytes, result.MemorySource = 123456, memoryPeakCgroupV2
	result.CPUContract.AffinityCPUs = []int{0, 1, 2, 3}
	result.Firstfloor = &FirstfloorRuntimeEvidence{
		Mode: "profiling", ModelInstanceCount: 1, MaxBatchSize: 4, MaxQueueSize: 32,
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(config.State.Firstfloor.DeadlinePolicy),
		ExecutionCount: uint64(result.InferenceCount), InferenceCount: uint64(result.InferenceCount),
		BatchExecutionCounts: map[string]uint64{"1": uint64(result.InferenceCount)},
		Runtime: firstfloor.RuntimeMetadata{Runtime: FirstfloorORTCPUBackend, RuntimeVersion: result.RuntimeVersion,
			ModelFiles: config.State.ModelFiles(), Inputs: config.State.Preprocessor.Inputs,
			Outputs: config.State.Preprocessor.Outputs, Providers: []string{cpuExecutionProvider},
			AvailableProviders: []string{cpuExecutionProvider}, Device: "cpu", CPUAffinity: []int{0, 1, 2, 3},
			IntraOpThreads: 4, InterOpThreads: 1, InitializedBatches: []int{1, 2, 4}, CgroupCPUMax: "400000 100000", CPUSet: "0-3"},
	}
	return result
}

func populateFirstfloorPhases(config RunConfig, result *RuntimeResult, count int, duration time.Duration) {
	points := config.Measurement.OperatingPoints
	workers, sizing := fixtures.PhaseSupply(config.Measurement.Batch, config.Actor.MaxQueueRequests, points.QueueBytesCap,
		points.FramedDecodedRequestUpperBytes, float64(points.RequestLifetimeSeconds), float64(points.DeliveryMarginMilliseconds))
	fixtures.ApplyActivePhases(result, fixtures.PhaseOptions{
		StartedAt: time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC), Duration: duration,
		Batch: config.Measurement.Batch, Samples: count, Workers: workers, Utilizations: points.Utilizations,
		SparseMeanGapMilliseconds: points.SparseMeanGapMilliseconds, PodUID: result.ExperimentID, RecordIDs: []string{"262148000"},
		SupplySizing: sizing,
	})
	if result.Firstfloor != nil {
		result.Firstfloor.ExecutionCount = uint64(result.InferenceCount)
		result.Firstfloor.InferenceCount = uint64(result.InferenceCount * config.Measurement.Batch)
		result.Firstfloor.BatchExecutionCounts = map[string]uint64{fmt.Sprint(config.Measurement.Batch): uint64(result.InferenceCount)}
	}
}

func TestFirstfloorEvidenceBindsSelectedDeadlinePolicy(t *testing.T) {
	for _, policy := range []string{"", "drop", "keep", "defer"} {
		config := firstfloorRunConfig()
		config.State.Firstfloor.DeadlinePolicy = policy
		result := firstfloorResult(config)
		if err := ValidateRuntime(config, "pod-uid", result); err != nil {
			t.Fatalf("policy %q: %v", policy, err)
		}
		result.Firstfloor.DeadlinePolicy = "invalid"
		if err := ValidateRuntime(config, "pod-uid", result); err == nil {
			t.Fatalf("policy %q accepted mismatched actor policy", policy)
		}
	}
}

func TestFirstfloorEvidenceRetainsDistinctCorrelatedSpans(t *testing.T) {
	config := firstfloorRunConfig()
	if err := config.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntime(config, "pod-uid", firstfloorResult(config)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{"other family", func(r *RuntimeResult) { r.Runtime = "onnxruntime" }},
		{"synthetic runner", func(r *RuntimeResult) { r.Firstfloor.Runtime.Synthetic = true }},
		{"missing memory", func(r *RuntimeResult) { r.MemoryBytes = 0 }},
		{"wrong memory scope", func(r *RuntimeResult) { r.MemorySource = "process-rss" }},
		{"different input", func(r *RuntimeResult) { r.Firstfloor.Runtime.Inputs = []string{"other"} }},
		{"different output", func(r *RuntimeResult) { r.Firstfloor.Runtime.Outputs = nil }},
		{"different model files", func(r *RuntimeResult) { r.Firstfloor.Runtime.ModelFiles = []string{"/model/model.onnx"} }},
		{"wrong max", func(r *RuntimeResult) { r.Firstfloor.MaxBatchSize = 2 }},
		{"batch not initialized", func(r *RuntimeResult) { r.Firstfloor.Runtime.InitializedBatches = []int{2, 4} }},
		{"wrong provider", func(r *RuntimeResult) { r.Firstfloor.Runtime.Providers[0] = "CUDAExecutionProvider" }},
		{"wrong actual affinity", func(r *RuntimeResult) { r.Firstfloor.Runtime.CPUAffinity = []int{1, 2, 3, 4} }},
		{"uncorrelated samples", func(r *RuntimeResult) { r.Phases[0].NativeSamples = nil }},
		{"wrong measured items", func(r *RuntimeResult) { r.Firstfloor.InferenceCount = 2 }},
		{"wrong batch", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].BatchSize = 2 }},
		{"invalid backend", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].BackendCallMilliseconds = math.NaN() }},
		{"zero diagnostic service", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].RunnerServiceMilliseconds = 0 }},
		{"nonfinite diagnostic service", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].RunnerServiceMilliseconds = math.NaN() }},
		{"canonical service below backend", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].ServiceDurationNanos = 300000 }},
		{"canonical service above native request", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].ServiceDurationNanos = 1000000 }},
		{"record IDs short of batch", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].RecordIDs = nil }},
		{"units short of batch", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].Units = nil }},
		{"zero units", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].Units = []int{0} }},
		{"negative preprocess", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].PreprocessMilliseconds = -1 }},
		{"infinite postprocess", func(r *RuntimeResult) {
			r.Phases[0].NativeSamples[0].PostprocessMilliseconds = math.Inf(1)
		}},
		{"first token without generation", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].FirstTokenMilliseconds = 0.3 }},
		{"missing canonical duration", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].ServiceDurationNanos = 0 }},
		{"donor is not physical trial", func(r *RuntimeResult) { r.Phases[0].NativeSamples[0].TrialID = "donor-trial" }},
		{"unknown gap after startup", func(r *RuntimeResult) { r.Phases[0].NativeSamples[1].PrecedingIdleNanos = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := firstfloorResult(config)
			test.mutate(&result)
			if err := ValidateRuntime(config, "pod-uid", result); err == nil {
				t.Fatal("contradictory native evidence accepted")
			}
		})
	}
}

func TestFirstfloorGenerativeNativeSamplesRequireFirstToken(t *testing.T) {
	config := firstfloorRunConfig()
	config.State.Preprocessor.MaxNewTokens = 16
	result := firstfloorResult(config)
	if err := ValidateRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("generative native sample without a first-token span was accepted")
	}
	for phase := range result.Phases {
		for index := range result.Phases[phase].NativeSamples {
			result.Phases[phase].NativeSamples[index].FirstTokenMilliseconds = 0.3
		}
	}
	if err := ValidateRuntime(config, "pod-uid", result); err != nil {
		t.Fatal(err)
	}
}

func TestFirstfloorSampledMemoryIsBoundToWindowAndRetainedMaximum(t *testing.T) {
	config := firstfloorRunConfig()
	sampled := func() RuntimeResult {
		result := firstfloorResult(config)
		result.MemorySource = memorySampledCgroupV2
		result.MemorySamples = []MemorySample{{"2026-01-01T00:00:01.5Z", 100}, {"2026-01-01T00:00:15Z", 300}, {"2026-01-01T00:00:30Z", 200}}
		result.MemoryBytes = 300
		return result
	}
	if err := ValidateRuntime(config, "pod-uid", sampled()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*RuntimeResult)
	}{
		{"maximum not retained", func(r *RuntimeResult) { r.MemoryBytes = 400 }},
		{"sample outside window", func(r *RuntimeResult) { r.MemorySamples[2].At = "2026-01-01T00:01:32Z" }},
		{"unordered samples", func(r *RuntimeResult) { r.MemorySamples[1].At = r.MemorySamples[0].At }},
		{"single sample", func(r *RuntimeResult) { r.MemorySamples = r.MemorySamples[:1] }},
		{"peak source with samples", func(r *RuntimeResult) { r.MemorySource = memoryPeakCgroupV2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := sampled()
			test.mutate(&result)
			if err := ValidateRuntime(config, "pod-uid", result); err == nil {
				t.Fatal("untruthful sampled memory evidence accepted")
			}
		})
	}
}

func TestFirstfloorNativeSamplesRejectReusedBatchIdentity(t *testing.T) {
	config := firstfloorRunConfig()
	repetition := firstfloorResult(config).Phases[0]
	repetition.LatencyMilliseconds = []float64{1, 1}
	repetition.NativeSamples = append(repetition.NativeSamples, repetition.NativeSamples[0])
	if err := validateNativeSamples(config, "pod-uid", []Phase{repetition}); err == nil {
		t.Fatal("reused batch ID accepted")
	}
}

func TestFirstfloorIdleEvidenceRejectsInferenceCounts(t *testing.T) {
	config := firstfloorRunConfig()
	result := firstfloorResult(config)
	result.Firstfloor.ExecutionCount, result.Firstfloor.InferenceCount = 0, 0
	result.Firstfloor.BatchExecutionCounts = map[string]uint64{}
	if err := validateFirstfloorRuntimeEvidence(config, result.Firstfloor, 0); err != nil {
		t.Fatal(err)
	}
	result.Firstfloor.BatchExecutionCounts["1"] = 1
	if err := validateFirstfloorRuntimeEvidence(config, result.Firstfloor, 0); err == nil {
		t.Fatal("idle inference accepted")
	}
}

func TestFirstfloorCUDARejectsCPUFallbackAndWrongWholeGPU(t *testing.T) {
	config := firstfloorRunConfig()
	config.State.Backend = FirstfloorORTCUDABackend
	config.Delivery.RuntimeAccess = &CandidateReportRuntimeAccess{GPUUUID: "GPU-a", Resources: map[string]string{"nvidia.com/gpu": "1"}}
	result := firstfloorResult(config)
	result.Firstfloor.Runtime.Runtime = FirstfloorORTCUDABackend
	if err := validateFirstfloorPath(config, result.Firstfloor, nil, result.Provider, result.AvailableProviders, result.CPUContract); err == nil {
		t.Fatal("CUDA CPU fallback accepted")
	}
	result.Provider = "CUDAExecutionProvider"
	result.AvailableProviders = []string{result.Provider}
	result.Firstfloor.Runtime.Providers = []string{result.Provider}
	result.Firstfloor.Runtime.AvailableProviders = []string{result.Provider}
	result.Firstfloor.Runtime.Device, result.Firstfloor.Runtime.GPUUUID = "cuda", "GPU-b"
	if err := validateFirstfloorPath(config, result.Firstfloor, nil, result.Provider, result.AvailableProviders, result.CPUContract); err == nil {
		t.Fatal("different whole GPU accepted")
	}
	result.Firstfloor.Runtime.GPUUUID = "GPU-a"
	if err := validateFirstfloorPath(config, result.Firstfloor, nil, result.Provider, result.AvailableProviders, result.CPUContract); err != nil {
		t.Fatal(err)
	}
}

func TestFirstfloorEvidenceRecordsSharedCPUEnvironment(t *testing.T) {
	config := firstfloorRunConfig()
	for _, quota := range []string{"max 100000", "-1 100000", "150000 100000"} {
		result := firstfloorResult(config)
		result.CPUContract.CgroupCPUMax = quota
		result.Firstfloor.Runtime.CgroupCPUMax = quota
		result.CPUContract.ORTIntraOpThreads, result.CPUContract.ORTInterOpThreads = 0, 0
		result.Firstfloor.Runtime.IntraOpThreads, result.Firstfloor.Runtime.InterOpThreads = 0, 0
		result.CPUContract.AffinityCPUs = []int{0, 1, 2, 4}
		result.Firstfloor.Runtime.CPUAffinity = []int{0, 1, 2, 4}
		if err := ValidateRuntime(config, "pod-uid", result); err != nil {
			t.Fatalf("shared observation %q was rejected: %v", quota, err)
		}
		idleNative := *result.Firstfloor
		idleNative.ExecutionCount, idleNative.InferenceCount = 0, 0
		idleNative.BatchExecutionCounts = map[string]uint64{}
		idle := IdleRuntimeResult{CPUContract: result.CPUContract, Firstfloor: &idleNative,
			MemoryBytes: result.MemoryBytes, MemorySource: result.MemorySource, Provider: result.Provider,
			AvailableProviders: result.AvailableProviders, DerivedContract: result.DerivedContract, RuntimeVersion: result.RuntimeVersion}
		if err := validateIdleRuntimePathEvidence(config, idle); err != nil {
			t.Fatal(err)
		}
		if err := validateCPUObservation(quota); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompactBatchMembershipUsesSampleRequestIDsOnce(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	makePhase := func() Phase {
		return fixtures.ActivePhases[Phase](fixtures.PhaseOptions{
			StartedAt: start, Duration: 30 * time.Second, Batch: 2, Samples: 2, Workers: 4,
			PodUID: "physical-pod", SparseMeanGapMilliseconds: 1000,
		})[0]
	}
	phase := makePhase()
	if err := validatePhaseRequests(phase, 2, start, start.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Phase)
	}{
		{"missing IDs", func(p *Phase) { p.NativeSamples[0].RequestIDs = nil }},
		{"duplicate IDs", func(p *Phase) { p.NativeSamples[0].RequestIDs[1] = p.NativeSamples[0].RequestIDs[0] }},
		{"foreign batch", func(p *Phase) { p.NativeSamples[0].RequestIDs[1] = p.NativeSamples[1].RequestIDs[0] }},
		{"record order", func(p *Phase) { p.NativeSamples[0].RecordIDs[0] = "wrong" }},
		{"unit order", func(p *Phase) { p.NativeSamples[0].Units[0] = 2 }},
		{"uncorrelated latency", func(p *Phase) { p.LatencyMilliseconds[0] = 2 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			phase := makePhase()
			test.mutate(&phase)
			if err := validatePhaseRequests(phase, 2, start, start.Add(30*time.Second)); err == nil {
				t.Fatal("incomplete compact membership was accepted")
			}
		})
	}
	cloned := phase.NativeSamples[0].Clone()
	cloned.RequestIDs[0] = "changed"
	if phase.NativeSamples[0].RequestIDs[0] == "changed" {
		t.Fatal("sample clone shares request membership")
	}
}

func TestPoissonIssueEvidenceRejectsIncompleteLateOrNonfiniteSchedules(t *testing.T) {
	config := firstfloorRunConfig()
	tests := []struct {
		name   string
		mutate func(*Phase)
	}{
		{"missing lag", func(p *Phase) { p.IssueLag = nil }},
		{"count mismatch", func(p *Phase) { p.IssueLag.Count-- }},
		{"late send", func(p *Phase) { p.IssueLag.Max = 10.001 }},
		{"nonfinite mean", func(p *Phase) { p.IssueLag.Mean = math.NaN() }},
		{"infinite tail", func(p *Phase) { p.IssueLag.P99 = math.Inf(1) }},
		{"negative minimum", func(p *Phase) { p.IssueLag.Min = -1 }},
		{"unordered percentiles", func(p *Phase) { p.IssueLag.P95, p.IssueLag.P99, p.IssueLag.Max = 2, 1, 3 }},
		{"wrong tolerance", func(p *Phase) { p.IssueLagToleranceMS = 11 }},
		{"missing elapsed", func(p *Phase) { p.IssueElapsedSeconds = 0 }},
		{"elapsed beyond phase", func(p *Phase) { p.IssueElapsedSeconds = p.MeasurementElapsedSeconds + 1 }},
		{"wrong denominator", func(p *Phase) { p.AchievedArrivalRatePerSecond++ }},
		{"nonfinite rate", func(p *Phase) { p.AchievedArrivalRatePerSecond = math.Inf(1) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := firstfloorResult(config)
			test.mutate(&result.Phases[0])
			if err := ValidateRuntime(config, "pod-uid", result); err == nil {
				t.Fatal("invalid Poisson issue evidence was accepted")
			}
		})
	}
	result := firstfloorResult(config)
	result.Phases[0].IssueLag.Max = 10
	if err := ValidateRuntime(config, "pod-uid", result); err != nil {
		t.Fatalf("inclusive 10ms tolerance rejected: %v", err)
	}
	result.Phases[1].IssueLag = &IssueLag{}
	if err := ValidateRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("saturation accepted Poisson issue evidence")
	}
	result = firstfloorResult(config)
	result.SchemaVersion = "chill.dacs.io/batch-characterization.v1alpha11"
	if err := ValidateRuntime(config, "pod-uid", result); err == nil {
		t.Fatal("retired runtime schema was accepted")
	}
}
