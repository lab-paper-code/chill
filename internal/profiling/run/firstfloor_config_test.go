package profilingrun

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

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
		config.Measurement.Batch = 2
		config.CPU.ORTIntraOpThreads = 0
		config.CPU.ORTInterOpThreads = 0
	}
	return config
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
		{"wrong threads", FirstfloorORTCUDABackend, func(c *RunConfig) { c.CPU.ORTIntraOpThreads = 1 }},
		{"missing CUDA access", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess = nil }},
		{"missing CUDA UUID", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.GPUUUID = "" }},
		{"CUDA index is not UUID", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.GPUUUID = "0" }},
		{"CUDA two GPUs", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.Resources["nvidia.com/gpu"] = "2" }},
		{"CUDA fractional GPU", FirstfloorORTCUDABackend, func(c *RunConfig) { c.Delivery.RuntimeAccess.Resources["nvidia.com/gpu"] = "500m" }},
		{"RKNN missing package", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Delivery.RuntimePackages = nil }},
		{"RKNN missing access", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Delivery.RuntimeAccess = nil }},
		{"RKNN undeclared batch", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Measurement.Batch = 1 }},
		{"RKNN unsupported shape", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.Delivery.ArtifactCapability.Tensors[0].SampleShape = []int32{224, 224} }},
		{"RKNN ORT threads", FirstfloorRKNNLiteBackend, func(c *RunConfig) { c.CPU.ORTInterOpThreads = 1 }},
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

func TestFirstfloorSettingsWireRejectsRetiredPriorityLevels(t *testing.T) {
	payload, err := json.Marshal(firstfloorFrozenConfig(FirstfloorORTCPUBackend))
	if err != nil {
		t.Fatal(err)
	}
	payload = []byte(strings.Replace(string(payload), `"maxQueueSize":32`, `"maxQueueSize":32,"priorityLevels":1`, 1))
	var config RunConfig
	if err := json.Unmarshal(payload, &config); err == nil {
		t.Fatal("retired priorityLevels was accepted")
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

func TestFirstfloorFrozenAffinityValidationIdentityAndCopies(t *testing.T) {
	config := firstfloorFrozenConfig(FirstfloorORTCPUBackend)
	config.CPU.AffinityCPUs = []int{8, 10, 12, 31}
	canonical, err := config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	verified, err := ValidateRunConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	config.CPU.AffinityCPUs[0] = 9
	record, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(record.CPU.AffinityCPUs, []int{8, 10, 12, 31}) {
		t.Fatal("verified RunConfig aliases the supplied mask")
	}
	record.CPU.AffinityCPUs[0] = 9
	frozen, err := verified.Record()
	if err != nil {
		t.Fatal(err)
	}
	if frozen.CPU.AffinityCPUs[0] != 8 {
		t.Fatal("verified RunConfig exposes its mask")
	}
	changedJSON, err := config.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(canonical, changedJSON) {
		t.Fatal("CPU mask replacement retained frozen identity")
	}
	var reconstructed RunConfig
	if err := json.Unmarshal(canonical, &reconstructed); err != nil {
		t.Fatal(err)
	}
	if err := reconstructed.Validate(); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reconstructed.CPU.AffinityCPUs, frozen.CPU.AffinityCPUs) {
		t.Fatal("RunConfig reconstruction lost the explicit mask")
	}
	for _, mask := range [][]int{{-1, 10, 12, 31}, {8, 8, 12, 31}, {31, 12, 10, 8}, {8}} {
		reconstructed.CPU.AffinityCPUs = mask
		if err := reconstructed.Validate(); err == nil {
			t.Fatalf("invalid frozen mask accepted: %v", mask)
		}
	}
	reference := validRunConfig()
	reference.CPU.AffinityCPUs = []int{8, 10, 12, 31}
	if err := reference.Validate(); err == nil {
		t.Fatal("explicit reference producer affinity accepted")
	}
}
