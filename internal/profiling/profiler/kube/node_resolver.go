package kube

import (
	"context"
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
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
	var allocatableCPU string
	if value, present := node.Status.Allocatable[corev1.ResourceCPU]; present {
		allocatableCPU = value.String()
	}
	allocatableMemory := node.Status.Allocatable[corev1.ResourceMemory]

	return profilingrun.NodeSnapshot{
		Name:                   node.Name,
		UID:                    string(node.UID),
		ResourceVersion:        node.ResourceVersion,
		Architecture:           architecture,
		AllocatableCPU:         allocatableCPU,
		AllocatableMemoryBytes: allocatableMemory.Value(),
	}, nil
}

func validateNodeSelection(selection profiler.NodeSelection) (labels.Selector, error) {
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: selection.DeviceClassSelector,
	})
	if err != nil {
		return nil, fmt.Errorf("validate resolved DeviceClass selector: %w", err)
	}
	return selector, nil
}
