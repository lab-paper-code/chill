package deviceclass

import (
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

// ValidatePowerModes validates the semantic contract also used by catalog
// ConfigMaps, which do not pass through CRD admission.
func ValidatePowerModes(modes []edgev1alpha1.PowerMode, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	seen := make(map[string]struct{}, len(modes))
	for i := range modes {
		modePath := path.Index(i)
		mode := &modes[i]
		if strings.TrimSpace(mode.Name) == "" || strings.TrimSpace(mode.Name) != mode.Name {
			allErrs = append(allErrs, field.Invalid(modePath.Child("name"), mode.Name, "must be non-empty without surrounding whitespace"))
		} else if _, exists := seen[mode.Name]; exists {
			allErrs = append(allErrs, field.Duplicate(modePath.Child("name"), mode.Name))
		}
		seen[mode.Name] = struct{}{}
		allErrs = append(allErrs, validatePowerModeActuator(mode.Actuator, modePath.Child("actuator"))...)
	}
	return allErrs
}

// FindPowerMode returns the uniquely named mode after validating the complete
// declaration. Callers therefore never act on an ambiguous catalog entry.
func FindPowerMode(modes []edgev1alpha1.PowerMode, name string) (edgev1alpha1.PowerMode, error) {
	if errs := ValidatePowerModes(modes, field.NewPath("powerModes")); len(errs) > 0 {
		return edgev1alpha1.PowerMode{}, errs.ToAggregate()
	}
	for i := range modes {
		if modes[i].Name == name {
			return *modes[i].DeepCopy(), nil
		}
	}
	return edgev1alpha1.PowerMode{}, fmt.Errorf("power mode %q is not declared", name)
}

func validatePowerModeActuator(actuator edgev1alpha1.PowerModeActuator, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	switch actuator.Kind {
	case edgev1alpha1.PowerModeActuatorFixed:
		if actuator.JetsonNVPModel != nil || actuator.NvidiaLockedGPUClock != nil {
			allErrs = append(allErrs, field.Invalid(path, actuator, "Fixed must not declare a target"))
		}
	case edgev1alpha1.PowerModeActuatorJetsonNVPModel:
		if actuator.JetsonNVPModel == nil || actuator.NvidiaLockedGPUClock != nil {
			allErrs = append(allErrs, field.Invalid(path, actuator, "JetsonNVPModel requires only jetsonNVPModel"))
		} else if actuator.JetsonNVPModel.ID < 0 {
			allErrs = append(allErrs, field.Invalid(path.Child("jetsonNVPModel", "id"), actuator.JetsonNVPModel.ID, "must be non-negative"))
		}
	case edgev1alpha1.PowerModeActuatorNvidiaLockedGPUClock:
		target := actuator.NvidiaLockedGPUClock
		if target == nil || actuator.JetsonNVPModel != nil {
			allErrs = append(allErrs, field.Invalid(path, actuator, "NvidiaLockedGPUClock requires only nvidiaLockedGPUClock"))
		} else {
			if target.MinMHz <= 0 {
				allErrs = append(allErrs, field.Invalid(path.Child("nvidiaLockedGPUClock", "minMHz"), target.MinMHz, "must be positive"))
			}
			if target.MaxMHz <= 0 {
				allErrs = append(allErrs, field.Invalid(path.Child("nvidiaLockedGPUClock", "maxMHz"), target.MaxMHz, "must be positive"))
			}
			if target.MinMHz > target.MaxMHz {
				allErrs = append(allErrs, field.Invalid(path.Child("nvidiaLockedGPUClock"), target, "minMHz must not exceed maxMHz"))
			}
			if target.Mode != nil && *target.Mode != 0 && *target.Mode != 1 {
				allErrs = append(allErrs, field.NotSupported(path.Child("nvidiaLockedGPUClock", "mode"), *target.Mode, []string{"0", "1"}))
			}
		}
	default:
		allErrs = append(allErrs, field.NotSupported(path.Child("kind"), actuator.Kind, []string{
			string(edgev1alpha1.PowerModeActuatorFixed),
			string(edgev1alpha1.PowerModeActuatorJetsonNVPModel),
			string(edgev1alpha1.PowerModeActuatorNvidiaLockedGPUClock),
		}))
	}
	return allErrs
}
