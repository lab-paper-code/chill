package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// KubernetesCandidateResolver performs exact reads of the three authorities
// required for one static candidate: ModelSpec, DeviceClass, and the trusted
// installation-time execution declaration.
type KubernetesCandidateResolver struct {
	reader  client.Reader
	options CandidateResolverOptions
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
		modelPath.Runtime.Family,
		modelPath.Runtime.Backend,
	)
	if err != nil {
		return profiler.ResolvedCandidate{}, err
	}
	if modelPath.Runtime.Family == profilingrun.FirstfloorRuntimeFamily &&
		modelPath.Runtime.Backend == profilingrun.FirstfloorRKNNLiteBackend {
		if declaration.Host == nil || declaration.Host.Platform == nil || declaration.Access == nil ||
			len(declaration.Runtime.Packages) != 1 || declaration.Runtime.Packages[0].Name != rknnLitePackage {
			return profiler.ResolvedCandidate{}, errors.New("RKNNLite requires declared host, platform, runtime access, and rknn-toolkit-lite2 package")
		}
	}

	result := modelcompat.Evaluate(modelcompat.Input{
		Artifact: modelcompat.ArtifactRequirement{Format: artifact.Format},
		Path: modelcompat.ExecutionPathRequirement{
			RuntimeFamily: modelPath.Runtime.Family,
			Backend:       modelPath.Runtime.Backend,
		},
		DeviceClass: modelcompat.DeviceClassInfo{Architecture: deviceClass.Spec.Architecture},
		Runtime: &modelcompat.RuntimeDeclaration{
			Trusted:      true,
			Architecture: declaration.Runtime.Architecture,
			Family:       declaration.Runtime.Runtime.Family,
			Backends:     append([]string(nil), declaration.Runtime.Runtime.Backends...),
		},
	})
	if result.Verdict != modelcompat.VerdictCompatible {
		return profiler.ResolvedCandidate{}, fmt.Errorf(
			"static candidate verdict is %s: %s", result.Verdict, compatibilityReasonCodes(result),
		)
	}

	if artifact.Source.Architecture != deviceClass.Spec.Architecture {
		return profiler.ResolvedCandidate{}, errors.New("artifact supply architecture does not match DeviceClass")
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
			ArtifactPath:  artifact.Source.Path,
			RuntimeFamily: modelPath.Runtime.Family, Backend: modelPath.Runtime.Backend,
			PowerMode: selection.PowerMode, HostContract: reportHostContract(declaration.Host),
			Firstfloor: modelcatalog.FirstfloorSettings(modelPath.Runtime),
		},
		DeviceArchitecture:   deviceClass.Spec.Architecture,
		DeviceNodeSelector:   deviceFacts.nodeSelector,
		DevicePowerModes:     deviceFacts.powerModes,
		SelectedActuator:     cloneActuatorTarget(deviceFacts.selectedActuator),
		RuntimeImage:         declaration.Runtime.Image,
		ArtifactImage:        artifact.Source.Image,
		ArtifactArchitecture: artifact.Source.Architecture,
		ArtifactPath:         artifact.Source.Path,
		MetricObserverImage:  declaration.MetricObserverImage,
	}, nil
}

func validateCandidateSelection(selection profiler.CandidateSelection) error {
	for name, value := range map[string]string{
		"ModelSpec": selection.ModelSpec, "DeviceClass": selection.DeviceClass,
		"execution path": selection.ExecutionPath, "power mode": selection.PowerMode,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("candidate %s is required without surrounding whitespace", name)
		}
	}
	return nil
}
