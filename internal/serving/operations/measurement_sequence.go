package operations

import (
	context "context"
	errors "errors"
	fmt "fmt"
	time "time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	serving "github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PowerModeStart reads the Node at a measurement window's start and its
// power-mode generation, nil on a Node without an actuation agent.
func (o *Operations) PowerModeStart(ctx context.Context, ref serving.NodeReference) (*core.Node, *PowerModeGeneration, error) {
	node, err := o.Kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	generation, ok, err := chillmeta.NodePowerModeGeneration(node)
	if err != nil || !ok {
		return node, nil, err
	}
	return node, &PowerModeGeneration{Start: generation}, nil
}

// MeasurementIsolation is the final isolation witness: the Node still carries
// the measurement taint, the Workloads of its measured models are recorded,
// and the Node's power-mode generation advanced by exactly the changes the
// measurement made itself since the window's start generation.
func (o *Operations) MeasurementIsolation(ctx context.Context, expected serving.NodeReference, models map[string]bool, generation *PowerModeGeneration, changes int64) (MeasurementIsolation, error) {
	witness := MeasurementIsolation{Workloads: []serving.WorkloadReference{}}
	if err := o.checkRecovery(ctx, expected, ""); err != nil {
		return witness, err
	}
	node, err := o.Kube.CoreV1().Nodes().Get(ctx, expected.Name, meta.GetOptions{})
	if err != nil {
		return witness, err
	}
	witness.Node = serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	witness.Tainted = profilingrun.InMeasurement(node)
	var workloads edge.WorkloadList
	if err = o.Store.Client().List(ctx, &workloads); err != nil {
		return witness, err
	}
	for _, w := range workloads.Items {
		if models[w.Spec.ModelRef.Name] || w.Status.AppliedDefinition != nil && models[w.Status.AppliedDefinition.ModelRef.Name] {
			witness.Workloads = append(witness.Workloads, serving.WorkloadReference{Name: w.Name, UID: string(w.UID)})
		}
	}
	witness.CheckedAt = time.Now().UTC()
	// The taint keeps ChillServer off this Node, so Workloads of the same model are recorded, not refused.
	if witness.Node != expected || !witness.Tainted {
		return witness, errors.New("actual physical Node or taint violates measurement isolation")
	}
	if generation == nil {
		return witness, nil
	}
	end, ok, err := chillmeta.NodePowerModeGeneration(node)
	if err != nil {
		return witness, err
	}
	witness.PowerModeGeneration = &PowerModeGeneration{Start: generation.Start, End: end}
	if !ok || end-generation.Start != changes {
		return witness, fmt.Errorf("%s: power-mode generation %d to %d, expected %d changes", servingstate.PowerChangedReason, generation.Start, end, changes)
	}
	return witness, nil
}
