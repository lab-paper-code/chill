package admission

import (
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const (
	WallPowerPolicyV2     = "wall-power-v2"
	IdleWallPowerPolicyV1 = "idle-wall-power-v1"
)

// LegacyFrozenPolicy reconstructs the exact policy used by intent records
// written before admission-policy bytes were retained. New Runs always freeze
// a configured policy and do not use this compatibility path.
func LegacyFrozenPolicy(name, workloadMode string) (FrozenPolicy, error) {
	if name == WallPowerPolicyV2 && workloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		return FrozenPolicy{}, fmt.Errorf("legacy active policy %q cannot admit idle evidence", name)
	}
	if name == IdleWallPowerPolicyV1 && workloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		return FrozenPolicy{}, fmt.Errorf("legacy idle policy %q cannot admit active evidence", name)
	}
	if name != WallPowerPolicyV2 && name != IdleWallPowerPolicyV1 {
		return FrozenPolicy{}, fmt.Errorf("unsupported legacy admission policy %q", name)
	}
	policy := Policy{
		SchemaVersion:                  PolicySchemaVersion,
		Name:                           name,
		WorkloadMode:                   workloadMode,
		MinimumSamples:                 20,
		MaximumGapSeconds:              1.5,
		MaximumBoundaryDistanceSeconds: 1.0,
		MinimumCoverage:                0.95,
		AllowReceiptTimestamps:         true,
		RejectConcurrentCHILLWorkloads: true,
		RejectExternalWorkloadChanges:  true,
	}
	digest, err := policy.Digest()
	if err != nil {
		return FrozenPolicy{}, err
	}
	return FrozenPolicy{
		SchemaVersion: FrozenPolicySchemaVersion,
		Source: PolicySource{
			Kind:               BuiltInPolicySourceKind,
			Name:               "chill-pre-configured-admission-policy",
			EntryContentDigest: digest,
		},
		Policy: policy,
	}, nil
}
