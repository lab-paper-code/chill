package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcatalog"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const rknnLitePackage = "rknn-toolkit-lite2"

type CandidateResolverOptions struct {
	Namespace     string
	ConfigMapName string
	ConfigMapKey  string
}

// KubernetesCandidateResolver performs exact reads of the authorities
// required for one static candidate: ModelSpec, DeviceClass, Preprocessor,
// the active measurement's Dataset, and the trusted installation-time
// execution declaration.
type KubernetesCandidateResolver struct {
	reader  client.Reader
	options CandidateResolverOptions
	declarationMu sync.Mutex
	declarationIdentity string
	declarations []resolvedExecutionDeclaration
	declarationErr error
}

var _ profiler.CandidateResolver = (*KubernetesCandidateResolver)(nil)

func NewKubernetesCandidateResolver(
	reader client.Reader,
	options CandidateResolverOptions,
) (*KubernetesCandidateResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes candidate fact reader is required")
	}
	for name, value := range map[string]string{
		"fact-source namespace":      options.Namespace,
		"fact-source ConfigMap name": options.ConfigMapName,
		"fact-source ConfigMap key":  options.ConfigMapKey,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesCandidateResolver{reader: reader, options: options}, nil
}

func (r *KubernetesCandidateResolver) ResolveCandidate(
	ctx context.Context,
	selection profiler.CandidateSelection,
) (profiler.ResolvedCandidate, error) {
	if err := validateCandidateSelection(selection); err != nil {
		return profiler.ResolvedCandidate{}, err
	}

	model := &edgev1alpha1.ModelSpec{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.ModelSpec}, model); err != nil {
		return profiler.ResolvedCandidate{}, fmt.Errorf("get ModelSpec %q: %w", selection.ModelSpec, err)
	}
	deviceClass := &edgev1alpha1.DeviceClass{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.DeviceClass}, deviceClass); err != nil {
		return profiler.ResolvedCandidate{}, fmt.Errorf("get DeviceClass %q: %w", selection.DeviceClass, err)
	}
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return profiler.ResolvedCandidate{}, fmt.Errorf("get Profiler fact-source ConfigMap %s: %w", key, err)
	}
	preprocessor := &edgev1alpha1.Preprocessor{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.Preprocessor}, preprocessor); err != nil {
		return profiler.ResolvedCandidate{}, fmt.Errorf("get Preprocessor %q: %w", selection.Preprocessor, err)
	}
	var dataset *edgev1alpha1.Dataset
	if selection.Dataset != "" {
		dataset = &edgev1alpha1.Dataset{}
		if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.Dataset}, dataset); err != nil {
			return profiler.ResolvedCandidate{}, fmt.Errorf("get Dataset %q: %w", selection.Dataset, err)
		}
	}

	modelPath, artifact, err := selectModelPath(model, selection.ExecutionPath)
	if err != nil {
		return profiler.ResolvedCandidate{}, err
	}
	deviceFacts, err := validateDeviceClass(deviceClass, selection.PowerMode)
	if err != nil {
		return profiler.ResolvedCandidate{}, err
	}
	declaration, err := selectExecutionDeclaration(
		configMap,
		r.options.ConfigMapKey,
		selection.DeviceClass,
		selection.Node,
		modelPath.Runtime.Family,
		modelPath.Runtime.Backend,
	)
	if err != nil {
		return profiler.ResolvedCandidate{}, err
	}
	if err := validateSelectedRuntime(modelPath, declaration); err != nil {
		return profiler.ResolvedCandidate{}, err
	}

	result := evaluateStaticCandidate(modelPath, artifact, deviceClass, declaration)
	if result.Verdict != modelcompat.VerdictCompatible {
		return profiler.ResolvedCandidate{}, fmt.Errorf(
			"static candidate verdict is %s: %s", result.Verdict, compatibilityReasonCodes(result),
		)
	}

	if artifact.Source.Architecture != deviceClass.Spec.Architecture {
		return profiler.ResolvedCandidate{}, errors.New("artifact supply architecture does not match DeviceClass")
	}
	preprocessorArtifact, ok := modelcatalog.PreprocessorArtifact(preprocessor, model.Name, artifact.Name)
	if !ok {
		return profiler.ResolvedCandidate{}, fmt.Errorf("Preprocessor %q has no artifact for ModelSpec %q artifact %q",
			preprocessor.Name, model.Name, artifact.Name)
	}
	var frozenDataset *profilingrun.DatasetContract
	if dataset != nil {
		if mismatched := modelcatalog.Compatible(dataset, preprocessor); len(mismatched) != 0 {
			return profiler.ResolvedCandidate{}, fmt.Errorf("Dataset %q records do not satisfy Preprocessor %q input fields %s",
				dataset.Name, preprocessor.Name, strings.Join(mismatched, ","))
		}
		frozenDataset = &profilingrun.DatasetContract{
			Name: dataset.Name, UID: string(dataset.UID),
			Image: dataset.Spec.Source.Image, Path: dataset.Spec.Source.Path,
		}
	}
	report, err := buildCandidateReport(candidateReportInput{
		Model: model, DeviceClass: deviceClass, ConfigMap: configMap,
		ConfigMapKey: r.options.ConfigMapKey,
		Path:         modelPath, Artifact: artifact, PowerMode: selection.PowerMode,
		DeviceFacts: deviceFacts, Declaration: declaration, Result: result,
	})
	if err != nil {
		return profiler.ResolvedCandidate{}, err
	}

	return profiler.ResolvedCandidate{
		ReportBytes: report.Bytes,
		Inputs: profilingrun.CandidateReportInputs{
			ModelSpec: profilingrun.CandidateReportObjectInput{
				Name: model.Name, UID: string(model.UID), ResourceVersion: model.ResourceVersion,
			},
			DeviceClass: profilingrun.CandidateReportObjectInput{
				Name: deviceClass.Name, UID: string(deviceClass.UID), ResourceVersion: deviceClass.ResourceVersion,
			},
			RuntimeDeclaration: profilingrun.CandidateReportRuntimeInput{
				Namespace: configMap.Namespace, Name: configMap.Name, UID: string(configMap.UID),
				ResourceVersion: configMap.ResourceVersion, Key: r.options.ConfigMapKey, Image: declaration.Runtime.Image,
			},
		},
		Verdict: string(result.Verdict),
		State: profilingrun.ExecutionState{
			DeviceClass: selection.DeviceClass,
			Model:       selection.ModelSpec, Artifact: artifact.Name,
			ArtifactFiles: slices.Clone(artifact.Source.Files),
			RuntimeFamily: modelPath.Runtime.Family, Backend: modelPath.Runtime.Backend,
			PowerMode: selection.PowerMode, HostContract: reportHostContract(declaration.Host),
			Firstfloor:   modelcatalog.FirstfloorSettings(modelPath.Runtime),
			Preprocessor: freezePreprocessor(preprocessor, preprocessorArtifact),
		},
		Dataset:              frozenDataset,
		DeviceArchitecture:   deviceClass.Spec.Architecture,
		DeviceNodeSelector:   deviceFacts.nodeSelector,
		DevicePowerModes:     deviceFacts.powerModes,
		SelectedActuator:     cloneActuatorTarget(deviceFacts.selectedActuator),
		RuntimeImage:         declaration.Runtime.Image,
		ArtifactImage:        artifact.Source.Image,
		ArtifactArchitecture: artifact.Source.Architecture,
		MetricObserverImage:  declaration.MetricObserverImage,
	}, nil
}

func freezePreprocessor(
	preprocessor *edgev1alpha1.Preprocessor,
	artifact edgev1alpha1.PreprocessorArtifact,
) profilingrun.PreprocessorState {
	state := profilingrun.PreprocessorState{
		Name: preprocessor.Name, UID: string(preprocessor.UID),
		Image: artifact.Source.Image, Architecture: artifact.Source.Architecture, Path: artifact.Source.Path,
		Inputs:  make([]string, len(preprocessor.Spec.Input.Fields)),
		Outputs: make([]string, len(preprocessor.Spec.Output.Fields)),
	}
	if preprocessor.Spec.MaxNewTokens != nil {
		state.MaxNewTokens = int(*preprocessor.Spec.MaxNewTokens)
	}
	for index, field := range preprocessor.Spec.Input.Fields {
		state.Inputs[index] = field.Name
	}
	for index, field := range preprocessor.Spec.Output.Fields {
		state.Outputs[index] = field.Name
	}
	return state
}

func validateCandidateSelection(selection profiler.CandidateSelection) error {
	for name, value := range map[string]string{
		"ModelSpec": selection.ModelSpec, "DeviceClass": selection.DeviceClass,
		"execution path": selection.ExecutionPath, "power mode": selection.PowerMode,
		"preprocessor": selection.Preprocessor, "Node": selection.Node,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("candidate %s is required without surrounding whitespace", name)
		}
	}
	return nil
}

// evaluateStaticCandidate is shared by preflight and the read-only support
// query. Admission's additional dataset, mode and host checks remain separate.
func evaluateStaticCandidate(
	modelPath edgev1alpha1.ModelExecutionPath,
	artifact edgev1alpha1.ModelArtifact,
	deviceClass *edgev1alpha1.DeviceClass,
	declaration resolvedExecutionDeclaration,
) modelcompat.Result {
	return modelcompat.Evaluate(modelcompat.Input{
		Artifact: modelcompat.ArtifactRequirement{Format: artifact.Format},
		Path: modelcompat.ExecutionPathRequirement{
			RuntimeFamily: modelPath.Runtime.Family, Backend: modelPath.Runtime.Backend,
		},
		DeviceClass: modelcompat.DeviceClassInfo{Architecture: deviceClass.Spec.Architecture},
		Runtime: &modelcompat.RuntimeDeclaration{
			Trusted: true, Architecture: declaration.Runtime.Architecture,
			Family: declaration.Runtime.Runtime.Family, Backends: declaration.Runtime.Runtime.Backends,
		},
	})
}

func validateSelectedRuntime(path edgev1alpha1.ModelExecutionPath, declaration resolvedExecutionDeclaration) error {
	if path.Runtime.Family == profilingrun.FirstfloorRuntimeFamily &&
		path.Runtime.Backend == profilingrun.FirstfloorRKNNLiteBackend &&
		(declaration.Host == nil || declaration.Host.Platform == nil || declaration.Access == nil ||
			len(declaration.Runtime.Packages) != 1 || declaration.Runtime.Packages[0].Name != rknnLitePackage) {
		return errors.New("RKNNLite requires declared host, platform, runtime access, and rknn-toolkit-lite2 package")
	}
	return nil
}
