package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operationCostKind         = "operation-cost-measurement"
	refusedProfileMissing     = "ProfileMissing"
	refusedNodeMismatch       = "NodeMismatch"
	refusedActuationNamespace = "ActuationNamespace"
	refusedPowerModeUnknown   = "PowerModeUnknown"
)

// measurementTerminal reports a terminal measurement phase.
func measurementTerminal(phase edge.MeasurementPhase) bool {
	return phase == edge.MeasurementPhaseComplete || phase == edge.MeasurementPhaseInsufficient || phase == edge.MeasurementPhaseFailed
}

// measurementProfile finds the catalog profile of a BatchEnergyOptimization a
// measurement names on its Node. wait is set while the catalog has neither
// the profile nor an issue of it yet.
func measurementProfile(namespace, name, node string, in admissionInputs) (profile *serving.Profile, reason, detail string, wait bool) {
	var beo *edge.BatchEnergyOptimization
	for _, r := range in.roots {
		if o, ok := r.object.(*edge.BatchEnergyOptimization); ok && o.Namespace == namespace && o.Name == name {
			beo = o
		}
	}
	switch {
	case beo == nil:
		return nil, refusedProfileMissing, name + " NotFound", false
	case beo.Status.Phase != catalogCompletePhase && beo.Status.Phase != campaignFailedPhase:
		return nil, refusedProfileMissing, name + " NotComplete", false
	case beo.Status.Phase != catalogCompletePhase:
		return nil, refusedProfileMissing, name + " " + beo.Status.Phase, false
	case beo.Spec.State.TargetNode != node:
		return nil, refusedNodeMismatch, name + " " + beo.Spec.State.TargetNode, false
	}
	source := types.NamespacedName{Namespace: namespace, Name: name}
	if i := slices.IndexFunc(in.config.CatalogIssues, func(issue CatalogIssue) bool { return issue.Source == source }); i >= 0 {
		return nil, refusedProfileMissing, name + " " + in.config.CatalogIssues[i].Code, false
	}
	id := source.String() + "/" + string(beo.UID)
	i := slices.IndexFunc(in.profiles, func(p serving.Profile) bool { return p.ID == id })
	if _, prepared := in.config.Preparations[id]; i < 0 || !prepared {
		return nil, "", "", true
	}
	return &in.profiles[i], "", "", false
}

// operationCostRefusal judges an operation-cost measurement before its grant.
// profiles maps each named BatchEnergyOptimization to its catalog profile ID.
func (c *SchedulerController) operationCostRefusal(m *edge.OperationCostMeasurement, node *core.Node, in admissionInputs) (profiles map[string]string, reason, detail string, wait bool) {
	if m.Namespace != c.observer.config.ActuationNamespace {
		return nil, refusedActuationNamespace, m.Namespace, false
	}
	if !slices.ContainsFunc(in.config.Meters, func(meter MeterDeclaration) bool { return meter.Node.UID == string(node.UID) }) {
		return nil, MeterUndeclared, node.Name, false
	}
	profiles = map[string]string{}
	targets := nodePowerTargets(node, in.classes, in.config.PowerPolicies)
	for _, step := range m.Spec.Steps {
		if step.Kind == edge.OperationCostStepPower {
			if _, ok := targets[step.PowerMode]; !ok {
				return nil, refusedPowerModeUnknown, step.PowerMode, false
			}
			continue
		}
		if step.BatchEnergyOptimization == "" {
			continue
		}
		profile, reason, detail, wait := measurementProfile(m.Namespace, step.BatchEnergyOptimization, m.Spec.Node, in)
		if reason != "" || wait {
			return nil, reason, detail, wait
		}
		profiles[step.BatchEnergyOptimization] = profile.ID
	}
	return profiles, "", "", false
}

// patchMeasurementStatus merges status into the measurement's status.
func (c *SchedulerController) patchMeasurementStatus(ctx context.Context, object client.Object, set func()) error {
	base := object.DeepCopyObject().(client.Object)
	set()
	return c.store.client.Status().Patch(ctx, object, client.MergeFrom(base))
}

// failMeasurement writes the Failed phase of a measurement kind ChillServer runs.
func (c *SchedulerController) failMeasurement(ctx context.Context, object client.Object, reason string) error {
	switch o := object.(type) {
	case *edge.OperationCostMeasurement:
		return c.patchMeasurementStatus(ctx, o, func() { o.Status.Phase, o.Status.Reason = edge.MeasurementPhaseFailed, reason })
	}
	return nil
}

// startOperationCost registers the run of a granted operation-cost measurement.
func (c *SchedulerController) startOperationCost(node *core.Node, m *edge.OperationCostMeasurement, profiles map[string]string, in admissionInputs) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	targets := nodePowerTargets(node, in.classes, in.config.PowerPolicies)
	root := &measurementRoot{object: m, node: node.Name}
	operation := Operation{ID: operationCostKind + ":" + node.Name + ":" + string(m.UID), Kind: operationCostKind, Node: ref}
	c.startNodeRun(operation, in.config, func(ctx context.Context, result *OperationResult) error {
		err := c.runOperationCost(ctx, m, ref, targets, profiles, in.config)
		return errors.Join(err, c.handBack(ctx, ref, string(m.UID), root, false, result))
	})
}

// runOperationCost measures the steps in order and writes the status; a
// sequence that stops early gets its sequence record.
func (c *SchedulerController) runOperationCost(ctx context.Context, m *edge.OperationCostMeasurement, ref serving.NodeReference, targets map[string]edge.NodeActuationSpec, profiles map[string]string, config SchedulerControllerConfig) error {
	if err := c.patchMeasurementStatus(ctx, m, func() { m.Status.Phase = edge.MeasurementPhaseMeasuring }); err != nil {
		return err
	}
	observation := m.Spec.Observation
	coverage, err := strconv.ParseFloat(observation.MinimumCoverage, 64)
	if err != nil {
		return err
	}
	interval := time.Duration(observation.IntervalMilliseconds) * time.Millisecond
	duration := time.Duration(observation.MaximumOperationSeconds) * time.Second
	base := MeasurementRequest{
		Sampling: metricobserver.Request{Interval: interval, Duration: duration,
			RequestTimeout: time.Duration(observation.RequestTimeoutMilliseconds) * time.Millisecond, MaxEvents: int(duration/interval) + 2},
		Coverage: powerwindow.Policy{MinimumCoverage: coverage,
			MaximumBoundaryDistance: time.Duration(observation.MaximumBoundaryDistanceMilliseconds) * time.Millisecond,
			MaximumGap:              time.Duration(observation.MaximumGapMilliseconds) * time.Millisecond},
	}
	phase, reason := edge.MeasurementPhaseComplete, ""
	var saved []string
	for i, step := range m.Spec.Steps {
		request := base
		request.Operation = Operation{ID: fmt.Sprintf("%s.%d.%s", m.UID, i, step.Name), Kind: string(step.Kind), Node: ref}
		switch step.Kind {
		case edge.OperationCostStepPower:
			act := powerRequest(targets[step.PowerMode], time.Now())
			act.OwnerReferences = []meta.OwnerReference{{APIVersion: edge.GroupVersion.String(), Kind: "OperationCostMeasurement", Name: m.Name, UID: m.UID}}
			request.Operation.Actuation = act
		case edge.OperationCostStepPrepare, edge.OperationCostStepRemove:
			spec := config.Preparations[profiles[step.BatchEnergyOptimization]]
			if step.Kind == edge.OperationCostStepPrepare {
				request.Operation.Prepare = &spec
			} else {
				request.Operation.Instance = spec.Name
			}
		case edge.OperationCostStepIdle:
			request.IdleDuration = time.Duration(*step.IdleDurationSeconds) * time.Second
		}
		record, receipt, err := c.measureStep(ctx, config, request)
		if receipt.Object.Key != "" {
			saved = append(saved, receipt.Object.Key)
			if patchErr := c.patchMeasurementStatus(ctx, m, func() {
				m.Status.Steps = append(m.Status.Steps, edge.OperationCostStepStatus{Name: step.Name, Key: receipt.Object.Key, Verdict: record.Verdict})
			}); patchErr != nil {
				err = errors.Join(err, patchErr)
			}
		}
		if err != nil {
			phase, reason = edge.MeasurementPhaseFailed, firstLine(err.Error())
		} else if record.Verdict != measurementAccepted {
			phase, reason = edge.MeasurementPhaseInsufficient, record.Reason
		}
		if phase != edge.MeasurementPhaseComplete {
			break
		}
	}
	var recordErr error
	if phase != edge.MeasurementPhaseComplete {
		recordErr = c.recordSequence(ctx, config, OperationSequence{
			SequenceID: string(m.UID), Node: ref, Requested: len(m.Spec.Steps), Saved: append([]string{}, saved...), Reason: reason,
		})
	}
	return errors.Join(recordErr, c.patchMeasurementStatus(ctx, m, func() { m.Status.Phase, m.Status.Reason = phase, reason }))
}

// restartedOperationCost ends an operation-cost measurement a restart cut off:
// Failed with its sequence record, then the hand-back.
func (c *SchedulerController) restartedOperationCost(node *core.Node, m *edge.OperationCostMeasurement, config SchedulerControllerConfig) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	root := &measurementRoot{object: m, node: node.Name}
	operation := Operation{ID: handBackKind + ":" + node.Name + ":" + string(m.UID), Kind: handBackKind, Node: ref}
	c.startNodeRun(operation, config, func(ctx context.Context, result *OperationResult) error {
		if err := c.recordRestartedSequence(ctx, config, ref, string(m.UID)); err != nil {
			return err
		}
		if err := c.failMeasurement(ctx, m, sequenceRestartReason); err != nil {
			return err
		}
		result.Progress = append(result.Progress, "sequence record "+sequenceRestartReason)
		return c.handBack(ctx, ref, string(m.UID), root, false, result)
	})
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
