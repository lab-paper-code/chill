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

// ProfilingRunsSnapshot is one namespaced or all-namespaces LIST response. An
// empty Namespace means all namespaces; it does not imply ChillSystem ownership.
type ProfilingRunsSnapshot struct {
	ReadAt          time.Time
	ResourceVersion string
	Namespace       string
	Runs            []edgev1alpha1.ProfilingRun
}

// KubernetesProfilingRunsQuery reads ProfilingRuns without caching or
// inferring an owning ChillSystem from their namespace.
type KubernetesProfilingRunsQuery struct {
	reader    client.Reader
	namespace string
	now       func() time.Time
}

func NewKubernetesProfilingRunsQuery(reader client.Reader, namespace string) (*KubernetesProfilingRunsQuery, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes ProfilingRun reader is required")
	}
	return &KubernetesProfilingRunsQuery{reader: reader, namespace: namespace, now: time.Now}, nil
}

func (q *KubernetesProfilingRunsQuery) Read(ctx context.Context) (ProfilingRunsSnapshot, error) {
	runs := &edgev1alpha1.ProfilingRunList{}
	options := make([]client.ListOption, 0, 1)
	if q.namespace != "" {
		options = append(options, client.InNamespace(q.namespace))
	}
	if err := q.reader.List(ctx, runs, options...); err != nil {
		return ProfilingRunsSnapshot{}, fmt.Errorf("list ProfilingRuns: %w", err)
	}

	sort.Slice(runs.Items, func(i, j int) bool {
		left := runs.Items[i].Namespace + "\x00" + runs.Items[i].Name
		right := runs.Items[j].Namespace + "\x00" + runs.Items[j].Name
		return left < right
	})
	return ProfilingRunsSnapshot{
		ReadAt: q.now().UTC(), ResourceVersion: runs.ResourceVersion,
		Namespace: q.namespace, Runs: runs.Items,
	}, nil
}
