package controller

import (
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"k8s.io/apimachinery/pkg/types"
)

const fixedPowerMode = "fixed"

type selectedDeviceFacts struct {
	nodeSelector map[string]string
	powerModes   []string
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
	if len(deviceClass.Spec.PowerModes) != 1 || powerMode != fixedPowerMode ||
		selectedMode.Actuator.Kind != edgev1alpha1.PowerModeActuatorFixed {
		return selectedDeviceFacts{}, fmt.Errorf(
			"DeviceClass %q is not the first-path Fixed actuator class", deviceClass.Name,
		)
	}
	selector := make(map[string]string, len(deviceClass.Spec.NodeSelector))
	for key, value := range deviceClass.Spec.NodeSelector {
		selector[key] = value
	}
	return selectedDeviceFacts{nodeSelector: selector, powerModes: []string{fixedPowerMode}}, nil
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
	return *selected, artifact, nil
}

func validateObjectIdentity(kind, name string, uid types.UID, resourceVersion string) error {
	if strings.TrimSpace(name) == "" || uid == "" || strings.TrimSpace(resourceVersion) == "" {
		return fmt.Errorf("%s has incomplete API identity", kind)
	}
	return nil
}
