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

// TritonSettings projects selected API dimensions without inventing defaults.
// Callers validate/select the execution path first.
func TritonSettings(runtime edgev1alpha1.ModelRuntimeRequirement) *modelcompat.TritonSettings {
	if runtime.Settings == nil || runtime.Settings.Triton == nil {
		return nil
	}
	settings := runtime.Settings.Triton
	return &modelcompat.TritonSettings{
		MaxBatchSize:   settings.MaxBatchSize,
		PriorityLevels: settings.PriorityLevels,
		MaxQueueSize:   settings.MaxQueueSize,
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
		var tensors []modelcompat.InputTensor
		if artifact.Input != nil {
			tensors = make([]modelcompat.InputTensor, len(artifact.Input.Tensors))
			for index, tensor := range artifact.Input.Tensors {
				tensors[index] = modelcompat.InputTensor(tensor)
			}
		}
		if err := modelcompat.ValidateInputTensors(tensors); err != nil {
			return fmt.Errorf("artifact %q input tensor contract: %w", artifact.Name, err)
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
		if executionPath.Runtime.Family == "rknnlite" &&
			(artifact.Derivation == nil || len(artifact.Derivation.DeclaredBatchSizes) == 0) {
			return errors.New("RKNNLite artifact requires a matching input tensor contract and declared batch sizes")
		}
		runtime := executionPath.Runtime
		if runtime.Family == "triton" {
			if runtime.Backend != "python" || artifact.Format != "onnx" ||
				runtime.Settings == nil || runtime.Settings.Triton == nil {
				return errors.New("Triton execution path requires ONNX, Python backend, and selected native settings")
			}
			native := runtime.Settings.Triton
			if native.MaxBatchSize < 1 || native.PriorityLevels < 1 || native.MaxQueueSize < 1 {
				return errors.New("Triton native settings must be positive int32 values")
			}
		} else if runtime.Settings != nil && runtime.Settings.Triton != nil {
			return errors.New("non-Triton execution path cannot contain Triton settings")
		}
	}
	return nil
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
		{"image", source.Image}, {"architecture", source.Architecture}, {"path", source.Path},
	} {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("%s is required without surrounding whitespace", field.name)
		}
	}
	if !artifactPath.MatchString(source.Path) || path.Clean(source.Path) != source.Path {
		return errors.New("path must be a clean absolute container path")
	}
	return nil
}
