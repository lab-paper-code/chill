package profilingrun

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

const mutatedInputName = "changed"

func firstfloorFrozenConfig(backend string) RunConfig {
	config := validRunConfig()
	config.State.RuntimeFamily = FirstfloorRuntimeFamily
	config.State.Backend = backend
	config.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32}
	if backend == FirstfloorORTCUDABackend {
		config.Delivery.RuntimeAccess = &CandidateReportRuntimeAccess{
			GPUUUID: "GPU-actual-device", Resources: map[string]string{"nvidia.com/gpu": "1"},
		}
	}
	if backend == FirstfloorRKNNLiteBackend {
		host := rknnHostContractForTest()
		config.State.HostContract = &host
		config.Delivery = rknnDeliveryForTest(2, &host)
		config.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050-rk3588-bs2.rknn"}
		config.Measurement.Batch = 2
	}
	config.Actor = config.ActorConfig()
	config.Actor.MemoryRequestBytes = 128 << 20
	config.Actor.MemoryLimitBytes = 1024 << 20
	return config
}

// Warmup covers every initialized batch, whatever subset the measurement
// protocol names.
func TestActorRecipeWarmsEveryInitializedBatch(t *testing.T) {
	config := firstfloorFrozenConfig(FirstfloorORTCPUBackend)
	config.Measurement.WarmupBatchSizes = []int{1, 4}
	actor := config.ActorConfig()
	if !slices.Equal(actor.BatchSizes, []int{1, 2, 3, 4}) || !slices.Equal(actor.WarmupBatchSizes, actor.BatchSizes) {
		t.Fatalf("initialized batches %v, warmup %v", actor.BatchSizes, actor.WarmupBatchSizes)
	}
	if command := strings.Join(actor.RunnerCommand, " "); !strings.Contains(command, "--warmup-batch 1 --warmup-batch 2 --warmup-batch 3 --warmup-batch 4") {
		t.Fatalf("runner does not warm every initialized batch: %s", command)
	}
	config.State.Preprocessor.Inputs[0] = mutatedInputName
	if actor.Inputs[0] != "image" {
		t.Fatal("frozen actor recipe aliases mutable preparation inputs")
	}
}

func TestFrozenDeadlinePolicyDefaultIdentityAndCopies(t *testing.T) {
	config := firstfloorFrozenConfig(FirstfloorORTCPUBackend)
	omitted, err := config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	config.State.Firstfloor.DeadlinePolicy = "keep"
	explicit, err := config.CanonicalJSON()
	if err != nil || !bytes.Equal(omitted, explicit) {
		t.Fatalf("omitted policy differs from keep: %v", err)
	}
	verified, err := ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, policy := range []string{"drop", "defer"} {
		config.State.Firstfloor.DeadlinePolicy = policy
		changed, err := config.CanonicalJSON()
		if err != nil || bytes.Equal(explicit, changed) {
			t.Fatalf("policy %q retained keep identity: %v", policy, err)
		}
		var reconstructed RunConfig
		if err := json.Unmarshal(changed, &reconstructed); err != nil {
			t.Fatal(err)
		}
		if reconstructed.State.Firstfloor.DeadlinePolicy != policy {
			t.Fatal("wire reconstruction lost deadline policy")
		}
	}
	record, err := verified.Record()
	if err != nil || record.State.Firstfloor.DeadlinePolicy != "keep" {
		t.Fatalf("frozen policy aliases source: %v", err)
	}
	config.State.Firstfloor.DeadlinePolicy = "invalid"
	if err := config.Validate(); err == nil {
		t.Fatal("invalid deadlinePolicy accepted")
	}
}

func TestFirstfloorFrozenBackendRequirements(t *testing.T) {
	for _, backend := range []string{FirstfloorORTCPUBackend, FirstfloorORTCUDABackend, FirstfloorRKNNLiteBackend} {
		t.Run(backend, func(t *testing.T) {
			config := firstfloorFrozenConfig(backend)
			if err := config.Validate(); err != nil {
				t.Fatal(err)
			}
			if !IsSupportedFirstfloorRunConfig(config) {
				t.Fatal("native execution was not supported")
			}
		})
	}
	for _, test := range []struct {
		name    string
		backend string
		mutate  func(*RunConfig)
	}{
		{"missing settings", FirstfloorORTCPUBackend, func(c *RunConfig) { c.State.Firstfloor = nil }},
		{"zero queue", FirstfloorORTCPUBackend, func(c *RunConfig) { c.State.Firstfloor.MaxQueueSize = 0 }},
		{"oversized batch", FirstfloorORTCPUBackend, func(c *RunConfig) { c.Measurement.Batch = 5 }},
		{"unknown backend", FirstfloorORTCPUBackend, func(c *RunConfig) { c.State.Backend = "python" }},
		{"missing CUDA access", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess = nil }},
		{"missing CUDA UUID", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.GPUUUID = "" }},
		{"CUDA index is not UUID", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.GPUUUID = "0" }},
		{"CUDA two GPUs", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.Resources["nvidia.com/gpu"] = "2" }},
		{"CUDA fractional GPU", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.Resources["nvidia.com/gpu"] = "500m" }},
		{"RKNN missing package", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Delivery.RuntimePackages = nil }},
		{"RKNN missing access", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Delivery.RuntimeAccess = nil }},
		{"RKNN undeclared batch", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Measurement.Batch = 1 }},
		{"RKNN unsupported shape", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Delivery.ArtifactCapability.Tensors[0].SampleShape = []int32{224, 224} }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := firstfloorFrozenConfig(test.backend)
			test.mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatal("contradictory backend requirements were accepted")
			}
		})
	}
}

func TestFrozenGPUAccessIsDetached(t *testing.T) {
	config := firstfloorFrozenConfig(FirstfloorORTCUDABackend)
	copy := cloneRunConfig(config)
	config.Delivery.RuntimeAccess.Resources["nvidia.com/gpu"] = "2"
	if copy.Delivery.RuntimeAccess.Resources["nvidia.com/gpu"] != "1" {
		t.Fatal("frozen GPU allocation aliases caller resources")
	}
}
