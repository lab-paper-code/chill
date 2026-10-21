package profiler

import (
	"bytes"
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
	if err := selection.Validate(); err != nil {
		return FrozenActuationPolicy{}, fmt.Errorf("validate actuation selection: %w", err)
	}
	policies, err := DecodeActuationPolicyCatalog(raw)
	if err != nil {
		return FrozenActuationPolicy{}, err
	}
	var selected *ActuationPolicy
	for _, spec := range policies {
		if spec.DeviceClass == selection.DeviceClass && spec.PowerMode == selection.PowerMode &&
			spec.ActuatorKind == selection.Actuator.Kind {
			selected = &ActuationPolicy{SchemaVersion: ActuationPolicySchemaVersion, ActuationPolicySpec: spec}
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

// DecodeActuationPolicyCatalog returns the validated configured policies,
// unique by name and by DeviceClass, mode, and actuator.
func DecodeActuationPolicyCatalog(raw []byte) ([]ActuationPolicySpec, error) {
	if len(raw) == 0 {
		return nil, errors.New("actuation-policy catalog is empty")
	}
	var catalog ActuationPolicyCatalog
	if err := yaml.UnmarshalStrict(raw, &catalog); err != nil {
		return nil, fmt.Errorf("decode actuation-policy catalog: %w", err)
	}
	if catalog.SchemaVersion != ActuationPolicyCatalogSchemaVersion {
		return nil, fmt.Errorf("unsupported actuation-policy catalog schema %q", catalog.SchemaVersion)
	}
	if len(catalog.Policies) == 0 {
		return nil, errors.New("actuation-policy catalog contains no policies")
	}
	seenNames := make(map[string]struct{}, len(catalog.Policies))
	seenSelectors := make(map[string]struct{}, len(catalog.Policies))
	for _, policy := range catalog.Policies {
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("validate configured actuation policy %q: %w", policy.Name, err)
		}
		if _, exists := seenNames[policy.Name]; exists {
			return nil, fmt.Errorf("actuation-policy catalog contains duplicate name %q", policy.Name)
		}
		seenNames[policy.Name] = struct{}{}
		selector := policy.DeviceClass + "\x00" + policy.PowerMode + "\x00" + policy.ActuatorKind
		if _, exists := seenSelectors[selector]; exists {
			return nil, fmt.Errorf("actuation-policy catalog contains duplicate selector %q/%q/%q",
				policy.DeviceClass, policy.PowerMode, policy.ActuatorKind)
		}
		seenSelectors[selector] = struct{}{}
	}
	return catalog.Policies, nil
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
	return frozen, nil
}

func (f FrozenActuationPolicy) CanonicalJSON() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(f)
}
