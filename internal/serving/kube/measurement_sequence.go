package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const (
	// OperationSequencePrefix holds the records of operation sequences that
	// did not run every request.
	OperationSequencePrefix = "serving/operation-sequences/"
	OperationSequenceSchema = "serving-operation-sequence.v1alpha1"
	sequenceIncompleteCode  = "SequenceIncomplete"
	sequenceRestartReason   = "ChillServerRestart"
)

// measurementEvidence stores operation measurements and sequence records.
type measurementEvidence interface {
	evidencestore.ImmutableWriter
	evidencestore.Lister
}

// OperationSequence records an operation sequence that stopped before its last
// request. Its saved records keep their own verdicts.
type OperationSequence struct {
	SchemaVersion string                `json:"schemaVersion"`
	SequenceID    string                `json:"sequenceID"`
	Node          serving.NodeReference `json:"node"`
	// Requested is zero when a restart lost the request count.
	Requested int      `json:"requested,omitempty"`
	Saved     []string `json:"saved"`
	Reason    string   `json:"reason"`
}

// admitNodeLocked checks that a request may take the Node now: it exists, no
// measurement holds it, and no run or power change is in flight. Callers hold
// tickMu, so no run starts meanwhile.
func (c *SchedulerController) admitNodeLocked(ctx context.Context, ref serving.NodeReference) (*core.Node, error) {
	node, err := c.admitNodeStateLocked(ctx, ref)
	if err != nil {
		return nil, err
	}
	if c.nodeBusy(node) {
		return nil, servingstate.Refused("OperationConflict", "Node resources are reserved")
	}
	return node, nil
}

// Reset checks reservation ownership separately, then applies the same physical
// Node/measurement/power guard without rejecting the terminal owner it inherits.
func (c *SchedulerController) admitNodeStateLocked(ctx context.Context, ref serving.NodeReference) (*core.Node, error) {
	node, err := c.kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
	if err != nil || string(node.UID) != ref.UID {
		return nil, servingstate.Refused(refusedNodeMissing, fmt.Sprintf("Node %s/%s is not present", ref.Name, ref.UID))
	}
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	var classes edge.DeviceClassList
	if err = c.store.client.List(ctx, &classes); err != nil {
		return nil, err
	}
	if profilingrun.InMeasurement(node) || powerModePending(node, classes.Items, config, time.Now()) {
		return nil, servingstate.Refused("NodeBusy", "Node "+ref.Name+" is held by a measurement, a run or a power change")
	}
	return node, nil
}

// measureOperation measures one request with the measurement operations: the
// configured meter of its Node, the configured evidence store and the catalog
// profile of a prepare. A measurement instance is prepared without a profile.
func (c *SchedulerController) measureOperation(ctx context.Context, config SchedulerControllerConfig, request servingops.MeasurementRequest) (servingops.OperationMeasurement, evidencestore.Receipt, error) {
	record := servingops.OperationMeasurement{SchemaVersion: servingops.OperationMeasurementSchema, Request: request, Verdict: servingops.MeasurementInsufficient}
	i := slices.IndexFunc(config.Meters, func(m servingops.MeterDeclaration) bool { return m.Node == request.Operation.Node })
	if i < 0 {
		return record, evidencestore.Receipt{}, servingstate.Refused(MeterUndeclared, "no wall meter is configured for Node "+request.Operation.Node.Name)
	}
	request.Meter, request.Store = config.Meters[i], config.EvidenceStore
	profiles := c.observer.Profiles()
	if prepare := request.Operation.Prepare; prepare != nil && !prepare.Measurement() {
		j := slices.IndexFunc(profiles, func(p serving.Profile) bool {
			return prepare.Instance.Profile != nil && p.ID == prepare.Instance.Profile.ID
		})
		if j < 0 {
			return record, evidencestore.Receipt{}, servingstate.Refused("InvalidInput", "operation preparation has no catalog-verified selected profile")
		}
		prepare.Instance.Profile = &profiles[j]
	}
	source, err := config.MeterSource(request.Meter.Source)
	if err != nil {
		return record, evidencestore.Receipt{}, err
	}
	models := map[string]bool{}
	for _, p := range profiles {
		if p.Condition.NodeUID == request.Operation.Node.UID {
			models[p.Condition.LogicalModel] = true
		}
	}
	node, generation, err := c.measurement.PowerModeStart(ctx, request.Operation.Node)
	if err != nil {
		return record, evidencestore.Receipt{}, err
	}
	var classes edge.DeviceClassList
	if err = c.store.client.List(ctx, &classes); err != nil {
		return record, evidencestore.Receipt{}, err
	}
	changes := expectedPowerModeChanges(request.Operation, servingstate.ReadBackMode(node, servingstate.NodeClass(node, classes.Items)))
	return c.measurement.Measure(ctx, request, source, config.Evidence, func(checkCtx context.Context) (servingops.MeasurementIsolation, error) {
		return c.measurement.MeasurementIsolation(checkCtx, request.Operation.Node, models, generation, changes)
	})
}

// expectedPowerModeChanges is the number of power-mode changes a measured
// operation makes itself: one for a power step into another mode, else none.
func expectedPowerModeChanges(operation servingstate.Operation, current string) int64 {
	if operation.Kind == servingstate.OperationPower && operation.PowerMode != current {
		return 1
	}
	return 0
}

// recordSequence stores the record of a sequence that did not run every request.
func (c *SchedulerController) recordSequence(ctx context.Context, config SchedulerControllerConfig, sequence OperationSequence) error {
	if config.Evidence == nil {
		return errors.New("operation measurement evidence store is not configured")
	}
	sequence.SchemaVersion = OperationSequenceSchema
	raw, err := json.Marshal(sequence)
	if err != nil {
		return err
	}
	identity := evidencestore.ObjectIdentity{Store: config.EvidenceStore, Key: OperationSequencePrefix + sequence.SequenceID + ".json", Size: int64(len(raw))}
	_, err = config.Evidence.PutImmutable(ctx, identity, bytes.NewReader(raw))
	return err
}

// recordRestartedSequence records a sequence a restart cut off; its saved
// records are the measurement keys that carry its ID.
func (c *SchedulerController) recordRestartedSequence(ctx context.Context, config SchedulerControllerConfig, node serving.NodeReference, id string) error {
	if config.Evidence == nil {
		return errors.New("operation measurement evidence store is not configured")
	}
	identities, err := config.Evidence.List(ctx, servingops.OperationMeasurementPrefix+node.UID+"/")
	if err != nil {
		return err
	}
	saved := []string{}
	for _, identity := range identities {
		if strings.HasPrefix(path.Base(identity.Key), id+".") {
			saved = append(saved, identity.Key)
		}
	}
	slices.Sort(saved)
	return c.recordSequence(ctx, config, OperationSequence{SequenceID: id, Node: node, Saved: saved, Reason: sequenceRestartReason})
}

// ExecuteManual reserves admitted resources before executing outside tickMu.
// Physical failures retain the OperationResult envelope and partial evidence.
func (c *SchedulerController) ExecuteManual(ctx context.Context, request servingstate.ManualOperation) (servingstate.OperationResult, error) {
	c.tickMu.Lock()
	c.mu.Lock()
	config, snapshot := c.config, c.snapshotLocked()
	c.mu.Unlock()
	var operation servingstate.Operation
	var err error
	if request.Kind == servingstate.OperationReset {
		// Structural/scope refusals need no physical discovery or watcher start.
		snapshot.Workloads = c.runtime.SchedulerInputs(time.Now).Workloads
		operation, err = c.manualResetOperationLocked(request, config, snapshot)
		if err == nil {
			err = c.observer.Refresh(ctx)
		}
	} else {
		// A completed manual ACK can change routing or instances between Ticks.
		// Admit from the same live sources as Tick, without publishing a decision.
		snapshot, err = c.capture(ctx, time.Now(), config)
		if err == nil {
			operation, err = c.manualOperationLocked(ctx, request, config, snapshot)
		}
	}
	if err != nil {
		c.tickMu.Unlock()
		return servingstate.OperationResult{}, err
	}
	plan := manualResetPlan{}
	if operation.Kind == servingstate.OperationReset {
		plan, err = c.planManualResetLocked(ctx, operation)
		for _, node := range plan.nodes {
			if err != nil {
				break
			}
			_, err = c.admitNodeStateLocked(ctx, node)
		}
		if err != nil {
			c.tickMu.Unlock()
			return servingstate.OperationResult{}, err
		}
		operation.Nodes = slices.Clone(plan.nodes)
	}
	done, outcome := c.startNodeRun(operation, config, func(runCtx context.Context, result *servingstate.OperationResult) error {
		workCtx, cancel := context.WithCancel(runCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		if config.OnAction != nil {
			if err := config.OnAction(*result); err != nil {
				result.EffectsNotStarted = true
				return fmt.Errorf("record manual operation before physical effect: %w", err)
			}
		}
		return c.executeManualOperation(workCtx, operation, config, plan, result)
	})
	c.tickMu.Unlock()
	select {
	case <-done:
		if outcome.Error == "OperationConflict: resources are reserved" {
			return *outcome, servingstate.Refused("OperationConflict", outcome.Error)
		}
		return *outcome, nil
	case <-ctx.Done():
		return servingstate.OperationResult{}, ctx.Err()
	}
}

func (c *SchedulerController) executeManualOperation(ctx context.Context, operation servingstate.Operation, config SchedulerControllerConfig, plan manualResetPlan, result *servingstate.OperationResult) error {
	startedAt := result.StartedAt
	defer func() { result.StartedAt = startedAt }()
	switch operation.Kind {
	case servingstate.OperationReset:
		return c.executeManualReset(ctx, operation, config, plan, result)
	case servingstate.OperationRoute, servingstate.OperationUnroute, servingstate.OperationRelease:
		outcome, err := c.executeManualRouting(ctx, operation)
		*result = outcome
		return err
	default:
		outcome, err := c.operations.Execute(ctx, operation)
		*result = outcome
		return err
	}
}

func (c *SchedulerController) manualOperationLocked(ctx context.Context, request servingstate.ManualOperation, config SchedulerControllerConfig, snapshot serving.SchedulerSnapshot) (servingstate.Operation, error) {
	switch request.Kind {
	case servingstate.OperationRoute, servingstate.OperationUnroute, servingstate.OperationRelease:
		return c.manualRoutingOperationLocked(ctx, request, config, snapshot)
	case servingstate.OperationPrepare, servingstate.OperationRemove:
	default:
		return servingstate.Operation{}, servingstate.Refused("InvalidInput", "unsupported manual operation kind")
	}
	if strings.TrimSpace(request.Reason) == "" || request.Weights != nil || request.WorkloadUIDs != nil || request.Scope != "" {
		return servingstate.Operation{}, servingstate.Refused("InvalidInput", "manual instance operation requires a reason and only instance fields")
	}
	if !slices.ContainsFunc(snapshot.Workloads, func(d *serving.Definition) bool { return d != nil && d.UID == request.WorkloadUID }) {
		return servingstate.Operation{}, servingstate.Refused("InvalidInput", "workloadUID must name a current Workload")
	}
	if slices.Contains(config.Settings.AutonomousWorkloads, request.WorkloadUID) {
		return servingstate.Operation{}, servingstate.Refused("OperationConflict", "the Workload is under autonomous control")
	}
	if err := c.authorize(servingstate.Operation{Node: request.Node, WorkloadUID: request.WorkloadUID, Instance: request.Instance}); err != nil {
		return servingstate.Operation{}, err
	}
	node, err := c.admitNodeLocked(ctx, request.Node)
	if err != nil {
		return servingstate.Operation{}, err
	}
	operation := servingstate.Operation{ID: "manual-" + request.Kind + ":" + string(uuid.NewUUID()), Kind: request.Kind, Node: request.Node,
		WorkloadUID: request.WorkloadUID, Reason: request.Reason, ActorUsername: request.ActorUsername, ActorUID: request.ActorUID}
	if request.Kind == servingstate.OperationPrepare {
		spec, err := c.manualPreparationLocked(ctx, request, config, snapshot, node)
		if err != nil {
			return servingstate.Operation{}, err
		}
		operation.Prepare = &spec
	} else {
		if err := manualRemovalAllowed(snapshot, request); err != nil {
			return servingstate.Operation{}, err
		}
		operation.Instance = request.Instance
	}
	action := serving.Action{Kind: operation.Kind, Node: operation.Node, Instance: operation.Instance}
	if operation.Prepare != nil {
		action.From, action.To = servingstate.NativeStateUnloaded, serving.SchedulerInstanceState(operation.Prepare.Instance)
	} else {
		for _, instance := range snapshot.Instances {
			if instance.ID == operation.Instance {
				action.From, action.To = serving.SchedulerInstanceState(instance), servingstate.NativeStateUnloaded
			}
		}
	}
	bindTerminatedPodRemoval(&operation, action, snapshot.Instances)
	action = serving.EstimateAction(config.Settings, snapshot, action)
	operation.ExpectedSeconds, operation.ExpectedSource = action.ExpectedSeconds, action.ExpectedSource
	return operation, nil
}

func (c *SchedulerController) manualPreparationLocked(ctx context.Context, request servingstate.ManualOperation, config SchedulerControllerConfig, snapshot serving.SchedulerSnapshot, node *core.Node) (servingstate.PrepareSpec, error) {
	spec, ok := config.Preparations[request.ProfileID]
	if !ok || spec.Instance.Node != request.Node || request.Instance != "" {
		return servingstate.PrepareSpec{}, servingstate.Refused("InvalidInput", "profileID must name a prepared profile of the Node")
	}
	if request.MaxBatch != nil {
		if *request.MaxBatch <= 0 {
			return servingstate.PrepareSpec{}, servingstate.Refused("InvalidInput", "maxBatch must be a positive measured batch")
		}
		spec.Config.Policy.MaxBatchSize, spec.Instance.MaxBatch = *request.MaxBatch, *request.MaxBatch
	}
	if request.FixedBatchSize != 0 {
		spec.Config.Policy.FixedBatchSize, spec.Instance.FixedBatchSize = request.FixedBatchSize, request.FixedBatchSize
	}
	if request.MaxBatch != nil || request.FixedBatchSize != 0 {
		checked := spec.Config
		checked.Policy.Batches = spec.Instance.Profile.NativeBundle().Batches
		if err := native.ValidateConfig(&checked); err != nil {
			return servingstate.PrepareSpec{}, servingstate.Refused("InvalidInput", err.Error())
		}
	}
	for _, d := range snapshot.Workloads {
		if d != nil && d.UID == request.WorkloadUID {
			spec.Owner = &serving.WorkloadReference{Name: d.Name, UID: d.UID}
		}
	}
	index := slices.IndexFunc(snapshot.Nodes, func(n serving.SchedulerNode) bool { return n.Node == request.Node })
	if index < 0 {
		return servingstate.PrepareSpec{}, servingstate.Refused("OperationConflict", "the Node is absent from the last snapshot")
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{})
	if err != nil {
		return servingstate.PrepareSpec{}, err
	}
	if err = ValidatePreparationCapacity(c.observer.config.Namespace, node, pods, snapshot.Nodes[index], spec.Instance); err != nil {
		return servingstate.PrepareSpec{}, servingstate.Refused("OperationConflict", err.Error())
	}
	return spec, nil
}

func manualRemovalAllowed(snapshot serving.SchedulerSnapshot, request servingstate.ManualOperation) error {
	if request.FixedBatchSize != 0 || request.MaxBatch != nil || request.ProfileID != "" {
		return servingstate.Refused("InvalidInput", "remove takes only the instance")
	}
	if err := removableBy(snapshot, request.Instance, request.WorkloadUID); err != nil {
		return servingstate.Refused("OperationConflict", err.Error())
	}
	if !slices.ContainsFunc(snapshot.Instances, func(instance serving.SchedulerInstance) bool {
		return instance.ID == request.Instance && instance.Node == request.Node
	}) {
		return servingstate.Refused("InvalidInput", "remove target is not on the supplied Node")
	}
	for _, weights := range snapshot.Routing {
		if weights[request.Instance] > 0 {
			return servingstate.Refused("OperationConflict", "the instance still receives routing; unroute it first")
		}
	}
	for _, requestInFlight := range snapshot.Outstanding {
		if requestInFlight.Instance == request.Instance {
			return servingstate.Refused("OperationConflict", "the instance still owns accepted requests")
		}
	}
	return nil
}

// removableBy allows a remove only of an instance no other Workload owns or is bound to.
func removableBy(snapshot serving.SchedulerSnapshot, id, workloadUID string) error {
	i := slices.IndexFunc(snapshot.Instances, func(s serving.SchedulerInstance) bool { return s.ID == id })
	if i < 0 {
		return errors.New("remove target is absent from the snapshot")
	}
	instance := snapshot.Instances[i]
	if (instance.OwnerWorkloadUID != "" && instance.OwnerWorkloadUID != workloadUID) ||
		(instance.WorkloadUID != "" && instance.WorkloadUID != workloadUID) {
		return errors.New("remove target serves another Workload")
	}
	return nil
}
