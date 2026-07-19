package operatorconsole

import (
	"context"
	"errors"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// InventorySnapshot is one read-through projection from two authoritative
// Kubernetes LIST responses. Their resource versions are kept separately;
// this type does not claim an atomic cross-resource snapshot.
type InventorySnapshot struct {
	ReadAt                       time.Time
	DeviceClassesResourceVersion string
	NodesResourceVersion         string
	Classes                      []deviceclass.InventoryClass
}

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

// Read lists all DeviceClasses and live Nodes, then applies Kubernetes label
// selector semantics through the deviceclass projection.
func (q *KubernetesInventoryQuery) Read(ctx context.Context) (InventorySnapshot, error) {
	classes := &edgev1alpha1.DeviceClassList{}
	if err := q.reader.List(ctx, classes); err != nil {
		return InventorySnapshot{}, fmt.Errorf("list DeviceClasses for inventory: %w", err)
	}

	nodes := &corev1.NodeList{}
	if err := q.reader.List(ctx, nodes); err != nil {
		return InventorySnapshot{}, fmt.Errorf("list Nodes for inventory: %w", err)
	}

	projected, err := deviceclass.ProjectInventory(classes.Items, nodes.Items)
	if err != nil {
		return InventorySnapshot{}, fmt.Errorf("project inventory: %w", err)
	}

	return InventorySnapshot{
		ReadAt:                       q.now().UTC(),
		DeviceClassesResourceVersion: classes.ResourceVersion,
		NodesResourceVersion:         nodes.ResourceVersion,
		Classes:                      projected,
	}, nil
}
