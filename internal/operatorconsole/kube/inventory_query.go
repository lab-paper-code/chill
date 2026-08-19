package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// KubernetesInventoryQuery reads inventory authorities without caching or
// persisting a duplicate status surface.
type KubernetesInventoryQuery struct {
	reader client.Reader
	now    func() time.Time
}

func NewKubernetesInventoryQuery(reader client.Reader) (*KubernetesInventoryQuery, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes inventory reader is required")
	}
	return &KubernetesInventoryQuery{reader: reader, now: time.Now}, nil
}

// Read lists DeviceClasses, live Nodes, and declared ModelSpecs. It applies
// Kubernetes label selector semantics only to the class-to-Node projection;
// it does not infer model compatibility.
func (q *KubernetesInventoryQuery) Read(ctx context.Context) (operatorconsole.InventorySnapshot, error) {
	classes := &edgev1alpha1.DeviceClassList{}
	if err := q.reader.List(ctx, classes); err != nil {
		return operatorconsole.InventorySnapshot{}, fmt.Errorf("list DeviceClasses for inventory: %w", err)
	}

	nodes := &corev1.NodeList{}
	if err := q.reader.List(ctx, nodes); err != nil {
		return operatorconsole.InventorySnapshot{}, fmt.Errorf("list Nodes for inventory: %w", err)
	}

	models := &edgev1alpha1.ModelSpecList{}
	if err := q.reader.List(ctx, models); err != nil {
		return operatorconsole.InventorySnapshot{}, fmt.Errorf("list ModelSpecs for inventory: %w", err)
	}

	projected, err := deviceclass.ProjectInventory(classes.Items, nodes.Items)
	if err != nil {
		return operatorconsole.InventorySnapshot{}, fmt.Errorf("project inventory: %w", err)
	}
	sort.Slice(models.Items, func(i, j int) bool { return models.Items[i].Name < models.Items[j].Name })

	return operatorconsole.InventorySnapshot{
		ReadAt:                       q.now().UTC(),
		DeviceClassesResourceVersion: classes.ResourceVersion,
		NodesResourceVersion:         nodes.ResourceVersion,
		ModelSpecsResourceVersion:    models.ResourceVersion,
		Classes:                      projected,
		Models:                       models.Items,
	}, nil
}
