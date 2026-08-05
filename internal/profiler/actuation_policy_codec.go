package profiler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"sigs.k8s.io/yaml"
)

func FreezeConfiguredActuationPolicy(
	raw []byte,
	selection ActuationSelection,
	source ActuationPolicySource,
) (FrozenActuationPolicy, error) {
	if len(raw) == 0 {
		return FrozenActuationPolicy{}, errors.New("actuation-policy catalog is empty")
	}
	if err := selection.Validate(); err != nil {
		return FrozenActuationPolicy{}, fmt.Errorf("validate actuation selection: %w", err)
	}
	var catalog ActuationPolicyCatalog
	if err := yaml.UnmarshalStrict(raw, &catalog); err != nil {
		return FrozenActuationPolicy{}, fmt.Errorf("decode actuation-policy catalog: %w", err)
	}
	if catalog.SchemaVersion != ActuationPolicyCatalogSchemaVersion {
		return FrozenActuationPolicy{}, fmt.Errorf("unsupported actuation-policy catalog schema %q", catalog.SchemaVersion)
	}
	if len(catalog.Policies) == 0 {
		return FrozenActuationPolicy{}, errors.New("actuation-policy catalog contains no policies")
	}

	seenNames := make(map[string]struct{}, len(catalog.Policies))
	seenSelectors := make(map[string]struct{}, len(catalog.Policies))
	var selected *ActuationPolicy
	for _, spec := range catalog.Policies {
		policy := ActuationPolicy{SchemaVersion: ActuationPolicySchemaVersion, ActuationPolicySpec: spec}
		if err := policy.Validate(); err != nil {
			return FrozenActuationPolicy{}, fmt.Errorf("validate configured actuation policy %q: %w", spec.Name, err)
		}
		if _, exists := seenNames[policy.Name]; exists {
			return FrozenActuationPolicy{}, fmt.Errorf("actuation-policy catalog contains duplicate name %q", policy.Name)
		}
		seenNames[policy.Name] = struct{}{}
		selector := policy.DeviceClass + "\x00" + policy.PowerMode + "\x00" + policy.ActuatorKind
		if _, exists := seenSelectors[selector]; exists {
			return FrozenActuationPolicy{}, fmt.Errorf(
				"actuation-policy catalog contains duplicate selector %q/%q/%q",
				policy.DeviceClass,
				policy.PowerMode,
				policy.ActuatorKind,
			)
		}
		seenSelectors[selector] = struct{}{}
		if policy.DeviceClass == selection.DeviceClass && policy.PowerMode == selection.PowerMode &&
			policy.ActuatorKind == selection.Actuator.Kind {
			copy := policy
			selected = &copy
		}
	}
	if selected == nil {
		return FrozenActuationPolicy{}, fmt.Errorf(
			"no actuation policy is configured for DeviceClass %q, mode %q, and actuator %q",
			selection.DeviceClass,
			selection.PowerMode,
			selection.Actuator.Kind,
		)
	}

	source.CatalogContentDigest = actuationPolicyDigestBytes(raw)
	frozen := FrozenActuationPolicy{
		SchemaVersion: FrozenActuationPolicySchemaVersion,
		Source:        source,
		Policy:        *selected,
	}
	if err := frozen.Validate(); err != nil {
		return FrozenActuationPolicy{}, err
	}
	if err := validateActuationPolicySelection(frozen.Policy, selection); err != nil {
		return FrozenActuationPolicy{}, err
	}
	return frozen, nil
}

func DecodeFrozenActuationPolicy(raw []byte) (FrozenActuationPolicy, error) {
	var frozen FrozenActuationPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frozen); err != nil {
		return FrozenActuationPolicy{}, fmt.Errorf("decode frozen actuation policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return FrozenActuationPolicy{}, errors.New("frozen actuation policy contains a trailing JSON value")
	}
	if err := frozen.Validate(); err != nil {
		return FrozenActuationPolicy{}, err
	}
	canonical, err := frozen.CanonicalJSON()
	if err != nil {
		return FrozenActuationPolicy{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return FrozenActuationPolicy{}, errors.New("stored actuation policy is not canonical")
	}
	return frozen, nil
}

func (f FrozenActuationPolicy) CanonicalJSON() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(f)
}

func actuationPolicyDigestBytes(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
