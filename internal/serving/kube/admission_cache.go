package kube

import (
	"context"
	"fmt"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// measurementCache is owned by tickMu. Every round reads current metadata;
// unchanged UID/resourceVersion pairs reuse their decoded payload and judgment.
// No asynchronous delivery or resourceVersion ordering is used as a safety proof.
type measurementCache struct {
	namespace  string
	object     client.Object
	entries    map[client.ObjectKey]measurementPayload
	generation uint64
}

type measurementPayload struct {
	root       measurementRoot
	isRoot     bool
	generation uint64
}

func measurementCaches(namespace string) []*measurementCache {
	return []*measurementCache{
		{namespace: namespace, object: &edge.BatchEnergyOptimization{}},
		{namespace: namespace, object: &edge.ModelLoadedIdleBaseline{}},
		{namespace: namespace, object: &edge.ProfilingRun{}},
		{namespace: namespace, object: &edge.PowerModeTransitionCost{}},
		{namespace: namespace, object: &edge.OperationCostMeasurement{}},
		{namespace: namespace, object: &edge.DispatchOccupancyMeasurement{}},
	}
}

func measurementObjectRoot(object client.Object) (measurementRoot, bool) {
	root := measurementRoot{object: object}
	switch o := object.(type) {
	case *edge.BatchEnergyOptimization:
		root.node = o.Spec.State.TargetNode
		root.terminal = o.Status.Terminal(o.Generation)
	case *edge.ModelLoadedIdleBaseline:
		root.node = o.Spec.State.TargetNode
		root.terminal = o.Status.Phase == edge.ModelLoadedIdleBaselinePhaseComplete || o.Status.Phase == edge.ModelLoadedIdleBaselinePhaseInsufficient
	case *edge.ProfilingRun:
		if owner := meta.GetControllerOf(o); owner != nil && (owner.Kind == "BatchEnergyOptimization" || owner.Kind == "ModelLoadedIdleBaseline") {
			return root, false
		}
		root.node = o.Spec.TargetNode
		for _, condition := range o.Status.Conditions {
			root.terminal = root.terminal || condition.Type == edge.ProfilingRunConditionExecutionComplete && condition.Status != meta.ConditionUnknown
		}
	case *edge.PowerModeTransitionCost:
		root.node = o.Spec.TargetNode
		root.terminal = o.Status.Phase == edge.PowerModeTransitionCostPhaseComplete || o.Status.Phase == edge.PowerModeTransitionCostPhaseFailed
	case *edge.OperationCostMeasurement:
		root.node, root.terminal = o.Spec.Node, measurementTerminal(o.Status.Phase)
	case *edge.DispatchOccupancyMeasurement:
		root.node, root.terminal = o.Spec.Node, measurementTerminal(o.Status.Phase)
	default:
		return root, false
	}
	return root, true
}

func (s *measurementCache) refresh(ctx context.Context, api client.Client) error {
	gvk, err := api.GroupVersionKindFor(s.object)
	if err != nil {
		return err
	}
	gvk.Kind += "List"
	if s.entries == nil {
		// Seed once in one request; historical terminal CRs must not turn a
		// restart into thousands of rate-limited GETs. Metadata below still
		// proves the current membership/versions after this cold baseline.
		object, err := api.Scheme().New(gvk)
		if err != nil {
			return err
		}
		full := object.(client.ObjectList)
		if err := api.List(ctx, full, client.InNamespace(s.namespace)); err != nil {
			return err
		}
		objects, err := apimeta.ExtractListWithAlloc(full)
		if err != nil {
			return err
		}
		s.entries = make(map[client.ObjectKey]measurementPayload, len(objects))
		for _, object := range objects {
			value := object.(client.Object)
			root, isRoot := measurementObjectRoot(value)
			s.entries[client.ObjectKeyFromObject(value)] = measurementPayload{root: root, isRoot: isRoot}
		}
	}
	list := &meta.PartialObjectMetadataList{}
	list.SetGroupVersionKind(gvk)
	// This direct, metadata-only baseline proves membership and exact versions
	// across kinds before Admit sorts waiting roots. All I/O uses the round ctx.
	if err := api.List(ctx, list, client.InNamespace(s.namespace)); err != nil {
		return err
	}
	s.generation++
	for i := range list.Items {
		identity := &list.Items[i]
		key := client.ObjectKeyFromObject(identity)
		cached, found := s.entries[key]
		if !found || cached.root.object.GetUID() != identity.UID || cached.root.object.GetResourceVersion() != identity.ResourceVersion {
			object := s.object.DeepCopyObject().(client.Object)
			if err := api.Get(ctx, key, object); err != nil {
				if apierrors.IsNotFound(err) {
					delete(s.entries, key)
					continue
				}
				return err
			}
			cached.root, cached.isRoot = measurementObjectRoot(object)
		}
		cached.generation = s.generation
		s.entries[key] = cached
	}
	for key, cached := range s.entries {
		if cached.generation != s.generation {
			delete(s.entries, key)
		}
	}
	return nil
}

// measurementRoots obtains one current metadata baseline per kind and reuses
// payloads only when both physical identity and opaque resourceVersion match.
// No partial baseline is used after an error. Deletions and name reuse are
// observed without waiting for watch delivery or bookmarks.
func (c *SchedulerController) measurementRoots(ctx context.Context, namespaces []string, nodes []core.Node) ([]measurementRoot, error) {
	selected := map[string]bool{}
	for _, namespace := range namespaces {
		selected[namespace] = true
	}
	var caches []*measurementCache
	existing := map[string]bool{}
	for _, cache := range c.measurements {
		if selected[cache.namespace] {
			caches = append(caches, cache)
			existing[cache.namespace] = true
		}
	}
	for namespace := range selected {
		if !existing[namespace] {
			caches = append(caches, measurementCaches(namespace)...)
		}
	}
	c.measurements = caches
	for _, cache := range caches {
		if err := cache.refresh(ctx, c.store.client); err != nil {
			return nil, fmt.Errorf("observe measurements in %s: %w", cache.namespace, err)
		}
	}
	holders := map[string]bool{}
	for i := range nodes {
		if holder, tainted := profilingrun.MeasurementHolder(&nodes[i]); tainted {
			holders[holder] = true
		}
	}
	var roots []measurementRoot
	references := map[client.ObjectKey]bool{}
	present := map[client.ObjectKey]bool{}
	for _, cache := range caches {
		for _, cached := range cache.entries {
			root := cached.root
			if !cached.isRoot || root.terminal && !holders[string(root.object.GetUID())] {
				continue
			}
			// Admission patches and asynchronous runs must not mutate cached payloads.
			root.object = root.object.DeepCopyObject().(client.Object)
			roots = append(roots, root)
			switch object := root.object.(type) {
			case *edge.BatchEnergyOptimization:
				present[client.ObjectKeyFromObject(object)] = true
			case *edge.OperationCostMeasurement:
				for _, step := range object.Spec.Steps {
					if step.BatchEnergyOptimization != "" {
						references[client.ObjectKey{Namespace: object.Namespace, Name: step.BatchEnergyOptimization}] = true
					}
				}
			case *edge.DispatchOccupancyMeasurement:
				if object.Spec.BatchEnergyOptimization != "" {
					references[client.ObjectKey{Namespace: object.Namespace, Name: object.Spec.BatchEnergyOptimization}] = true
				}
			}
		}
	}
	// Completed BEOs remain profile evidence for waiting measurements, even when
	// they no longer need admission or hand-back themselves.
	for _, cache := range caches {
		if _, beo := cache.object.(*edge.BatchEnergyOptimization); !beo {
			continue
		}
		for key := range references {
			if cached, ok := cache.entries[key]; ok && !present[key] {
				root := cached.root
				root.object = root.object.DeepCopyObject().(client.Object)
				roots = append(roots, root)
				present[key] = true
			}
		}
	}
	return roots, nil
}
