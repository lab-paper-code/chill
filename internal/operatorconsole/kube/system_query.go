package kube

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

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

func (q *KubernetesSystemQuery) Read(ctx context.Context) (operatorconsole.SystemSnapshot, error) {
	systems := &edgev1alpha1.ChillSystemList{}
	if err := q.reader.List(ctx, systems); err != nil {
		return operatorconsole.SystemSnapshot{}, fmt.Errorf("list ChillSystems: %w", err)
	}

	sort.Slice(systems.Items, func(i, j int) bool {
		return systems.Items[i].Name < systems.Items[j].Name
	})
	return operatorconsole.SystemSnapshot{
		ReadAt:          q.now().UTC(),
		ResourceVersion: systems.ResourceVersion,
		Systems:         systems.Items,
	}, nil
}
