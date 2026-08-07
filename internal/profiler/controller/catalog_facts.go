package controller

import (
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	"k8s.io/apimachinery/pkg/types"
)

type selectedDeviceFacts struct {
	nodeSelector     map[string]string
	powerModes       []string
	selectedActuator *profilingrun.ActuatorTarget
}

func validateDeviceClass(deviceClass *edgev1alpha1.DeviceClass, powerMode string) (selectedDeviceFacts, error) {
	if err := validateObjectIdentity("DeviceClass", deviceClass.Name, deviceClass.UID, deviceClass.ResourceVersion); err != nil {
		return selectedDeviceFacts{}, err
	}
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
	if err := validateObjectIdentity("ModelSpec", model.Name, model.UID, model.ResourceVersion); err != nil {
		return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, err
	}
	artifacts := make(map[string]edgev1alpha1.ModelArtifact, len(model.Spec.Artifacts))
	for _, artifact := range model.Spec.Artifacts {
		if _, duplicate := artifacts[artifact.Name]; duplicate {
			return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, fmt.Errorf(
				"ModelSpec contains duplicate artifact %q", artifact.Name,
			)
		}
		artifacts[artifact.Name] = artifact
	}
	var selected *edgev1alpha1.ModelExecutionPath
	seenPaths := map[string]struct{}{}
	for index := range model.Spec.ExecutionPaths {
		path := &model.Spec.ExecutionPaths[index]
		if _, duplicate := seenPaths[path.Name]; duplicate {
			return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, fmt.Errorf(
				"ModelSpec contains duplicate execution path %q", path.Name,
			)
		}
		seenPaths[path.Name] = struct{}{}
		if path.Name == pathName {
			copy := *path
			selected = &copy
		}
	}
	if selected == nil {
		return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, fmt.Errorf(
			"execution path %q is not declared by ModelSpec %q", pathName, model.Name,
		)
	}
	artifact, exists := artifacts[selected.Artifact]
	if !exists {
		return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, fmt.Errorf(
			"execution path %q references missing artifact %q", selected.Name, selected.Artifact,
		)
	}
	if artifact.Derivation != nil {
		if artifact.Derivation.SourceArtifact == artifact.Name {
			return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, fmt.Errorf(
				"derived artifact %q references itself as source", artifact.Name,
			)
		}
		if _, exists := artifacts[artifact.Derivation.SourceArtifact]; !exists {
			return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, fmt.Errorf(
				"derived artifact %q references missing source artifact %q",
				artifact.Name, artifact.Derivation.SourceArtifact,
			)
		}
	}
	if selected.Runtime.Family == profilingrun.RKNNLiteRuntimeFamily {
		if artifact.Derivation == nil || artifact.Derivation.Input == nil ||
			len(artifact.Derivation.Input.Shape) == 0 ||
			artifact.Derivation.Input.Shape[0] != artifact.Derivation.FixedBatch ||
			artifact.Derivation.Input.Layout != profilingrun.RKNNLiteInputLayout ||
			artifact.Derivation.Input.DataType != profilingrun.RKNNLiteInputDataType {
			return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, errors.New(
				"RKNNLite artifact requires a matching fixed input tensor contract",
			)
		}
		for _, dimension := range artifact.Derivation.Input.Shape {
			if dimension < 1 {
				return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, errors.New(
					"RKNNLite artifact input dimensions must be positive",
				)
			}
		}
	}
	return *selected, artifact, nil
}

func validateObjectIdentity(kind, name string, uid types.UID, resourceVersion string) error {
	if strings.TrimSpace(name) == "" || uid == "" || strings.TrimSpace(resourceVersion) == "" {
		return fmt.Errorf("%s has incomplete API identity", kind)
	}
	return nil
}
