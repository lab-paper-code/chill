package admission

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const policyCatalogFixture = `
schemaVersion: chill.dacs.io/profiler-admission-policies.v1alpha1
policies:
  - name: wall-fast-meter
    workloadMode: saturated-serial-fixed-batch
    minimumSamples: 8
    maximumGapSeconds: 0.75
    maximumBoundaryDistanceSeconds: 0.5
    minimumCoverage: 0.98
    allowReceiptTimestamps: false
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
  - name: idle-slow-meter
    workloadMode: idle-model-loaded
    minimumSamples: 3
    maximumGapSeconds: 12
    maximumBoundaryDistanceSeconds: 6
    minimumCoverage: 0.9
    allowReceiptTimestamps: true
    rejectConcurrentCHILLWorkloads: true
    rejectExternalWorkloadChanges: true
`

func TestFreezeConfiguredPolicySelectsAndCanonicalizesExactPolicy(t *testing.T) {
	source := PolicySource{
		Kind: ConfigMapPolicySourceKind, Namespace: "chill-system", Name: "chill-profiler-facts",
		UID: "facts-uid", ResourceVersion: "42", Key: "admission-policies.yaml",
	}
	frozen, err := FreezeConfiguredPolicy([]byte(policyCatalogFixture), "idle-slow-meter", source)
	if err != nil {
		t.Fatal(err)
	}
	frozenSource, frozenRecord, err := frozen.Records()
	if err != nil {
		t.Fatal(err)
	}
	if frozenRecord.Name != "idle-slow-meter" || frozenRecord.MinimumSamples != 3 ||
		frozenRecord.MaximumGapSeconds != 12 || !strings.HasPrefix(frozenSource.EntryContentDigest, "sha256:") {
		t.Fatalf("frozen policy = %#v", frozen)
	}
	frozenRecord.Name = "mutated-copy"
	frozenSource.ResourceVersion = "mutated-copy"
	sealedSource, sealedRecord, err := frozen.Records()
	if err != nil {
		t.Fatal(err)
	}
	if sealedRecord.Name != "idle-slow-meter" || sealedSource.ResourceVersion != "42" {
		t.Fatalf("frozen policy changed through a detached record: source=%#v policy=%#v", sealedSource, sealedRecord)
	}
	policy, err := frozen.AdmissionPolicy()
	if err != nil {
		t.Fatal(err)
	}
	if policy.MinimumSamples != 3 || policy.MaximumGapSeconds != 12 ||
		!strings.HasPrefix(policy.Version, "sha256:") {
		t.Fatalf("admission policy = %#v", policy)
	}
	payload, err := frozen.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	payload[0] = 'x'
	sealedPayload, err := frozen.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if sealedPayload[0] != '{' {
		t.Fatalf("frozen policy changed through canonical byte projection: %q", sealedPayload)
	}
	payload = sealedPayload
	decoded, err := DecodeFrozenPolicy(payload)
	if err != nil {
		t.Fatal(err)
	}
	reencoded, err := decoded.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(payload, reencoded) {
		t.Fatal("frozen admission policy did not round-trip canonically")
	}
}

func TestPolicyVersionDependsOnSemanticsNotConfigMapRevision(t *testing.T) {
	base := PolicySource{
		Kind: ConfigMapPolicySourceKind, Namespace: "chill-system", Name: "chill-profiler-facts",
		UID: "facts-uid", ResourceVersion: "1", Key: "admission-policies.yaml",
	}
	first, err := FreezeConfiguredPolicy([]byte(policyCatalogFixture), "wall-fast-meter", base)
	if err != nil {
		t.Fatal(err)
	}
	base.ResourceVersion = "2"
	second, err := FreezeConfiguredPolicy([]byte(policyCatalogFixture), "wall-fast-meter", base)
	if err != nil {
		t.Fatal(err)
	}
	firstPolicy, _ := first.AdmissionPolicy()
	secondPolicy, _ := second.AdmissionPolicy()
	if firstPolicy.Version != secondPolicy.Version {
		t.Fatalf("source revision changed semantic policy version: %q != %q", firstPolicy.Version, secondPolicy.Version)
	}
	firstSource, _, firstErr := first.Records()
	secondSource, _, secondErr := second.Records()
	if firstErr != nil || secondErr != nil {
		t.Fatal(errors.Join(firstErr, secondErr))
	}
	if firstSource.ResourceVersion == secondSource.ResourceVersion {
		t.Fatal("source revision provenance was not retained")
	}
}

func TestFreezeConfiguredPolicyRejectsUnusableCatalog(t *testing.T) {
	source := PolicySource{
		Kind: ConfigMapPolicySourceKind, Namespace: "chill-system", Name: "facts",
		UID: "uid", ResourceVersion: "1", Key: "policies.yaml",
	}
	for _, raw := range []string{
		`schemaVersion: wrong\npolicies: []`,
		strings.Replace(policyCatalogFixture, "minimumSamples: 8", "minimumSamples: 1", 1),
		strings.Replace(policyCatalogFixture, "maximumGapSeconds: 0.75", "maximumGapSeconds: .nan", 1),
		strings.Replace(policyCatalogFixture, "name: idle-slow-meter", "name: wall-fast-meter", 1),
		policyCatalogFixture + "unknown: true\n",
	} {
		if _, err := FreezeConfiguredPolicy([]byte(raw), "wall-fast-meter", source); err == nil {
			t.Fatalf("invalid catalog accepted:\n%s", raw)
		}
	}
	if _, err := FreezeConfiguredPolicy([]byte(policyCatalogFixture), "missing", source); err == nil {
		t.Fatal("missing configured policy was accepted")
	}
}
