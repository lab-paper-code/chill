package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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
	declaration, err := selectExecutionDeclaration(configMap, r.options.ConfigMapKey, selection)
	if err != nil {
		return profiler.ResolvedCandidate{}, err
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

	modelDigest, err := catalogContentDigest(model.Name, string(model.UID), model.Generation, model.Spec)
	if err != nil {
		return profiler.ResolvedCandidate{}, fmt.Errorf("digest ModelSpec content: %w", err)
	}
	deviceDigest, err := catalogContentDigest(
		deviceClass.Name, string(deviceClass.UID), deviceClass.Generation, deviceClass.Spec,
	)
	if err != nil {
		return profiler.ResolvedCandidate{}, fmt.Errorf("digest DeviceClass content: %w", err)
	}
	if declaration.Artifact.Architecture != deviceClass.Spec.Architecture {
		return profiler.ResolvedCandidate{}, errors.New("artifact supply architecture does not match DeviceClass")
	}
	if declaration.Artifact.Digest != artifact.Digest {
		return profiler.ResolvedCandidate{}, errors.New("artifact supply digest does not match ModelSpec artifact")
	}
	reportBytes, err := buildCandidateReport(candidateReportInput{
		Model: model, DeviceClass: deviceClass, ConfigMap: configMap,
		ConfigMapKey: r.options.ConfigMapKey, ModelDigest: modelDigest,
		DeviceDigest: deviceDigest, DeclarationDigest: declaration.RuntimeDeclarationDigest,
		Path: modelPath, Artifact: artifact, PowerMode: selection.PowerMode,
		DeviceFacts: deviceFacts, Declaration: declaration, Result: result,
	})
	if err != nil {
		return profiler.ResolvedCandidate{}, err
	}

	return profiler.ResolvedCandidate{
		ReportBytes:                     reportBytes,
		ModelSpecContentDigest:          modelDigest,
		DeviceClassContentDigest:        deviceDigest,
		RuntimeDeclarationContentDigest: declaration.RuntimeDeclarationDigest,
		Verdict:                         string(result.Verdict),
		State: profilingrun.ExecutionState{
			DeviceClass: selection.DeviceClass,
			Model:       selection.ModelSpec, Artifact: artifact.Name, ArtifactDigest: artifact.Digest,
			ArtifactPath:  declaration.Artifact.Path,
			RuntimeFamily: modelPath.Runtime.Family, Backend: modelPath.Runtime.Backend,
			PowerMode: selection.PowerMode,
		},
		DeviceArchitecture:   deviceClass.Spec.Architecture,
		DeviceNodeSelector:   deviceFacts.nodeSelector,
		DevicePowerModes:     deviceFacts.powerModes,
		RuntimeImage:         declaration.Runtime.Image,
		ArtifactImage:        declaration.Artifact.Image,
		ArtifactArchitecture: declaration.Artifact.Architecture,
		ArtifactPath:         declaration.Artifact.Path,
		ArtifactDigest:       declaration.Artifact.Digest,
		PowerObserverImage:   declaration.PowerObserverImage,
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
