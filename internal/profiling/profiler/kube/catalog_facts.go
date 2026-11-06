package kube

import (
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

type selectedDeviceFacts struct {
	nodeSelector     map[string]string
	powerModes       []string
	selectedActuator *profilingrun.ActuatorTarget
}

func validateDeviceClass(deviceClass *edgev1alpha1.DeviceClass, powerMode string) (selectedDeviceFacts, error) {
	if strings.TrimSpace(deviceClass.Spec.Architecture) == "" || len(deviceClass.Spec.NodeSelector) == 0 {
		return selectedDeviceFacts{}, errors.New("DeviceClass architecture and nodeSelector are required")
	}
	selectedMode, err := deviceclass.FindPowerMode(deviceClass.Spec.PowerModes, powerMode)
	if err != nil {
		return selectedDeviceFacts{}, fmt.Errorf("DeviceClass %q power modes: %w", deviceClass.Name, err)
	}
	selectedActuator, err := projectSelectedActuator(powerMode, selectedMode.Actuator)
	if err != nil {
		return selectedDeviceFacts{}, fmt.Errorf("DeviceClass %q selected power mode: %w", deviceClass.Name, err)
	}
	selector := make(map[string]string, len(deviceClass.Spec.NodeSelector))
	for key, value := range deviceClass.Spec.NodeSelector {
		selector[key] = value
	}
	powerModes := make([]string, len(deviceClass.Spec.PowerModes))
	for index, mode := range deviceClass.Spec.PowerModes {
		powerModes[index] = mode.Name
	}
	return selectedDeviceFacts{
		nodeSelector: selector, powerModes: powerModes, selectedActuator: selectedActuator,
	}, nil
}

func projectSelectedActuator(
	powerMode string,
	actuator edgev1alpha1.PowerModeActuator,
) (*profilingrun.ActuatorTarget, error) {
	if actuator.Kind == edgev1alpha1.PowerModeActuatorFixed {
		if powerMode != profilingrun.FixedPowerMode {
			return nil, errors.New("Fixed actuator must use the fixed mode name")
		}
		return nil, nil
	}
	if powerMode == profilingrun.FixedPowerMode {
		return nil, errors.New("fixed mode name must use the Fixed actuator")
	}
	target := &profilingrun.ActuatorTarget{}
	switch actuator.Kind {
	case edgev1alpha1.PowerModeActuatorJetsonNVPModel:
		target.Kind = profilingrun.ActuatorKindJetsonNVPModel
		target.JetsonNVPModel = &profilingrun.JetsonNVPModelTarget{ID: actuator.JetsonNVPModel.ID}
	case edgev1alpha1.PowerModeActuatorNvidiaLockedGPUClock:
		target.Kind = profilingrun.ActuatorKindNvidiaLockedGPUClock
		clock := actuator.NvidiaLockedGPUClock
		target.NvidiaLockedGPUClock = &profilingrun.NvidiaLockedGPUClockTarget{
			MinMHz: clock.MinMHz, MaxMHz: clock.MaxMHz,
		}
		if clock.Mode != nil {
			mode := *clock.Mode
			target.NvidiaLockedGPUClock.Mode = &mode
		}
	default:
		return nil, fmt.Errorf("unsupported actuator kind %q", actuator.Kind)
	}
	if err := target.Validate(); err != nil {
		return nil, err
	}
	return target, nil
}

func selectModelPath(
	model *edgev1alpha1.ModelSpec,
	pathName string,
) (edgev1alpha1.ModelExecutionPath, edgev1alpha1.ModelArtifact, error) {
	return modelcatalog.SelectModelPath(model, pathName)
}

