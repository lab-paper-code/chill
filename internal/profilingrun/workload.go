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

const AllowedBackgroundPolicySchemaVersion = "chill.dacs.io/allowed-background-policy.v1alpha1"

// WorkloadContract freezes the installation-owned policy used to interpret
// co-resident Pods. Observation records facts; admission applies this policy.
type WorkloadContract struct {
	AllowedBackground AllowedBackgroundPolicy `json:"allowedBackground"`
}

type AllowedBackgroundPolicy struct {
	SchemaVersion string                  `json:"schemaVersion"`
	Selectors     []BackgroundPodSelector `json:"selectors"`
}

// BackgroundPodSelector binds exact labels to one controller-owned workload.
// More selector operators require a new policy schema rather than implicit drift.
type BackgroundPodSelector struct {
	Namespace       string                  `json:"namespace"`
	ControllerOwner ControllerOwnerSelector `json:"controllerOwner"`
	MatchLabels     map[string]string       `json:"matchLabels"`
}

type ControllerOwnerSelector struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

func (p AllowedBackgroundPolicy) Validate() error {
	if p.SchemaVersion != AllowedBackgroundPolicySchemaVersion {
		return fmt.Errorf("unsupported allowed-background policy schemaVersion %q", p.SchemaVersion)
	}
	previous := ""
	for index, selector := range p.Selectors {
		if len(k8svalidation.IsDNS1123Label(selector.Namespace)) != 0 {
			return fmt.Errorf("allowed-background selector %d contains an invalid namespace", index)
		}
		if len(k8svalidation.IsCIdentifier(selector.ControllerOwner.Kind)) != 0 {
			return fmt.Errorf("allowed-background selector %d contains an invalid controller owner kind", index)
		}
		if len(k8svalidation.IsDNS1123Subdomain(selector.ControllerOwner.Name)) != 0 {
			return fmt.Errorf("allowed-background selector %d contains an invalid controller owner name", index)
		}
		if len(selector.MatchLabels) == 0 {
			return fmt.Errorf("allowed-background selector %d requires matchLabels", index)
		}
		for key, value := range selector.MatchLabels {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(key) != key ||
				strings.TrimSpace(value) != value || len(k8svalidation.IsQualifiedName(key)) != 0 ||
				len(k8svalidation.IsValidLabelValue(value)) != 0 {
				return fmt.Errorf("allowed-background selector %d contains an invalid label", index)
			}
		}
		canonical, err := json.Marshal(selector)
		if err != nil {
			return fmt.Errorf("encode allowed-background selector %d: %w", index, err)
		}
		identity := string(canonical)
		if previous != "" && identity <= previous {
			return errors.New("allowed-background selectors must be unique and canonically ordered")
		}
		previous = identity
	}
	return nil
}

func (p AllowedBackgroundPolicy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encode allowed-background policy: %w", err)
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (p AllowedBackgroundPolicy) LabelKeys() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
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
	return result, nil
}

// Matches reports whether one observed Pod identity satisfies a frozen exact
// selector. Callers validate the policy before using it.
func (p AllowedBackgroundPolicy) Matches(
	namespace, ownerKind, ownerName string,
	labels map[string]string,
) bool {
	for _, selector := range p.Selectors {
		if selector.Namespace != namespace || selector.ControllerOwner.Kind != ownerKind ||
			selector.ControllerOwner.Name != ownerName {
			continue
		}
		matched := true
		for key, value := range selector.MatchLabels {
			if labels[key] != value {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
