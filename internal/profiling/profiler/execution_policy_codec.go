package profiler

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"sigs.k8s.io/yaml"
)

func FreezeConfiguredExecutionPolicy(
	raw []byte,
	intent profilingrun.Intent,
	source ExecutionPolicySource,
) (FrozenExecutionPolicy, error) {
	if len(raw) == 0 {
		return FrozenExecutionPolicy{}, errors.New("execution-policy catalog is empty")
	}
	if err := intent.Validate(); err != nil {
		return FrozenExecutionPolicy{}, fmt.Errorf("validate frozen Run intent: %w", err)
	}
	var catalog ExecutionPolicyCatalog
	if err := yaml.UnmarshalStrict(raw, &catalog); err != nil {
		return FrozenExecutionPolicy{}, fmt.Errorf("decode execution-policy catalog: %w", err)
	}
	if catalog.SchemaVersion != ExecutionPolicyCatalogSchemaVersion {
		return FrozenExecutionPolicy{}, fmt.Errorf("unsupported execution-policy catalog schema %q", catalog.SchemaVersion)
	}
	if len(catalog.Policies) == 0 {
		return FrozenExecutionPolicy{}, errors.New("execution-policy catalog contains no policies")
	}

	seenNames := make(map[string]struct{}, len(catalog.Policies))
	seenSelectors := make(map[string]struct{}, len(catalog.Policies))
	var exact *ExecutionPolicy
	var fallback *ExecutionPolicy
	for _, spec := range catalog.Policies {
		policy := ExecutionPolicy{SchemaVersion: ExecutionPolicySchemaVersion, ExecutionPolicySpec: spec}
		if err := policy.Validate(); err != nil {
			return FrozenExecutionPolicy{}, fmt.Errorf("validate configured execution policy %q: %w", spec.Name, err)
		}
		if _, exists := seenNames[policy.Name]; exists {
			return FrozenExecutionPolicy{}, fmt.Errorf("execution-policy catalog contains duplicate name %q", policy.Name)
		}
		seenNames[policy.Name] = struct{}{}
		selector := policy.DeviceClass + "\x00" + policy.ExecutionPath + "\x00" + policy.WorkloadMode
		if _, exists := seenSelectors[selector]; exists {
			return FrozenExecutionPolicy{}, fmt.Errorf(
				"execution-policy catalog contains duplicate selector %q/%q/%q",
				policy.DeviceClass,
				policy.ExecutionPath,
				policy.WorkloadMode,
			)
		}
		seenSelectors[selector] = struct{}{}
		if policy.ExecutionPath != intent.Candidate.ExecutionPath ||
			policy.WorkloadMode != intent.Measurement.WorkloadMode {
			continue
		}
		if policy.DeviceClass == intent.State.DeviceClass {
			copy := policy
			exact = &copy
		} else if policy.DeviceClass == "*" {
			copy := policy
			fallback = &copy
		}
	}
	selected := exact
	if selected == nil {
		selected = fallback
	}
	if selected == nil {
		return FrozenExecutionPolicy{}, fmt.Errorf(
			"no execution policy is configured for DeviceClass %q, path %q, and workload mode %q",
			intent.State.DeviceClass,
			intent.Candidate.ExecutionPath,
			intent.Measurement.WorkloadMode,
		)
	}

	frozen := FrozenExecutionPolicy{
		SchemaVersion: FrozenExecutionPolicySchemaVersion,
		Source:        source,
		Policy:        *selected,
	}
	if err := frozen.Validate(); err != nil {
		return FrozenExecutionPolicy{}, err
	}
	return frozen, nil
}

func DecodeFrozenExecutionPolicy(raw []byte) (FrozenExecutionPolicy, error) {
	var frozen FrozenExecutionPolicy
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frozen); err != nil {
		return FrozenExecutionPolicy{}, fmt.Errorf("decode frozen execution policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return FrozenExecutionPolicy{}, errors.New("frozen execution policy contains a trailing JSON value")
	}
	if err := frozen.Validate(); err != nil {
		return FrozenExecutionPolicy{}, err
	}
	return frozen, nil
}

func (f FrozenExecutionPolicy) CanonicalJSON() ([]byte, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(f)
}
