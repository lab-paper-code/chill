package admission

import (
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilederivation"
)

const (
	WallPowerPolicyV2     = "wall-power-v2"
	IdleWallPowerPolicyV1 = "idle-wall-power-v1"
)

func builtInPolicy(name string) (profilederivation.AdmissionPolicy, error) {
	if name != WallPowerPolicyV2 {
		return profilederivation.AdmissionPolicy{}, fmt.Errorf("unsupported admission policy %q", name)
	}
	return powerWindowPolicy(WallPowerPolicyV2), nil
}

func builtInIdlePolicy(name string) (profilederivation.AdmissionPolicy, error) {
	if name != IdleWallPowerPolicyV1 {
		return profilederivation.AdmissionPolicy{}, fmt.Errorf("unsupported idle admission policy %q", name)
	}
	// Idle admission deliberately shares active power-window quality thresholds.
	// Its distinct version also covers the mode-specific completeness gates in
	// profilederivation.AdmitIdle.
	return powerWindowPolicy(IdleWallPowerPolicyV1), nil
}

func powerWindowPolicy(version string) profilederivation.AdmissionPolicy {
	return profilederivation.AdmissionPolicy{
		Version:                        version,
		MinimumSamples:                 20,
		MaximumGapSeconds:              1.5,
		MaximumBoundaryDistanceSeconds: 1.0,
		MinimumCoverage:                0.95,
		AllowReceiptTimestamps:         true,
		RejectConcurrentCHILLWorkloads: true,
		RejectExternalWorkloadChanges:  true,
	}
}
