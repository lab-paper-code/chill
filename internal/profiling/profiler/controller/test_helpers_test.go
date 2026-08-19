package controller

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"sigs.k8s.io/yaml"
)

const (
	testARM64Architecture     = "arm64"
	testSelectablePowerMode   = "15W"
	testWorkloadObserverImage = "registry.example/workload-observer@sha256:" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

const controllerPolicyCatalog = `
schemaVersion: chill.dacs.io/profiler-admission-policies.v1alpha1
policies:
  - name: operator-meter-policy
    workloadMode: saturated-serial-fixed-batch
    minimumSamples: 7
    maximumGapSeconds: 2.5
    maximumBoundaryDistanceSeconds: 1.25
    minimumCoverage: 0.97
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
`

func controllerExecutionPolicyCatalog(t *testing.T) []byte {
	t.Helper()
	active := controllerExecutionPolicySpec(profilingrun.SaturatedSerialFixedBatchWorkloadMode)
	active.Name = "configured-active"
	idle := active
	idle.Name = "configured-idle"
	idle.WorkloadMode = "idle-model-loaded"
	rknnActive := active
	rknnActive.Name = "test-rknn-active"
	rknnActive.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	rknnIdle := idle
	rknnIdle.Name = "test-rknn-idle"
	rknnIdle.ExecutionPath = profilingrun.RKNNLiteExecutionPath
	raw, err := yaml.Marshal(profiler.ExecutionPolicyCatalog{
		SchemaVersion: profiler.ExecutionPolicyCatalogSchemaVersion,
		Policies:      []profiler.ExecutionPolicySpec{active, idle, rknnActive, rknnIdle},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func controllerExecutionPolicySpec(workloadMode string) profiler.ExecutionPolicySpec {
	const mebibyte = int64(1024 * 1024)
	return profiler.ExecutionPolicySpec{
		Name: "test-policy", DeviceClass: "*", ExecutionPath: "ort-cpu", WorkloadMode: workloadMode,
		ActiveDeadlineOverheadSeconds: 180, AttemptTimeoutOverheadSeconds: 150,
		ObserverReadyTimeoutSeconds: 30, ObserverStartOverheadSeconds: 120,
		MetricResultOverheadSeconds: 30, WorkloadEndFenceTimeoutSeconds: 15,
		WorkloadResultTimeoutSeconds: 30,
		MaxLatencySamples:            1_000_000, MaxInputBytes: 64 * mebibyte,
		MaxRuntimeResultBytes: 32 * mebibyte, MaxMetricResultBytes: 8 * mebibyte,
		MaxMetricEvents: 50_000, MaxWorkloadEvents: 10_000,
		MaxWorkloadResultBytes:    8 * mebibyte,
		RuntimeMemoryRequestBytes: 128 * mebibyte, RuntimeMemoryLimitBytes: 1024 * mebibyte,
		ObserverCPURequest: "5m", ObserverCPULimit: "100m",
		ObserverMemoryRequestBytes: 16 * mebibyte, ObserverMemoryLimitBytes: 64 * mebibyte,
		WorkloadObserverCPURequest: "5m", WorkloadObserverCPULimit: "100m",
		WorkloadObserverMemoryRequestBytes: 32 * mebibyte,
		WorkloadObserverMemoryLimitBytes:   128 * mebibyte,
		CoordinationReserveBytes:           1 * mebibyte, WorkspaceBytes: 128 * mebibyte,
	}
}

func configuredAdmissionPolicy(
	t *testing.T,
	name string,
	workloadMode string,
) profileradmission.FrozenPolicy {
	return configuredAdmissionPolicyWithSource(t, name, workloadMode, profileradmission.PolicySource{
		Kind: profileradmission.ConfigMapPolicySourceKind, Namespace: testSystemNamespace,
		Name: "chill-profiler-facts", UID: testFactsUID, ResourceVersion: "42",
		Key: "admission-policies.yaml",
	})
}

func configuredAdmissionPolicyWithSource(
	t *testing.T,
	name string,
	workloadMode string,
	source profileradmission.PolicySource,
) profileradmission.FrozenPolicy {
	t.Helper()
	raw, err := yaml.Marshal(profileradmission.PolicyCatalog{
		SchemaVersion: profileradmission.PolicyCatalogSchemaVersion,
		Policies: []profileradmission.PolicySpec{{
			Name: name, WorkloadMode: workloadMode, MinimumSamples: 20,
			MaximumGapSeconds: 1.5, MaximumBoundaryDistanceSeconds: 1,
			MinimumCoverage: 0.95, AllowReceiptTimestamps: true,
			RejectConcurrentCHILLWorkloads: true, RejectExternalWorkloadChanges: true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := profileradmission.FreezeConfiguredPolicy(raw, name, source)
	if err != nil {
		t.Fatal(err)
	}
	return frozen
}
