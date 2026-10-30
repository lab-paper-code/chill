package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

type ModelCompatibilitySelection struct {
	ModelSpec, ExecutionPath, DeviceClass, Node string
}

type ModelCompatibilityReason struct {
	Kind    modelcompat.ReasonKind
	Code    modelcompat.ReasonCode
	Message string
}

// ModelCompatibility contains only identities and static support facts, never
// the runtime payload, host mounts, credentials, or an admission decision.
type ModelCompatibility struct {
	ModelSpec, DeviceClass, Node profilingrun.CandidateReportObjectInput
	RuntimeDeclaration           *profilingrun.CandidateReportRuntimeInput
	Verdict                      modelcompat.Verdict
	Reasons                      []ModelCompatibilityReason
}

// ResolveModelCompatibility reads the same installation-time fact source as
// ResolveCandidate. It does not require a power mode, dataset or preprocessor.
func (r *KubernetesCandidateResolver) ResolveModelCompatibility(
	ctx context.Context,
	selection ModelCompatibilitySelection,
) (ModelCompatibility, error) {
	model := &edgev1alpha1.ModelSpec{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.ModelSpec}, model); err != nil {
		return ModelCompatibility{}, fmt.Errorf("get ModelSpec %q: %w", selection.ModelSpec, err)
	}
	deviceClass := &edgev1alpha1.DeviceClass{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.DeviceClass}, deviceClass); err != nil {
		return ModelCompatibility{}, fmt.Errorf("get DeviceClass %q: %w", selection.DeviceClass, err)
	}
	node := &corev1.Node{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.Node}, node); err != nil {
		return ModelCompatibility{}, fmt.Errorf("get Node %q: %w", selection.Node, err)
	}
	modelPath, artifact, err := selectModelPath(model, selection.ExecutionPath)
	if err != nil {
		if !slices.ContainsFunc(model.Spec.ExecutionPaths, func(path edgev1alpha1.ModelExecutionPath) bool {
			return path.Name == selection.ExecutionPath
		}) {
			return ModelCompatibility{}, apierrors.NewBadRequest("executionPath is not declared by the selected ModelSpec")
		}
		return ModelCompatibility{}, err
	}
	if err := validateObjectIdentity("DeviceClass", deviceClass.Name, deviceClass.UID, deviceClass.ResourceVersion); err != nil {
		return ModelCompatibility{}, err
	}
	if err := validateObjectIdentity("Node", node.Name, node.UID, node.ResourceVersion); err != nil {
		return ModelCompatibility{}, err
	}
	if strings.TrimSpace(deviceClass.Spec.Architecture) == "" || len(deviceClass.Spec.NodeSelector) == 0 {
		return ModelCompatibility{}, errors.New("DeviceClass architecture and nodeSelector are required")
	}
	selector, err := labels.ValidatedSelectorFromSet(deviceClass.Spec.NodeSelector)
	if err != nil {
		return ModelCompatibility{}, fmt.Errorf("invalid DeviceClass nodeSelector: %w", err)
	}
	if !selector.Matches(labels.Set(node.Labels)) {
		return ModelCompatibility{}, apierrors.NewBadRequest("Node does not match the selected DeviceClass nodeSelector")
	}
	view := ModelCompatibility{
		ModelSpec:   profilingrun.CandidateReportObjectInput{Name: model.Name, UID: string(model.UID), ResourceVersion: model.ResourceVersion},
		DeviceClass: profilingrun.CandidateReportObjectInput{Name: deviceClass.Name, UID: string(deviceClass.UID), ResourceVersion: deviceClass.ResourceVersion},
		Node:        profilingrun.CandidateReportObjectInput{Name: node.Name, UID: string(node.UID), ResourceVersion: node.ResourceVersion},
		Verdict:     modelcompat.VerdictUnknown,
	}
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: r.options.ConfigMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		if !apierrors.IsNotFound(err) {
			return ModelCompatibility{}, fmt.Errorf("get Profiler fact-source ConfigMap %s: %w", key, err)
		}
		view.Reasons = []ModelCompatibilityReason{{Kind: modelcompat.ReasonKindUnknown,
			Code: modelcompat.ReasonRuntimeDeclarationMissing, Message: "Profiler fact-source ConfigMap is not present"}}
		return view, nil
	}
	if err := validateObjectIdentity("ConfigMap", configMap.Name, configMap.UID, configMap.ResourceVersion); err != nil {
		return ModelCompatibility{}, err
	}
	declaration, err := selectExecutionDeclaration(configMap, r.options.ConfigMapKey,
		selection.DeviceClass, selection.Node, modelPath.Runtime.Family, modelPath.Runtime.Backend)
	if err == nil {
		err = validateSelectedRuntime(modelPath, declaration)
	}
	if err != nil {
		code := "RuntimeDeclarationInvalid"
		var selectionError *declarationSelectionError
		if errors.As(err, &selectionError) {
			code = selectionError.code
		}
		view.Reasons = []ModelCompatibilityReason{{Kind: modelcompat.ReasonKindUnknown,
			Code: modelcompat.ReasonCode(code), Message: err.Error()}}
		return view, nil
	}
	view.RuntimeDeclaration = &profilingrun.CandidateReportRuntimeInput{
		Namespace: configMap.Namespace, Name: configMap.Name, UID: string(configMap.UID),
		ResourceVersion: configMap.ResourceVersion, Key: r.options.ConfigMapKey, Image: declaration.Runtime.Image,
	}
	result := evaluateStaticCandidate(modelPath, artifact, deviceClass, declaration)
	view.Verdict = result.Verdict
	for _, reason := range result.Reasons {
		view.Reasons = append(view.Reasons, ModelCompatibilityReason{
			Kind: reason.Kind, Code: reason.Code, Message: fmt.Sprintf("static runtime compatibility: %s", reason.Code),
		})
	}
	if artifact.Source.Architecture != deviceClass.Spec.Architecture {
		view.Verdict = modelcompat.VerdictIncompatible
		view.Reasons = append(view.Reasons, ModelCompatibilityReason{Kind: modelcompat.ReasonKindConflict,
			Code: "ArtifactArchitectureMismatch", Message: fmt.Sprintf("artifact architecture %q does not match DeviceClass architecture %q",
				artifact.Source.Architecture, deviceClass.Spec.Architecture)})
	}
	return view, nil
}
