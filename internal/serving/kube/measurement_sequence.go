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

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
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

// ManualOperation prepares or removes one instance of a Workload outside
// autonomous control.
type ManualOperation struct {
	Kind           string                `json:"kind"`
	WorkloadUID    string                `json:"workloadUID"`
	ProfileID      string                `json:"profileID,omitempty"`
	Node           serving.NodeReference `json:"node"`
	Instance       string                `json:"instance,omitempty"`
	FixedBatchSize int                   `json:"fixedBatchSize,omitempty"`
	Reason         string                `json:"reason"`
	Weights        map[string]int32      `json:"weights,omitempty"`
	WorkloadUIDs   []string              `json:"workloadUIDs,omitempty"`
	Scope          string                `json:"scope,omitempty"`
	ActorUsername  string                `json:"-"`
	ActorUID       string                `json:"-"`
}

// DecodeManualOperation rejects fields belonging to another operation kind,
// even when their supplied value is zero. HTTP and the CLI share this boundary.
func DecodeManualOperation(raw json.RawMessage) (ManualOperation, error) {
	var request ManualOperation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&request)
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return request, &serving.Error{Code: "InvalidBody", Message: err.Error()}
	}
	allowed := []string{"kind", "workloadUID", "reason"}
	switch request.Kind {
	case operationPrepare:
		allowed = append(allowed, "profileID", "node", "fixedBatchSize")
	case operationRemove:
		allowed = append(allowed, "node", "instance")
	case operationRoute:
		allowed = append(allowed, "weights")
	case operationUnroute, operationRelease:
	case operationReset:
		allowed = []string{"kind", "workloadUIDs", "scope", "reason"}
	default:
		return request, &serving.Error{Code: "InvalidBody", Message: "kind must be prepare, remove, route, unroute, release or reset", Field: "kind"}
	}
	for field := range fields {
		if !slices.Contains(allowed, field) {
			return request, &serving.Error{Code: "InvalidBody", Message: field + " is not valid for " + request.Kind, Field: field}
		}
	}
	if decodeErr != nil {
		return request, &serving.Error{Code: "InvalidBody", Message: decodeErr.Error()}
	}
	if strings.TrimSpace(request.Reason) == "" {
		return request, &serving.Error{Code: "InvalidBody", Message: "reason is required", Field: "reason"}
	}
	return request, nil
}

func refused(code, message string) error { return &serving.Error{Code: code, Message: message} }

// admitNodeLocked checks that a request may take the Node now: it exists, no
// measurement holds it, and no run or power change is in flight. Callers hold
// tickMu, so no run starts meanwhile.
func (c *SchedulerController) admitNodeLocked(ctx context.Context, ref serving.NodeReference) (*core.Node, error) {
	node, err := c.admitNodeStateLocked(ctx, ref)
	if err != nil {
		return nil, err
	}
	if c.nodeBusy(node) {
		return nil, refused("OperationConflict", "Node resources are reserved")
	}
	return node, nil
}

// Reset checks reservation ownership separately, then applies the same physical
// Node/measurement/power guard without rejecting the terminal owner it inherits.
func (c *SchedulerController) admitNodeStateLocked(ctx context.Context, ref serving.NodeReference) (*core.Node, error) {
	node, err := c.kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
	if err != nil || string(node.UID) != ref.UID {
		return nil, refused(refusedNodeMissing, fmt.Sprintf("Node %s/%s is not present", ref.Name, ref.UID))
	}
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	var classes edge.DeviceClassList
	if err = c.store.client.List(ctx, &classes); err != nil {
		return nil, err
	}
	if profilingrun.InMeasurement(node) || powerModePending(node, classes.Items, config, time.Now()) {
		return nil, refused("NodeBusy", "Node "+ref.Name+" is held by a measurement, a run or a power change")
	}
	return node, nil
}

// measureOperation measures one request with the measurement operations: the
// configured meter of its Node, the configured evidence store and the catalog
// profile of a prepare.
func (c *SchedulerController) measureOperation(ctx context.Context, config SchedulerControllerConfig, request MeasurementRequest) (OperationMeasurement, evidencestore.Receipt, error) {
	record := OperationMeasurement{SchemaVersion: OperationMeasurementSchema, Request: request, Verdict: measurementInsufficient}
	i := slices.IndexFunc(config.Meters, func(m MeterDeclaration) bool { return m.Node == request.Operation.Node })
	if i < 0 {
		return record, evidencestore.Receipt{}, refused(MeterUndeclared, "no wall meter is configured for Node "+request.Operation.Node.Name)
	}
	request.Meter, request.Store = config.Meters[i], config.EvidenceStore
	profiles := c.observer.Profiles()
	if prepare := request.Operation.Prepare; prepare != nil {
		j := slices.IndexFunc(profiles, func(p serving.Profile) bool {
			return prepare.Instance.Profile != nil && p.ID == prepare.Instance.Profile.ID
		})
		if j < 0 {
			return record, evidencestore.Receipt{}, refused("InvalidInput", "operation preparation has no catalog-verified selected profile")
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
	node, generation, err := c.measurement.powerModeStart(ctx, request.Operation.Node)
	if err != nil {
		return record, evidencestore.Receipt{}, err
	}
	var classes edge.DeviceClassList
	if err = c.store.client.List(ctx, &classes); err != nil {
		return record, evidencestore.Receipt{}, err
	}
	changes := expectedPowerModeChanges(request.Operation, readBackMode(node, nodeClass(node, classes.Items)))
	return c.measurement.Measure(ctx, request, source, config.Evidence, func(checkCtx context.Context) (MeasurementIsolation, error) {
		return c.measurement.measurementIsolation(checkCtx, request.Operation.Node, models, generation, changes)
	})
}

// expectedPowerModeChanges is the number of power-mode changes a measured
// operation makes itself: one for a power step into another mode, else none.
func expectedPowerModeChanges(operation Operation, current string) int64 {
	if operation.Kind == operationPower && operation.PowerMode != current {
		return 1
	}
	return 0
}

// powerModeStart reads the Node at a measurement window's start and its
// power-mode generation, nil on a Node without an actuation agent.
func (o *Operations) powerModeStart(ctx context.Context, ref serving.NodeReference) (*core.Node, *PowerModeGeneration, error) {
	node, err := o.kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	generation, ok, err := chillmeta.NodePowerModeGeneration(node)
	if err != nil || !ok {
		return node, nil, err
	}
	return node, &PowerModeGeneration{Start: generation}, nil
}

// measurementIsolation is the final isolation witness: the Node still carries
// the measurement taint, the Workloads of its measured models are recorded,
// and the Node's power-mode generation advanced by exactly the changes the
// measurement made itself since the window's start generation.
func (o *Operations) measurementIsolation(ctx context.Context, expected serving.NodeReference, models map[string]bool, generation *PowerModeGeneration, changes int64) (MeasurementIsolation, error) {
	witness := MeasurementIsolation{Workloads: []serving.WorkloadReference{}}
	node, err := o.kube.CoreV1().Nodes().Get(ctx, expected.Name, meta.GetOptions{})
	if err != nil {
		return witness, err
	}
	witness.Node = serving.NodeReference{Name: node.Name, UID: string(node.UID)}
	witness.Tainted = profilingrun.InMeasurement(node)
	var workloads edge.WorkloadList
	if err = o.store.client.List(ctx, &workloads); err != nil {
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
		return witness, fmt.Errorf("%s: power-mode generation %d to %d, expected %d changes", powerChangedReason, generation.Start, end, changes)
	}
	return witness, nil
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
	identities, err := config.Evidence.List(ctx, OperationMeasurementPrefix+node.UID+"/")
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
func (c *SchedulerController) ExecuteManual(ctx context.Context, request ManualOperation) (OperationResult, error) {
	c.tickMu.Lock()
	c.mu.Lock()
	config, snapshot := c.config, c.state.Snapshot
	c.mu.Unlock()
	var operation Operation
	var err error
	if request.Kind == operationReset {
		// Structural/scope refusals need no physical discovery or watcher start.
		snapshot.Workloads = c.runtime.SchedulerInputs(time.Now).Workloads
		operation, err = c.manualResetOperationLocked(request, config, snapshot)
		if err == nil {
			err = c.observer.Refresh(ctx)
		}
	} else {
		operation, err = c.manualOperationLocked(ctx, request, config, snapshot)
	}
	if err != nil {
		c.tickMu.Unlock()
		return OperationResult{}, err
	}
	plan := manualResetPlan{}
	if operation.Kind == operationReset {
		plan, err = c.planManualResetLocked(ctx, operation)
		for _, node := range plan.nodes {
			if err != nil {
				break
			}
			_, err = c.admitNodeStateLocked(ctx, node)
		}
		if err != nil {
			c.tickMu.Unlock()
			return OperationResult{}, err
		}
		operation.Nodes = slices.Clone(plan.nodes)
	}
	done, outcome := c.startNodeRun(operation, config, func(runCtx context.Context, result *OperationResult) error {
		workCtx, cancel := context.WithCancel(runCtx)
		stop := context.AfterFunc(ctx, cancel)
		defer stop()
		defer cancel()
		if config.OnAction != nil {
			if err := config.OnAction(*result); err != nil {
				result.effectsNotStarted = true
				return fmt.Errorf("record manual operation before physical effect: %w", err)
			}
		}
		return c.executeManualOperation(workCtx, operation, config, plan, result)
	})
	c.tickMu.Unlock()
	select {
	case <-done:
		if outcome.Error == "OperationConflict: resources are reserved" {
			return *outcome, refused("OperationConflict", outcome.Error)
		}
		return *outcome, nil
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	}
}

func (c *SchedulerController) executeManualOperation(ctx context.Context, operation Operation, config SchedulerControllerConfig, plan manualResetPlan, result *OperationResult) error {
	switch operation.Kind {
	case operationReset:
		return c.executeManualReset(ctx, operation, config, plan, result)
	case operationRoute, operationUnroute, operationRelease:
		outcome, err := c.executeManualRouting(ctx, operation)
		*result = outcome
		return err
	default:
		outcome, err := c.operations.Execute(ctx, operation)
		*result = outcome
		return err
	}
}

func (c *SchedulerController) manualOperationLocked(ctx context.Context, request ManualOperation, config SchedulerControllerConfig, snapshot serving.SchedulerSnapshot) (Operation, error) {
	switch request.Kind {
	case operationRoute, operationUnroute, operationRelease:
		return c.manualRoutingOperationLocked(ctx, request, config, snapshot)
	case operationPrepare, operationRemove:
	default:
		return Operation{}, refused("InvalidInput", "unsupported manual operation kind")
	}
	if strings.TrimSpace(request.Reason) == "" || request.Weights != nil || request.WorkloadUIDs != nil || request.Scope != "" {
		return Operation{}, refused("InvalidInput", "manual instance operation requires a reason and only instance fields")
	}
	if !slices.ContainsFunc(snapshot.Workloads, func(d *serving.Definition) bool { return d != nil && d.UID == request.WorkloadUID }) {
		return Operation{}, refused("InvalidInput", "workloadUID must name a current Workload")
	}
	if slices.Contains(config.Settings.AutonomousWorkloads, request.WorkloadUID) {
		return Operation{}, refused("OperationConflict", "the Workload is under autonomous control")
	}
	if err := c.authorize(Operation{Node: request.Node, WorkloadUID: request.WorkloadUID, Instance: request.Instance}); err != nil {
		return Operation{}, err
	}
	node, err := c.admitNodeLocked(ctx, request.Node)
	if err != nil {
		return Operation{}, err
	}
	operation := Operation{ID: "manual-" + request.Kind + ":" + string(uuid.NewUUID()), Kind: request.Kind, Node: request.Node,
		WorkloadUID: request.WorkloadUID, Reason: request.Reason, ActorUsername: request.ActorUsername, ActorUID: request.ActorUID}
	if request.Kind == operationPrepare {
		spec, err := c.manualPreparationLocked(ctx, request, config, snapshot, node)
		if err != nil {
			return Operation{}, err
		}
		operation.Prepare = &spec
	} else {
		if err := manualRemovalAllowed(snapshot, request); err != nil {
			return Operation{}, err
		}
		operation.Instance = request.Instance
	}
	action := serving.Action{Kind: operation.Kind, Node: operation.Node, Instance: operation.Instance}
	if operation.Prepare != nil {
		action.From, action.To = nativeStateUnloaded, serving.SchedulerInstanceState(operation.Prepare.Instance)
	} else {
		for _, instance := range snapshot.Instances {
			if instance.ID == operation.Instance {
				action.From, action.To = serving.SchedulerInstanceState(instance), nativeStateUnloaded
			}
		}
	}
	action = serving.EstimateAction(snapshot, action)
	operation.ExpectedSeconds, operation.ExpectedSource = action.ExpectedSeconds, action.ExpectedSource
	return operation, nil
}

func (c *SchedulerController) manualPreparationLocked(ctx context.Context, request ManualOperation, config SchedulerControllerConfig, snapshot serving.SchedulerSnapshot, node *core.Node) (PrepareSpec, error) {
	spec, ok := config.Preparations[request.ProfileID]
	if !ok || spec.Instance.Node != request.Node || request.Instance != "" {
		return PrepareSpec{}, refused("InvalidInput", "profileID must name a prepared profile of the Node")
	}
	if request.FixedBatchSize != 0 {
		spec.Config.Policy.FixedBatchSize, spec.Instance.FixedBatchSize = request.FixedBatchSize, request.FixedBatchSize
		checked := spec.Config
		checked.Policy.Batches = spec.Instance.Profile.NativeBundle().Batches
		if err := native.ValidateConfig(&checked); err != nil {
			return PrepareSpec{}, refused("InvalidInput", err.Error())
		}
	}
	for _, d := range snapshot.Workloads {
		if d != nil && d.UID == request.WorkloadUID {
			spec.Owner = &serving.WorkloadReference{Name: d.Name, UID: d.UID}
		}
	}
	index := slices.IndexFunc(snapshot.Nodes, func(n serving.SchedulerNode) bool { return n.Node == request.Node })
	if index < 0 {
		return PrepareSpec{}, refused("OperationConflict", "the Node is absent from the last snapshot")
	}
	pods, err := c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{})
	if err != nil {
		return PrepareSpec{}, err
	}
	if err = ValidatePreparationCapacity(c.observer.config.Namespace, node, pods, snapshot.Nodes[index], spec.Instance); err != nil {
		return PrepareSpec{}, refused("OperationConflict", err.Error())
	}
	return spec, nil
}

func manualRemovalAllowed(snapshot serving.SchedulerSnapshot, request ManualOperation) error {
	if request.FixedBatchSize != 0 || request.ProfileID != "" {
		return refused("InvalidInput", "remove takes only the instance")
	}
	if err := removableBy(snapshot, request.Instance, request.WorkloadUID); err != nil {
		return refused("OperationConflict", err.Error())
	}
	if !slices.ContainsFunc(snapshot.Instances, func(instance serving.SchedulerInstance) bool {
		return instance.ID == request.Instance && instance.Node == request.Node
	}) {
		return refused("InvalidInput", "remove target is not on the supplied Node")
	}
	for _, weights := range snapshot.Routing {
		if weights[request.Instance] > 0 {
			return refused("OperationConflict", "the instance still receives routing; unroute it first")
		}
	}
	for _, requestInFlight := range snapshot.Outstanding {
		if requestInFlight.Instance == request.Instance {
			return refused("OperationConflict", "the instance still owns accepted requests")
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
