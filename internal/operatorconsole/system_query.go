package operatorconsole

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SystemSnapshot is one read-through view of the authoritative ChillSystem
// resources. It preserves the LIST resource version and does not derive a
// separate health state from the stored spec and conditions.
type SystemSnapshot struct {
	ReadAt          time.Time
	ResourceVersion string
	Systems         []edgev1alpha1.ChillSystem
}

// KubernetesSystemQuery reads ChillSystem resources without caching or
// persisting a console-owned status surface.
type KubernetesSystemQuery struct {
	reader client.Reader
	now    func() time.Time
}

func NewKubernetesSystemQuery(reader client.Reader) (*KubernetesSystemQuery, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes system reader is required")
	}
	return &KubernetesSystemQuery{reader: reader, now: time.Now}, nil
}

func (q *KubernetesSystemQuery) Read(ctx context.Context) (SystemSnapshot, error) {
	systems := &edgev1alpha1.ChillSystemList{}
	if err := q.reader.List(ctx, systems); err != nil {
		return SystemSnapshot{}, fmt.Errorf("list ChillSystems: %w", err)
	}

	sort.Slice(systems.Items, func(i, j int) bool {
		return systems.Items[i].Name < systems.Items[j].Name
	})
	return SystemSnapshot{
		ReadAt:          q.now().UTC(),
		ResourceVersion: systems.ResourceVersion,
		Systems:         systems.Items,
	}, nil
}
