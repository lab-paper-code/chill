package kube

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	operationCostKind       = "operation-cost-measurement"
	refusedProfileMissing   = "ProfileMissing"
	refusedNodeMismatch     = "NodeMismatch"
	refusedPowerModeUnknown = "PowerModeUnknown"
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
	case *edge.DispatchOccupancyMeasurement:
		return c.patchMeasurementStatus(ctx, o, func() { o.Status.Phase, o.Status.Reason = edge.MeasurementPhaseFailed, reason })
	}
	return nil
}

// startOperationCost registers the run of a granted operation-cost measurement.
func (c *SchedulerController) startOperationCost(node *core.Node, m *edge.OperationCostMeasurement, profiles map[string]string, in admissionInputs) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	targets := nodePowerTargets(node, in.classes, in.config.PowerPolicies)
	root := &measurementRoot{object: m, node: node.Name}
	operation := Operation{ID: operationCostKind + ":" + node.Name + ":" + string(m.UID), Kind: operationCostKind, Node: ref, Holder: string(m.UID)}
	c.startNodeRun(operation, in.config, func(ctx context.Context, result *OperationResult) error {
		err := c.runOperationCost(ctx, m, ref, targets, profiles, in.config)
		return errors.Join(err, c.handBack(ctx, ref, string(m.UID), root, false, result))
	})
}

// runOperationCost measures the steps in order and writes the status; a
// sequence that stops early gets its sequence record.
func (c *SchedulerController) runOperationCost(ctx context.Context, m *edge.OperationCostMeasurement, ref serving.NodeReference, targets map[string]time.Duration, profiles map[string]string, config SchedulerControllerConfig) error {
	if err := c.patchMeasurementStatus(ctx, m, func() { m.Status.Phase = edge.MeasurementPhaseMeasuring }); err != nil {
		return err
	}
	observation := m.Spec.Observation
	allowance, err := measurementTimingAllowance(observation.Timing)
	if err != nil {
		return errors.Join(err, c.failMeasurement(ctx, m, firstLine(err.Error())))
	}
	interval := time.Duration(observation.IntervalMilliseconds) * time.Millisecond
	duration := time.Duration(observation.MaximumOperationSeconds) * time.Second
	timeout := time.Duration(observation.RequestTimeoutMilliseconds) * time.Millisecond
	// One finite watchdog covers pre-support, the unchanged action, post-support
	// and two polling/transport deliveries. The action gets its own full budget.
	if interval <= 0 || duration <= 0 || timeout <= 0 || allowance > (time.Duration(1<<63-1)-duration-2*interval-timeout)/2 {
		err := errors.New("invalid or overflowing operation measurement budget")
		return errors.Join(err, c.failMeasurement(ctx, m, err.Error()))
	}
	watchdog := duration + 2*allowance + 2*interval + timeout
	base := MeasurementRequest{
		Timing: observation.Timing, OperationBudget: duration,
		Sampling: metricobserver.Request{Interval: interval, Duration: watchdog,
			RequestTimeout: timeout, MaxEvents: int(watchdog/interval) + 3},
		Coverage: powerwindow.Policy{MaximumGap: time.Duration(observation.MaximumGapMilliseconds) * time.Millisecond},
	}
	phase, reason := edge.MeasurementPhaseComplete, ""
	var saved []string
	prepared := map[string]string{}
	for i, step := range m.Spec.Steps {
		request := base
		request.Operation = Operation{ID: fmt.Sprintf("%s.%d.%s", m.UID, i, step.Name), Kind: string(step.Kind), Node: ref}
		switch step.Kind {
		case edge.OperationCostStepPower:
			request.Operation.PowerMode, request.Operation.MaxActuationDuration = step.PowerMode, targets[step.PowerMode]
			request.Operation.Holder = string(m.UID)
		case edge.OperationCostStepPrepare, edge.OperationCostStepRemove:
			spec := config.Preparations[profiles[step.BatchEnergyOptimization]]
			if step.Kind == edge.OperationCostStepPrepare {
				request.Operation.Prepare = &spec
			} else {
				request.Operation.Instance = prepared[profiles[step.BatchEnergyOptimization]]
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
		if step.Kind == edge.OperationCostStepPrepare {
			if actual := schedulerPhysicalState(request.Operation.Prepare.Instance.ID, record.Result.Actual.Instances); actual != nil {
				prepared[profiles[step.BatchEnergyOptimization]] = actual.ID
			}
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

// restartedMeasurement ends a measurement run a restart cut off: Failed, an
// operation-cost sequence record, then the hand-back.
func (c *SchedulerController) restartedMeasurement(node *core.Node, object client.Object, config SchedulerControllerConfig) {
	ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	root := &measurementRoot{object: object, node: node.Name}
	uid := string(object.GetUID())
	operation := Operation{ID: handBackKind + ":" + node.Name + ":" + uid, Kind: handBackKind, Node: ref, Holder: uid}
	c.startNodeRun(operation, config, func(ctx context.Context, result *OperationResult) error {
		if _, cost := object.(*edge.OperationCostMeasurement); cost {
			if err := c.recordRestartedSequence(ctx, config, ref, uid); err != nil {
				return err
			}
			result.Progress = append(result.Progress, "sequence record "+sequenceRestartReason)
		}
		if err := c.failMeasurement(ctx, object, sequenceRestartReason); err != nil {
			return err
		}
		return c.handBack(ctx, ref, uid, root, false, result)
	})
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
