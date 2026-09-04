package profiler

import (
	"strings"
	"testing"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"sigs.k8s.io/yaml"
)

const (
	testConfiguredActivePolicy = "configured-active"
	testConfiguredIdlePolicy   = "configured-idle"
)

func TestFreezeConfiguredExecutionPolicySelectsAndBindsCatalogSnapshot(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	raw := executionPolicyCatalogFixture(t)
	source := ExecutionPolicySource{
		Kind:            ConfigMapExecutionPolicySourceKind,
		Namespace:       "chill-system",
		Name:            "chill-profiler-facts",
		UID:             "facts-uid",
		ResourceVersion: "42",
		Key:             "execution-policies.yaml",
	}

	frozen, err := FreezeConfiguredExecutionPolicy(raw, config, source)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.Name != testConfiguredActivePolicy {
		t.Fatalf("selected execution policy = %#v", frozen)
	}
	canonical, err := frozen.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrozenExecutionPolicy(append([]byte(" \n"), canonical...))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Policy.Name != frozen.Policy.Name || decoded.Source != frozen.Source {
		t.Fatalf("decoded execution policy = %#v", decoded)
	}
}

func TestExecutionPolicyRetainsSourceRevisionOutsideSelectedPolicy(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	raw := executionPolicyCatalogFixture(t)
	freeze := func(resourceVersion string) FrozenExecutionPolicy {
		t.Helper()
		frozen, err := FreezeConfiguredExecutionPolicy(raw, config, ExecutionPolicySource{
			Kind:            ConfigMapExecutionPolicySourceKind,
			Namespace:       "chill-system",
			Name:            "facts",
			UID:             "facts-uid",
			ResourceVersion: resourceVersion,
			Key:             "execution-policies.yaml",
		})
		if err != nil {
			t.Fatal(err)
		}
		return frozen
	}
	first := freeze("1")
	second := freeze("2")
	if first.Policy != second.Policy {
		t.Fatalf("source-only revision changed selected policy: %#v != %#v", first.Policy, second.Policy)
	}
	if first.Source.ResourceVersion == second.Source.ResourceVersion {
		t.Fatal("source revision was not retained in the frozen record")
	}
}

func TestFreezeConfiguredExecutionPolicyPrefersExactDeviceClass(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	exact := testExecutionPolicySpec(config.Measurement.WorkloadMode)
	exact.Name = "exact-device"
	exact.DeviceClass = config.State.DeviceClass
	fallback := exact
	fallback.Name = "fallback"
	fallback.DeviceClass = "*"
	raw, err := yaml.Marshal(ExecutionPolicyCatalog{
		SchemaVersion: ExecutionPolicyCatalogSchemaVersion,
		Policies:      []ExecutionPolicySpec{exact, fallback},
	})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := FreezeConfiguredExecutionPolicy(raw, config, validExecutionPolicySource())
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.Name != "exact-device" {
		t.Fatalf("selected execution policy = %q, want exact DeviceClass entry", frozen.Policy.Name)
	}
}

func TestFreezeConfiguredExecutionPolicyRejectsAmbiguousOrUnknownCatalog(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	first := testExecutionPolicySpec(config.Measurement.WorkloadMode)
	first.Name = "first"
	second := first
	second.Name = "second"
	ambiguous, err := yaml.Marshal(ExecutionPolicyCatalog{
		SchemaVersion: ExecutionPolicyCatalogSchemaVersion,
		Policies:      []ExecutionPolicySpec{first, second},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := FreezeConfiguredExecutionPolicy(ambiguous, config, validExecutionPolicySource()); err == nil ||
		!strings.Contains(err.Error(), "duplicate selector") {
		t.Fatalf("ambiguous catalog error = %v", err)
	}

	unknown := append(executionPolicyCatalogFixture(t), []byte("unknownField: true\n")...)
	if _, err := FreezeConfiguredExecutionPolicy(unknown, config, validExecutionPolicySource()); err == nil {
		t.Fatal("strict decoder accepted an unknown catalog field")
	}
}

func TestExecutionPolicyRejectsIncoherentResourceEnvelope(t *testing.T) {
	config := currentExecutionPolicyTestRunConfig(t)
	policy := ExecutionPolicy{
		SchemaVersion:       ExecutionPolicySchemaVersion,
		ExecutionPolicySpec: testExecutionPolicySpec(config.Measurement.WorkloadMode),
	}
	policy.RuntimeMemoryRequestBytes = 1
	policy.RuntimeMemoryLimitBytes = policy.MaxInputBytes + policy.MaxRuntimeResultBytes
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "runtime capacities") {
		t.Fatalf("incoherent envelope error = %v", err)
	}
}

func currentExecutionPolicyTestRunConfig(t *testing.T) profilingrun.RunConfig {
	t.Helper()
	config := testRunConfig(t)
	config.SchemaVersion = profilingrun.RunConfigSchemaVersion
	config.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&config)
	return config
}

func executionPolicyCatalogFixture(t *testing.T) []byte {
	t.Helper()
	activeSpec := testExecutionPolicySpec(profilingrun.SaturatedSerialFixedBatchWorkloadMode)
	activeSpec.Name = testConfiguredActivePolicy
	idleSpec := activeSpec
	idleSpec.Name = testConfiguredIdlePolicy
	idleSpec.WorkloadMode = profilingrun.IdleModelLoadedWorkloadMode
	raw, err := yaml.Marshal(ExecutionPolicyCatalog{
		SchemaVersion: ExecutionPolicyCatalogSchemaVersion,
		Policies:      []ExecutionPolicySpec{activeSpec, idleSpec},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testExecutionPolicySpec(workloadMode string) ExecutionPolicySpec {
	const mebibyte = int64(1024 * 1024)
	return ExecutionPolicySpec{
		Name: "test-policy", DeviceClass: "*", ExecutionPath: firstExecutionPath,
		WorkloadMode:                  workloadMode,
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

func validExecutionPolicySource() ExecutionPolicySource {
	return ExecutionPolicySource{
		Kind:            ConfigMapExecutionPolicySourceKind,
		Namespace:       "chill-system",
		Name:            "facts",
		UID:             "facts-uid",
		ResourceVersion: "1",
		Key:             "execution-policies.yaml",
	}
}
