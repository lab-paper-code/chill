package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

const (
	WorkloadContractSchemaVersion = "chill.dacs.io/workload-contract.v1alpha1"
	legacyAllowedPolicyVersion    = "chill.dacs.io/allowed-background-policy.v1alpha1"

	MeasurementWorkloadLabelKey   = "edge.dacs.io/workload-role"
	MeasurementWorkloadLabelValue = "profiling-run"
)

// WorkloadContract freezes only the observation semantics used by current
// Runs. External workloads are evidence, not an installation-specific allow
// list. AllowedBackground remains solely for decoding retained v1alpha4 intent.
type WorkloadContract struct {
	SchemaVersion     string               `json:"schemaVersion,omitempty"`
	AllowedBackground *legacyAllowedPolicy `json:"allowedBackground,omitempty"`
}

type legacyAllowedPolicy struct {
	SchemaVersion string                     `json:"schemaVersion"`
	Selectors     []legacyBackgroundSelector `json:"selectors"`
}

type legacyBackgroundSelector struct {
	Namespace       string                   `json:"namespace"`
	ControllerOwner legacyControllerSelector `json:"controllerOwner"`
	MatchLabels     map[string]string        `json:"matchLabels"`
}

type legacyControllerSelector struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func CurrentWorkloadContract() WorkloadContract {
	return WorkloadContract{SchemaVersion: WorkloadContractSchemaVersion}
}

func (c WorkloadContract) Validate() error {
	if c.SchemaVersion == WorkloadContractSchemaVersion && c.AllowedBackground == nil {
		return nil
	}
	if c.SchemaVersion == "" && c.AllowedBackground != nil {
		return c.AllowedBackground.validate()
	}
	return fmt.Errorf("unsupported workload contract schemaVersion %q", c.SchemaVersion)
}

func (c WorkloadContract) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	if c.AllowedBackground != nil {
		return c.AllowedBackground.digest()
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode workload contract: %w", err)
	}
	return sha256Digest(payload), nil
}

// LabelKeys returns only the labels required to distinguish another CHILL
// measurement Pod. External workload identities remain generic Pod facts.
func (c WorkloadContract) LabelKeys() ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.AllowedBackground != nil {
		return c.AllowedBackground.labelKeys(), nil
	}
	return []string{
		"app.kubernetes.io/component",
		"app.kubernetes.io/name",
		MeasurementWorkloadLabelKey,
	}, nil
}

func IsMeasurementWorkload(ownerKind string, labels map[string]string) bool {
	if ownerKind != "Job" {
		return false
	}
	if labels[MeasurementWorkloadLabelKey] == MeasurementWorkloadLabelValue {
		return true
	}
	// Recognize retained CHILL profiling Jobs created before the dedicated role
	// label existed while current Jobs roll through the cluster.
	return labels["app.kubernetes.io/name"] == "chill" &&
		labels["app.kubernetes.io/component"] == "profiler"
}

func (p legacyAllowedPolicy) validate() error {
	if p.SchemaVersion != legacyAllowedPolicyVersion {
		return fmt.Errorf("unsupported retained allowed-background policy schemaVersion %q", p.SchemaVersion)
	}
	previous := ""
	for index, selector := range p.Selectors {
		if len(k8svalidation.IsDNS1123Label(selector.Namespace)) != 0 {
			return fmt.Errorf("retained allowed-background selector %d contains an invalid namespace", index)
		}
		if len(k8svalidation.IsCIdentifier(selector.ControllerOwner.Kind)) != 0 {
			return fmt.Errorf("retained allowed-background selector %d contains an invalid controller owner kind", index)
		}
		if len(k8svalidation.IsDNS1123Subdomain(selector.ControllerOwner.Name)) != 0 {
			return fmt.Errorf("retained allowed-background selector %d contains an invalid controller owner name", index)
		}
		if len(selector.MatchLabels) == 0 {
			return fmt.Errorf("retained allowed-background selector %d requires matchLabels", index)
		}
		for key, value := range selector.MatchLabels {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key ||
				strings.TrimSpace(value) != value || len(k8svalidation.IsQualifiedName(key)) != 0 ||
				len(k8svalidation.IsValidLabelValue(value)) != 0 {
				return fmt.Errorf("retained allowed-background selector %d contains an invalid label", index)
			}
		}
		canonical, err := json.Marshal(selector)
		if err != nil {
			return fmt.Errorf("encode retained allowed-background selector %d: %w", index, err)
		}
		identity := string(canonical)
		if previous != "" && identity <= previous {
			return errors.New("retained allowed-background selectors must be unique and canonically ordered")
		}
		previous = identity
	}
	return nil
}

func (p legacyAllowedPolicy) digest() (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode retained allowed-background policy: %w", err)
	}
	return sha256Digest(payload), nil
}

func (p legacyAllowedPolicy) labelKeys() []string {
	keys := map[string]struct{}{}
	for _, selector := range p.Selectors {
		for key := range selector.MatchLabels {
			keys[key] = struct{}{}
		}
	}
	result := make([]string, 0, len(keys))
	for key := range keys {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func sha256Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
