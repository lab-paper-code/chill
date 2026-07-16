package controller

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesNodeResolver performs exact reads for the selected Node. Its
// reader should be the manager's uncached API reader when wired by cmd/profiler.
type KubernetesNodeResolver struct {
	reader client.Reader
}

var _ profiler.NodeResolver = (*KubernetesNodeResolver)(nil)

func NewKubernetesNodeResolver(reader client.Reader) (*KubernetesNodeResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes Node reader is required")
	}
	return &KubernetesNodeResolver{reader: reader}, nil
}

func (r *KubernetesNodeResolver) ResolveNode(
	ctx context.Context,
	selection profiler.NodeSelection,
) (profilingrun.NodeSnapshot, error) {
	selector, err := validateNodeSelection(selection)
	if err != nil {
		return profilingrun.NodeSnapshot{}, err
	}

	node := &corev1.Node{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: selection.Name}, node); err != nil {
		return profilingrun.NodeSnapshot{}, fmt.Errorf("get selected Node %q: %w", selection.Name, err)
	}
	if !selector.Matches(labels.Set(node.Labels)) {
		return profilingrun.NodeSnapshot{}, fmt.Errorf(
			"selected Node %q does not match resolved DeviceClass selector", selection.Name,
		)
	}
	architecture := node.Labels[corev1.LabelArchStable]
	if architecture != selection.DeviceArchitecture {
		return profilingrun.NodeSnapshot{}, fmt.Errorf(
			"selected Node %q architecture %q does not match resolved DeviceClass architecture %q",
			selection.Name, architecture, selection.DeviceArchitecture,
		)
	}
	if node.UID == "" || node.ResourceVersion == "" {
		return profilingrun.NodeSnapshot{}, fmt.Errorf("selected Node %q has incomplete API identity", selection.Name)
	}
	allocatableCPU := node.Status.Allocatable.Cpu()
	if allocatableCPU == nil || allocatableCPU.Sign() <= 0 {
		return profilingrun.NodeSnapshot{}, fmt.Errorf(
			"selected Node %q has no positive allocatable CPU", selection.Name,
		)
	}

	return profilingrun.NodeSnapshot{
		Name:            node.Name,
		UID:             string(node.UID),
		ResourceVersion: node.ResourceVersion,
		Architecture:    architecture,
		AllocatableCPU:  allocatableCPU.String(),
	}, nil
}

func validateNodeSelection(selection profiler.NodeSelection) (labels.Selector, error) {
	if strings.TrimSpace(selection.Name) == "" || strings.TrimSpace(selection.Name) != selection.Name {
		return nil, errors.New("selected Node name is required without surrounding whitespace")
	}
	if strings.TrimSpace(selection.DeviceArchitecture) == "" ||
		strings.TrimSpace(selection.DeviceArchitecture) != selection.DeviceArchitecture {
		return nil, errors.New("resolved DeviceClass architecture is required without surrounding whitespace")
	}
	if len(selection.DeviceClassSelector) == 0 {
		return nil, errors.New("resolved DeviceClass selector is required")
	}
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: selection.DeviceClassSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("validate resolved DeviceClass selector: %w", err)
	}
	return selector, nil
}
