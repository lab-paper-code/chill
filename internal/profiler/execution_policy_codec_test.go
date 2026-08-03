package profiler

import (
	"bytes"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"sigs.k8s.io/yaml"
)

func TestFreezeConfiguredExecutionPolicySelectsAndBindsCatalogSnapshot(t *testing.T) {
	intent := currentExecutionPolicyTestIntent(t)
	raw := executionPolicyCatalogFixture(t, intent)
	source := ExecutionPolicySource{
		Kind:            ConfigMapExecutionPolicySourceKind,
		Namespace:       "chill-system",
		Name:            "chill-profiler-facts",
		UID:             "facts-uid",
		ResourceVersion: "42",
		Key:             "execution-policies.yaml",
	}

	frozen, err := FreezeConfiguredExecutionPolicy(raw, intent, source)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.Name != "configured-active" || frozen.Source.CatalogContentDigest == "" {
		t.Fatalf("selected execution policy = %#v", frozen)
	}
	identity, err := frozen.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(identity, "configured-active/sha256:") {
		t.Fatalf("execution identity = %q", identity)
	}
	canonical, err := frozen.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeFrozenExecutionPolicy(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(canonical, mustFrozenExecutionPolicyJSON(t, decoded)) {
		t.Fatal("frozen execution policy did not round-trip canonically")
	}
}

func TestExecutionPolicyIdentityDependsOnSemanticsNotConfigMapRevision(t *testing.T) {
	intent := currentExecutionPolicyTestIntent(t)
	raw := executionPolicyCatalogFixture(t, intent)
	freeze := func(resourceVersion string) FrozenExecutionPolicy {
		t.Helper()
		frozen, err := FreezeConfiguredExecutionPolicy(raw, intent, ExecutionPolicySource{
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
	firstIdentity, _ := first.Identity()
	secondIdentity, _ := second.Identity()
	if firstIdentity != secondIdentity {
		t.Fatalf("source-only revision changed semantic identity: %q != %q", firstIdentity, secondIdentity)
	}
	if bytes.Equal(mustFrozenExecutionPolicyJSON(t, first), mustFrozenExecutionPolicyJSON(t, second)) {
		t.Fatal("source revision was not retained in the frozen record")
	}
}

func TestFreezeConfiguredExecutionPolicyPrefersExactDeviceClass(t *testing.T) {
	intent := currentExecutionPolicyTestIntent(t)
	legacy, err := LegacyFrozenExecutionPolicy(intent)
	if err != nil {
		t.Fatal(err)
	}
	exact := legacy.Policy.ExecutionPolicySpec
	exact.Name = "exact-device"
	exact.DeviceClass = intent.State.DeviceClass
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
	frozen, err := FreezeConfiguredExecutionPolicy(raw, intent, validExecutionPolicySource())
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Policy.Name != "exact-device" {
		t.Fatalf("selected execution policy = %q, want exact DeviceClass entry", frozen.Policy.Name)
	}
}

func TestFreezeConfiguredExecutionPolicyRejectsAmbiguousOrUnknownCatalog(t *testing.T) {
	intent := currentExecutionPolicyTestIntent(t)
	legacy, err := LegacyFrozenExecutionPolicy(intent)
	if err != nil {
		t.Fatal(err)
	}
	first := legacy.Policy.ExecutionPolicySpec
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
	if _, err := FreezeConfiguredExecutionPolicy(ambiguous, intent, validExecutionPolicySource()); err == nil ||
		!strings.Contains(err.Error(), "duplicate selector") {
		t.Fatalf("ambiguous catalog error = %v", err)
	}

	unknown := append(executionPolicyCatalogFixture(t, intent), []byte("unknownField: true\n")...)
	if _, err := FreezeConfiguredExecutionPolicy(unknown, intent, validExecutionPolicySource()); err == nil {
		t.Fatal("strict decoder accepted an unknown catalog field")
	}
}

func TestExecutionPolicyRejectsIncoherentResourceEnvelope(t *testing.T) {
	intent := currentExecutionPolicyTestIntent(t)
	legacy, err := LegacyFrozenExecutionPolicy(intent)
	if err != nil {
		t.Fatal(err)
	}
	policy := legacy.Policy
	policy.RuntimeMemoryRequestBytes = 1
	policy.RuntimeMemoryLimitBytes = policy.MaxInputBytes + policy.MaxRuntimeResultBytes
	if err := policy.Validate(); err == nil || !strings.Contains(err.Error(), "runtime capacities") {
		t.Fatalf("incoherent envelope error = %v", err)
	}
}

func currentExecutionPolicyTestIntent(t *testing.T) profilingrun.Intent {
	t.Helper()
	intent := testIntent(t)
	intent.SchemaVersion = profilingrun.SchemaVersion
	intent.State.ArtifactPath = testArtifactPath
	addWorkloadObservation(&intent)
	return intent
}

func executionPolicyCatalogFixture(t *testing.T, intent profilingrun.Intent) []byte {
	t.Helper()
	active, err := LegacyFrozenExecutionPolicy(intent)
	if err != nil {
		t.Fatal(err)
	}
	activeSpec := active.Policy.ExecutionPolicySpec
	activeSpec.Name = "configured-active"
	idleSpec := activeSpec
	idleSpec.Name = "configured-idle"
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

func mustFrozenExecutionPolicyJSON(t *testing.T, frozen FrozenExecutionPolicy) []byte {
	t.Helper()
	payload, err := frozen.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
