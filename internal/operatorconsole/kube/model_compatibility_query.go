package kube

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/validation"
)

// The composition root supplies the existing Profiler fact resolver without
// introducing an Operator-to-Profiler runtime dependency.
type KubernetesModelCompatibilityQuery struct {
	resolve func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error)
	now     func() time.Time
}

func NewKubernetesModelCompatibilityQuery(
	resolve func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error),
) (*KubernetesModelCompatibilityQuery, error) {
	if resolve == nil {
		return nil, errors.New("a static model compatibility resolver is required")
	}
	return &KubernetesModelCompatibilityQuery{resolve: resolve, now: time.Now}, nil
}

func (q *KubernetesModelCompatibilityQuery) Read(
	ctx context.Context,
	selection operatorconsole.ModelCompatibilitySelection,
) (operatorconsole.ModelCompatibilitySnapshot, error) {
	for _, name := range []string{selection.ModelSpec, selection.DeviceClass, selection.Node} {
		if len(validation.IsDNS1123Subdomain(name)) != 0 {
			return operatorconsole.ModelCompatibilitySnapshot{}, apierrors.NewBadRequest("modelSpec, deviceClass and node must be Kubernetes object names")
		}
	}
	if selection.ExecutionPath == "" || strings.TrimSpace(selection.ExecutionPath) != selection.ExecutionPath {
		return operatorconsole.ModelCompatibilitySnapshot{}, apierrors.NewBadRequest("executionPath is required without surrounding whitespace")
	}
	snapshot, err := q.resolve(ctx, selection)
	if err != nil {
		return operatorconsole.ModelCompatibilitySnapshot{}, err
	}
	snapshot.ReadAt, snapshot.Selection = q.now().UTC(), selection
	return snapshot, nil
}
