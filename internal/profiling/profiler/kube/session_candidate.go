package kube

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"
)

func selectSessionModelPath(model *edge.ModelSpec, name string) (edge.ModelExecutionPath, edge.ModelArtifact, error) {
	var selected *edge.ModelExecutionPath
	for index := range model.Spec.ExecutionPaths {
		candidate := &model.Spec.ExecutionPaths[index]
		if candidate.Name != name {
			continue
		}
		if selected != nil {
			return edge.ModelExecutionPath{}, edge.ModelArtifact{}, fmt.Errorf("duplicate selected execution path %q", name)
		}
		selected = candidate
	}
	if selected == nil {
		return edge.ModelExecutionPath{}, edge.ModelArtifact{}, fmt.Errorf("execution path %q is not declared by ModelSpec %q", name, model.Name)
	}
	var artifact *edge.ModelArtifact
	for index := range model.Spec.Artifacts {
		candidate := &model.Spec.Artifacts[index]
		if candidate.Name != selected.Artifact {
			continue
		}
		if artifact != nil {
			return edge.ModelExecutionPath{}, edge.ModelArtifact{}, fmt.Errorf("duplicate selected artifact %q", selected.Artifact)
		}
		artifact = candidate
	}
	if artifact == nil {
		return edge.ModelExecutionPath{}, edge.ModelArtifact{}, fmt.Errorf("execution path %q references missing artifact %q", name, selected.Artifact)
	}
	if artifact.Source.Image == "" || artifact.Source.Architecture == "" || len(artifact.Source.Files) == 0 {
		return edge.ModelExecutionPath{}, edge.ModelArtifact{}, errors.New("selected artifact requires an image, architecture and model files")
	}
	for _, file := range artifact.Source.Files {
		if !path.IsAbs(file) {
			return edge.ModelExecutionPath{}, edge.ModelArtifact{}, fmt.Errorf("model file %q must be an absolute container path", file)
		}
	}
	if err := modelcompat.ValidateModelFileNames(artifact.Source.Files, artifact.Format == "rknn"); err != nil {
		return edge.ModelExecutionPath{}, edge.ModelArtifact{}, err
	}
	runtime := selected.Runtime
	if runtime.Family != profilingrun.FirstfloorRuntimeFamily ||
		!((artifact.Format == "onnx" && (runtime.Backend == profilingrun.FirstfloorORTCPUBackend || runtime.Backend == profilingrun.FirstfloorORTCUDABackend)) ||
			(artifact.Format == "rknn" && runtime.Backend == profilingrun.FirstfloorRKNNLiteBackend)) {
		return edge.ModelExecutionPath{}, edge.ModelArtifact{}, errors.New("selected artifact and execution backend are unsupported")
	}
	settings := modelcatalog.FirstfloorSettings(runtime)
	if settings == nil || settings.MaxBatchSize < 1 || settings.MaxQueueSize < 1 {
		return edge.ModelExecutionPath{}, edge.ModelArtifact{}, errors.New("selected firstfloor maxBatchSize and maxQueueSize must be positive")
	}
	return *selected, *artifact, nil
}

func selectSessionDevice(device *edge.DeviceClass, mode string) (selectedDeviceFacts, error) {
	if device.Spec.Architecture == "" || len(device.Spec.NodeSelector) == 0 {
		return selectedDeviceFacts{}, errors.New("DeviceClass architecture and nodeSelector are required")
	}
	for _, candidate := range device.Spec.PowerModes {
		if candidate.Name == mode {
			return selectedDeviceFacts{nodeSelector: device.Spec.NodeSelector}, nil
		}
	}
	return selectedDeviceFacts{}, fmt.Errorf("DeviceClass %q has no power mode %q", device.Name, mode)
}

func selectSessionDeclaration(config *corev1.ConfigMap, key, class, node, family, backend string) (resolvedExecutionDeclaration, error) {
	document, err := readSingleYAMLDocument([]byte(config.Data[key]))
	if err != nil {
		return resolvedExecutionDeclaration{}, fmt.Errorf("decode Profiler execution facts: %w", err)
	}
	var facts executionFactsDocument
	if err := yaml.Unmarshal(document, &facts); err != nil {
		return resolvedExecutionDeclaration{}, fmt.Errorf("decode Profiler execution facts: %w", err)
	}
	var selected resolvedExecutionDeclaration
	found := false
	var decodeErr error
	for _, source := range facts.Declarations {
		if source.DeviceClass != class || source.Node != "" && source.Node != node {
			continue
		}
		var runtime runtimeDeclaration
		if err := json.Unmarshal([]byte(source.Runtime.Payload), &runtime); err != nil {
			decodeErr = fmt.Errorf("decode runtime producer declaration: %w", err)
			continue
		}
		if runtime.Runtime.Family != family || !slices.Contains(runtime.Runtime.Backends, backend) {
			continue
		}
		selected = resolvedExecutionDeclaration{
			DeviceClass: class, Node: source.Node, Runtime: runtime, Host: source.Host, Access: source.Access,
		}
		found = true
	}
	if !found {
		if decodeErr != nil {
			return resolvedExecutionDeclaration{}, decodeErr
		}
		return resolvedExecutionDeclaration{}, fmt.Errorf("no execution declaration for %s on %s/%s/%s", node, class, family, backend)
	}
	if selected.Runtime.Image == "" || selected.Runtime.Architecture == "" {
		return resolvedExecutionDeclaration{}, errors.New("selected runtime image and architecture are required")
	}
	if selected.Access != nil && selected.Access.GPUUUID != "" && selected.Node == "" {
		return resolvedExecutionDeclaration{}, errors.New("a declaration carrying a GPU UUID requires the node it was read on")
	}
	if backend == profilingrun.FirstfloorORTCUDABackend &&
		(selected.Access == nil || selected.Access.GPUUUID == "" || len(selected.Access.Resources) != 1 ||
			selected.Access.Resources["nvidia.com/gpu"] != "1") {
		return resolvedExecutionDeclaration{}, errors.New("firstfloor CUDA declaration requires an explicit GPU UUID and exactly one nvidia.com/gpu resource")
	}
	return selected, nil
}
