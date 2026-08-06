package admission

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"

	"github.com/lab-paper-code/chill/internal/profilederivation"
	"sigs.k8s.io/yaml"
)

const (
	PolicyCatalogSchemaVersion = "chill.dacs.io/profiler-admission-policies.v1alpha1"
	PolicySchemaVersion        = "chill.dacs.io/profiler-admission-policy.v1alpha1"
	FrozenPolicySchemaVersion  = "chill.dacs.io/frozen-profiler-admission-policy.v1alpha2"

	ConfigMapPolicySourceKind = "ConfigMap"
)

var policyNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// PolicyCatalog is installation-time trusted configuration. A Run selects one
// entry by name; it cannot supply or override scientific thresholds.
type PolicyCatalog struct {
	SchemaVersion string       `json:"schemaVersion"`
	Policies      []PolicySpec `json:"policies"`
}

// PolicySpec is one administrator-configured single-Run admission policy.
type PolicySpec struct {
	Name                           string  `json:"name"`
	WorkloadMode                   string  `json:"workloadMode"`
	MinimumSamples                 int     `json:"minimumSamples"`
	MaximumGapSeconds              float64 `json:"maximumGapSeconds"`
	MaximumBoundaryDistanceSeconds float64 `json:"maximumBoundaryDistanceSeconds"`
	MinimumCoverage                float64 `json:"minimumCoverage"`
	AllowReceiptTimestamps         bool    `json:"allowReceiptTimestamps"`
	RejectConcurrentCHILLWorkloads bool    `json:"rejectConcurrentCHILLWorkloads"`
	RejectExternalWorkloadChanges  bool    `json:"rejectExternalWorkloadChanges"`
}

// Policy is the self-describing selected policy whose content digest becomes
// the scientific admission-policy version.
type Policy struct {
	SchemaVersion                  string  `json:"schemaVersion"`
	Name                           string  `json:"name"`
	WorkloadMode                   string  `json:"workloadMode"`
	MinimumSamples                 int     `json:"minimumSamples"`
	MaximumGapSeconds              float64 `json:"maximumGapSeconds"`
	MaximumBoundaryDistanceSeconds float64 `json:"maximumBoundaryDistanceSeconds"`
	MinimumCoverage                float64 `json:"minimumCoverage"`
	AllowReceiptTimestamps         bool    `json:"allowReceiptTimestamps"`
	RejectConcurrentCHILLWorkloads bool    `json:"rejectConcurrentCHILLWorkloads"`
	RejectExternalWorkloadChanges  bool    `json:"rejectExternalWorkloadChanges"`
}

// PolicySource binds configured policy semantics to the exact mutable source
// snapshot used before the selected policy is copied into an immutable record.
type PolicySource struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Key             string `json:"key,omitempty"`
}

type frozenPolicyRecord struct {
	SchemaVersion string       `json:"schemaVersion"`
	Source        PolicySource `json:"source"`
	Policy        Policy       `json:"policy"`
}

// FrozenPolicy is retained verbatim beside the immutable execution intent. A
// populated value can only be constructed by the catalog-freeze or retained-
// JSON decode boundaries in this package.
type FrozenPolicy struct {
	record       frozenPolicyRecord
	canonical    []byte
	policyDigest string
	verified     bool
}

// FreezeConfiguredPolicy strict-decodes one catalog and selects a policy by
// name while retaining exact ConfigMap provenance. The selected policy is
// copied into the frozen record and is the input to its semantic digest.
func FreezeConfiguredPolicy(raw []byte, selectedName string, source PolicySource) (FrozenPolicy, error) {
	if len(raw) == 0 {
		return FrozenPolicy{}, errors.New("admission-policy catalog is empty")
	}
	var catalog PolicyCatalog
	if err := yaml.UnmarshalStrict(raw, &catalog); err != nil {
		return FrozenPolicy{}, fmt.Errorf("decode admission-policy catalog: %w", err)
	}
	if catalog.SchemaVersion != PolicyCatalogSchemaVersion {
		return FrozenPolicy{}, fmt.Errorf("unsupported admission-policy catalog schema %q", catalog.SchemaVersion)
	}
	if len(catalog.Policies) == 0 {
		return FrozenPolicy{}, errors.New("admission-policy catalog contains no policies")
	}

	seen := make(map[string]struct{}, len(catalog.Policies))
	var selected *Policy
	for _, spec := range catalog.Policies {
		policy := policyFromSpec(spec)
		if err := policy.Validate(); err != nil {
			return FrozenPolicy{}, fmt.Errorf("validate configured admission policy %q: %w", spec.Name, err)
		}
		if _, exists := seen[policy.Name]; exists {
			return FrozenPolicy{}, fmt.Errorf("admission-policy catalog contains duplicate name %q", policy.Name)
		}
		seen[policy.Name] = struct{}{}
		if policy.Name == selectedName {
			copy := policy
			selected = &copy
		}
	}
	if selected == nil {
		return FrozenPolicy{}, fmt.Errorf("admission policy %q is not configured", selectedName)
	}

	return sealFrozenPolicy(frozenPolicyRecord{
		SchemaVersion: FrozenPolicySchemaVersion, Source: source, Policy: *selected,
	})
}

func DecodeFrozenPolicy(raw []byte) (FrozenPolicy, error) {
	var record frozenPolicyRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return FrozenPolicy{}, fmt.Errorf("decode frozen admission policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return FrozenPolicy{}, errors.New("frozen admission policy contains a trailing JSON value")
	}
	if err := validateFrozenPolicy(record); err != nil {
		return FrozenPolicy{}, err
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return FrozenPolicy{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return FrozenPolicy{}, errors.New("stored admission policy is not canonical")
	}
	return newFrozenPolicy(record, canonical)
}

func validateFrozenPolicy(record frozenPolicyRecord) error {
	if record.SchemaVersion != FrozenPolicySchemaVersion {
		return fmt.Errorf("unsupported frozen admission-policy schema %q", record.SchemaVersion)
	}
	if err := record.Policy.Validate(); err != nil {
		return fmt.Errorf("validate frozen admission policy: %w", err)
	}
	if record.Source.Kind != ConfigMapPolicySourceKind {
		return fmt.Errorf("unsupported admission-policy source kind %q", record.Source.Kind)
	}
	for name, value := range map[string]string{
		"namespace": record.Source.Namespace, "name": record.Source.Name, "uid": record.Source.UID,
		"resourceVersion": record.Source.ResourceVersion, "key": record.Source.Key,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("admission-policy ConfigMap source %s is required without surrounding whitespace", name)
		}
	}
	return nil
}

func sealFrozenPolicy(record frozenPolicyRecord) (FrozenPolicy, error) {
	if err := validateFrozenPolicy(record); err != nil {
		return FrozenPolicy{}, err
	}
	canonical, err := json.Marshal(record)
	if err != nil {
		return FrozenPolicy{}, fmt.Errorf("encode frozen admission policy: %w", err)
	}
	return newFrozenPolicy(record, canonical)
}

func newFrozenPolicy(record frozenPolicyRecord, canonical []byte) (FrozenPolicy, error) {
	policyBytes, err := json.Marshal(record.Policy)
	if err != nil {
		return FrozenPolicy{}, fmt.Errorf("encode verified admission policy: %w", err)
	}
	return FrozenPolicy{
		record: record, canonical: append([]byte(nil), canonical...),
		policyDigest: digestBytes(policyBytes), verified: true,
	}, nil
}

func (f FrozenPolicy) Records() (PolicySource, Policy, error) {
	if !f.verified {
		return PolicySource{}, Policy{}, errors.New("frozen admission policy is empty")
	}
	return f.record.Source, f.record.Policy, nil
}

func (f FrozenPolicy) CanonicalJSON() ([]byte, error) {
	if !f.verified {
		return nil, errors.New("frozen admission policy is empty")
	}
	return append([]byte(nil), f.canonical...), nil
}

func (f FrozenPolicy) Digest() (string, error) {
	if !f.verified {
		return "", errors.New("frozen admission policy is empty")
	}
	return f.policyDigest, nil
}

func (f FrozenPolicy) AdmissionPolicy() (profilederivation.AdmissionPolicy, error) {
	_, policy, err := f.Records()
	if err != nil {
		return profilederivation.AdmissionPolicy{}, err
	}
	return profilederivation.AdmissionPolicy{
		Version:                        f.policyDigest,
		MinimumSamples:                 policy.MinimumSamples,
		MaximumGapSeconds:              policy.MaximumGapSeconds,
		MaximumBoundaryDistanceSeconds: policy.MaximumBoundaryDistanceSeconds,
		MinimumCoverage:                policy.MinimumCoverage,
		AllowReceiptTimestamps:         policy.AllowReceiptTimestamps,
		RejectConcurrentCHILLWorkloads: policy.RejectConcurrentCHILLWorkloads,
		RejectExternalWorkloadChanges:  policy.RejectExternalWorkloadChanges,
	}, nil
}

func (p Policy) Validate() error {
	if p.SchemaVersion != PolicySchemaVersion {
		return fmt.Errorf("unsupported admission-policy schema %q", p.SchemaVersion)
	}
	if len(p.Name) < 1 || len(p.Name) > 63 || !policyNamePattern.MatchString(p.Name) {
		return errors.New("admission-policy name must be a lowercase DNS-style name of at most 63 characters")
	}
	if strings.TrimSpace(p.WorkloadMode) == "" || strings.TrimSpace(p.WorkloadMode) != p.WorkloadMode {
		return errors.New("admission-policy workloadMode is required without surrounding whitespace")
	}
	if !finite(p.MaximumGapSeconds) || !finite(p.MaximumBoundaryDistanceSeconds) || !finite(p.MinimumCoverage) ||
		p.MinimumSamples < 2 || p.MaximumGapSeconds <= 0 || p.MaximumBoundaryDistanceSeconds < 0 ||
		p.MinimumCoverage <= 0 || p.MinimumCoverage > 1 {
		return errors.New("admission-policy power-window thresholds are invalid")
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func (p Policy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode admission policy: %w", err)
	}
	return digestBytes(payload), nil
}

func policyFromSpec(spec PolicySpec) Policy {
	return Policy{
		SchemaVersion: PolicySchemaVersion,
		Name:          spec.Name, WorkloadMode: spec.WorkloadMode,
		MinimumSamples: spec.MinimumSamples, MaximumGapSeconds: spec.MaximumGapSeconds,
		MaximumBoundaryDistanceSeconds: spec.MaximumBoundaryDistanceSeconds,
		MinimumCoverage:                spec.MinimumCoverage, AllowReceiptTimestamps: spec.AllowReceiptTimestamps,
		RejectConcurrentCHILLWorkloads: spec.RejectConcurrentCHILLWorkloads,
		RejectExternalWorkloadChanges:  spec.RejectExternalWorkloadChanges,
	}
}

func digestBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
