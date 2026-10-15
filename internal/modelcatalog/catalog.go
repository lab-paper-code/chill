// Package modelcatalog validates public model artifact declarations and selects execution paths.
package modelcatalog

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
)

const (
	runtimeBackendRKNNLite = "rknnlite"
	artifactFormatRKNN     = "rknn"
)

var artifactPath = regexp.MustCompile(`^/[A-Za-z0-9._/-]+$`)

// InputTensors returns a detached domain contract for an artifact declaration.
func InputTensors(input *edgev1alpha1.ModelArtifactInputContract) []modelcompat.InputTensor {
	if input == nil {
		return nil
	}
	tensors := make([]modelcompat.InputTensor, len(input.Tensors))
	for index, tensor := range input.Tensors {
		tensors[index] = modelcompat.InputTensor{
			Name: tensor.Name, Datatype: tensor.Datatype,
			SampleShape: append([]int32(nil), tensor.SampleShape...),
		}
	}
	return tensors
}

// FirstfloorSettings projects selected API dimensions with the keep deadline default.
// Callers validate/select the execution path first.
func FirstfloorSettings(runtime edgev1alpha1.ModelRuntimeRequirement) *modelcompat.FirstfloorSettings {
	if runtime.Settings == nil || runtime.Settings.Firstfloor == nil {
		return nil
	}
	settings := runtime.Settings.Firstfloor
	return &modelcompat.FirstfloorSettings{
		MaxBatchSize:   settings.MaxBatchSize,
		MaxQueueSize:   settings.MaxQueueSize,
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(settings.DeadlinePolicy),
	}
}

// Validate checks every artifact declaration and execution-path reference.
func Validate(model *edgev1alpha1.ModelSpec) error {
	if model == nil {
		return errors.New("ModelSpec is required")
	}
	artifacts := make(map[string]edgev1alpha1.ModelArtifact, len(model.Spec.Artifacts))
	for _, artifact := range model.Spec.Artifacts {
		if _, duplicate := artifacts[artifact.Name]; duplicate {
			return fmt.Errorf("ModelSpec contains duplicate artifact %q", artifact.Name)
		}
		artifacts[artifact.Name] = artifact
	}
	for _, artifact := range model.Spec.Artifacts {
		if err := validateSource(artifact.Source); err != nil {
			return fmt.Errorf("artifact %q source: %w", artifact.Name, err)
		}
		if artifact.Format == artifactFormatRKNN {
			if artifact.Input == nil {
				return fmt.Errorf("RKNN artifact %q requires an input tensor contract", artifact.Name)
			}
			tensors := make([]modelcompat.InputTensor, len(artifact.Input.Tensors))
			for index, tensor := range artifact.Input.Tensors {
				tensors[index] = modelcompat.InputTensor(tensor)
			}
			if err := modelcompat.ValidateInputTensors(tensors); err != nil {
				return fmt.Errorf("artifact %q input tensor contract: %w", artifact.Name, err)
			}
		} else if artifact.Input != nil {
			return fmt.Errorf("artifact %q declares input; preprocessor artifacts define %s inputs", artifact.Name, artifact.Format)
		}
		if artifact.Derivation != nil {
			if artifact.Derivation.SourceArtifact == artifact.Name {
				return fmt.Errorf("derived artifact %q references itself as source", artifact.Name)
			}
			if _, exists := artifacts[artifact.Derivation.SourceArtifact]; !exists {
				return fmt.Errorf("derived artifact %q references missing source artifact %q", artifact.Name, artifact.Derivation.SourceArtifact)
			}
		}
	}
	seenPaths := make(map[string]struct{}, len(model.Spec.ExecutionPaths))
	for _, executionPath := range model.Spec.ExecutionPaths {
		if _, duplicate := seenPaths[executionPath.Name]; duplicate {
			return fmt.Errorf("ModelSpec contains duplicate execution path %q", executionPath.Name)
		}
		seenPaths[executionPath.Name] = struct{}{}
		artifact, exists := artifacts[executionPath.Artifact]
		if !exists {
			return fmt.Errorf("execution path %q references missing artifact %q", executionPath.Name, executionPath.Artifact)
		}
		if err := validateExecutionRuntime(executionPath.Runtime, artifact); err != nil {
			return err
		}
	}
	return nil
}

func validateExecutionRuntime(runtime edgev1alpha1.ModelRuntimeRequirement, artifact edgev1alpha1.ModelArtifact) error {
	if runtime.Family != "firstfloor" {
		return errors.New("execution path runtime family must be firstfloor")
	}
	if !((artifact.Format == "onnx" && (runtime.Backend == "ort-cpu" || runtime.Backend == "ort-cuda")) ||
		(artifact.Format == artifactFormatRKNN && runtime.Backend == runtimeBackendRKNNLite)) {
		return errors.New("firstfloor requires ONNX with ort-cpu/ort-cuda or RKNN with rknnlite")
	}
	if runtime.Backend == runtimeBackendRKNNLite && (artifact.Derivation == nil ||
		artifact.Derivation.TargetPlatform == "" || len(artifact.Derivation.DeclaredBatchSizes) == 0) {
		return errors.New("RKNNLite artifact requires RKNN format, target platform, inputs, and declared batch sizes")
	}
	if runtime.Backend == runtimeBackendRKNNLite && (len(artifact.Input.Tensors) != 1 || artifact.Input.Tensors[0].Datatype != "FP32" ||
		len(artifact.Input.Tensors[0].SampleShape) != 3 || artifact.Derivation.TargetPlatform != "rk3588") {
		return errors.New("RKNNLite requires one FP32 rank-3 NHWC sample input for rk3588")
	}
	return modelcompat.ValidateFirstfloorRuntimeSettings(FirstfloorSettings(runtime))
}

// SelectModelPath selects a path only after validating the complete declaration.
func SelectModelPath(model *edgev1alpha1.ModelSpec, pathName string) (edgev1alpha1.ModelExecutionPath, edgev1alpha1.ModelArtifact, error) {
	if err := Validate(model); err != nil {
		return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, err
	}
	for _, executionPath := range model.Spec.ExecutionPaths {
		if executionPath.Name != pathName {
			continue
		}
		for _, artifact := range model.Spec.Artifacts {
			if artifact.Name == executionPath.Artifact {
				return executionPath, artifact, nil
			}
		}
	}
	return edgev1alpha1.ModelExecutionPath{}, edgev1alpha1.ModelArtifact{}, fmt.Errorf("execution path %q is not declared by ModelSpec %q", pathName, model.Name)
}

func validateSource(source edgev1alpha1.ModelArtifactSource) error {
	for _, field := range []struct{ name, value string }{
		{"image", source.Image}, {"architecture", source.Architecture},
	} {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("%s is required without surrounding whitespace", field.name)
		}
	}
	if len(source.Files) == 0 {
		return errors.New("files requires at least one model file")
	}
	basenames := make(map[string]struct{}, len(source.Files))
	for _, file := range source.Files {
		if !artifactPath.MatchString(file) || path.Clean(file) != file {
			return fmt.Errorf("file %q must be a clean absolute container path", file)
		}
		base := path.Base(file)
		if _, duplicate := basenames[base]; duplicate {
			return fmt.Errorf("files contain duplicate basename %q", base)
		}
		basenames[base] = struct{}{}
	}
	return nil
}
