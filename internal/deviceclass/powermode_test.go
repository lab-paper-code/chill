package deviceclass

import (
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

func TestValidatePowerModesAcceptsTypedActuators(t *testing.T) {
	mode := int32(1)
	modes := []edgev1alpha1.PowerMode{
		{Name: "fixed", Actuator: edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed}},
		{Name: "15W", Actuator: edgev1alpha1.PowerModeActuator{
			Kind:           edgev1alpha1.PowerModeActuatorJetsonNVPModel,
			JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0},
		}, OnlineCPUs: "0-3,6"},
		{Name: "gpu-1200mhz", Actuator: edgev1alpha1.PowerModeActuator{
			Kind: edgev1alpha1.PowerModeActuatorNvidiaLockedGPUClock,
			NvidiaLockedGPUClock: &edgev1alpha1.NvidiaLockedGPUClockTarget{
				MinMHz: 1200, MaxMHz: 1200, Mode: &mode,
			},
		}},
	}

	if errs := ValidatePowerModes(modes, field.NewPath("powerModes")); len(errs) > 0 {
		t.Fatalf("ValidatePowerModes() errors = %v", errs)
	}
}

func TestValidatePowerModesRejectsAmbiguousActuators(t *testing.T) {
	tests := []struct {
		name  string
		modes []edgev1alpha1.PowerMode
	}{
		{
			name: "duplicate names",
			modes: []edgev1alpha1.PowerMode{
				{Name: "fixed", Actuator: edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed}},
				{Name: "fixed", Actuator: edgev1alpha1.PowerModeActuator{Kind: edgev1alpha1.PowerModeActuatorFixed}},
			},
		},
		{
			name: "fixed with target",
			modes: []edgev1alpha1.PowerMode{{Name: "fixed", Actuator: edgev1alpha1.PowerModeActuator{
				Kind:           edgev1alpha1.PowerModeActuatorFixed,
				JetsonNVPModel: &edgev1alpha1.JetsonNVPModelTarget{ID: 0},
			}}},
		},
		{
			name: "nvpmodel without target",
			modes: []edgev1alpha1.PowerMode{{Name: "15W", Actuator: edgev1alpha1.PowerModeActuator{
				Kind: edgev1alpha1.PowerModeActuatorJetsonNVPModel,
			}}},
		},
		{
			name: "reversed gpu clock range",
			modes: []edgev1alpha1.PowerMode{{Name: "gpu", Actuator: edgev1alpha1.PowerModeActuator{
				Kind: edgev1alpha1.PowerModeActuatorNvidiaLockedGPUClock,
				NvidiaLockedGPUClock: &edgev1alpha1.NvidiaLockedGPUClockTarget{
					MinMHz: 1200, MaxMHz: 600,
				},
			}}},
		},
		{
			name: "unreadable online CPUs",
			modes: []edgev1alpha1.PowerMode{{Name: "fixed", Actuator: edgev1alpha1.PowerModeActuator{
				Kind: edgev1alpha1.PowerModeActuatorFixed,
			}, OnlineCPUs: "3-1"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if errs := ValidatePowerModes(test.modes, field.NewPath("powerModes")); len(errs) == 0 {
				t.Fatal("ValidatePowerModes() errors = nil")
			}
		})
	}
}
