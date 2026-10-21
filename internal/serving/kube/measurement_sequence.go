package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// OperationSequencePrefix holds the records of operation sequences that
	// did not run every request.
	OperationSequencePrefix = "serving/operation-sequences/"
	OperationSequenceSchema = "serving-operation-sequence.v1alpha1"
	// sequencePrefix starts every operation sequence ID, the taint value of its Node.
	sequencePrefix         = "seq-"
	operationSequenceKind  = "measurement-sequence"
	sequenceIncompleteCode = "SequenceIncomplete"
	sequenceRestartReason  = "ChillServerRestart"
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

// MeasuredOperation is one saved operation measurement of a sequence.
type MeasuredOperation struct {
	Record  OperationMeasurement  `json:"record"`
	Receipt evidencestore.Receipt `json:"receipt"`
}

// SequenceResult is the outcome of one operation sequence.
type SequenceResult struct {
	SequenceID   string              `json:"sequenceID"`
	Measurements []MeasuredOperation `json:"measurements"`
	Error        string              `json:"error,omitempty"`
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
}

func refused(code, message string) error { return &serving.Error{Code: code, Message: message} }

// admitNodeLocked checks that a request may take the Node now: it exists, no
// measurement holds it, no run or power change is in flight, and no CHILL
// serving Pod runs on it. Callers hold tickMu, so no run starts meanwhile and
// a run that ended before the busy check has its Pods in the list after it.
func (c *SchedulerController) admitNodeLocked(ctx context.Context, ref serving.NodeReference, measurement bool) (*core.Node, error) {
	node, err := c.kube.CoreV1().Nodes().Get(ctx, ref.Name, meta.GetOptions{})
	if err != nil || string(node.UID) != ref.UID {
		return nil, refused(refusedNodeMissing, fmt.Sprintf("Node %s/%s is not present", ref.Name, ref.UID))
	}
	c.mu.Lock()
	namespaces := c.config.MeasurementNamespaces
	c.mu.Unlock()
	acts, err := c.nodeActuations(ctx, namespaces)
	if err != nil {
		return nil, err
	}
	if profilingrun.InMeasurement(node) || c.nodeBusy(node) || pendingActuation(acts, ref.UID, time.Now()) {
		return nil, refused("NodeBusy", "Node "+ref.Name+" is held by a measurement, a run or a power change")
	}
	if !measurement {
		return node, nil
	}
	pods, err := c.kube.CoreV1().Pods(c.observer.config.Namespace).List(ctx, meta.ListOptions{
		LabelSelector: "app.kubernetes.io/component=" + servingInstanceComponent,
	})
	if err != nil {
		return nil, err
	}
	if names := servingPodsOn(pods.Items, ref.Name); len(names) > 0 {
		return nil, refused(refusedNodeServing, strings.Join(names, ","))
	}
	return node, nil
}

// MeasureSequence measures the requests in order on one Node admitted once,
// then hands the Node back. It stops at the first request that fails or is
// not Accepted, and records a sequence that did not run every request.
func (c *SchedulerController) MeasureSequence(ctx context.Context, requests []MeasurementRequest) (SequenceResult, error) {
	if len(requests) == 0 {
		return SequenceResult{}, refused("InvalidInput", "at least one measurement request is required")
	}
	ref := requests[0].Operation.Node
	for _, r := range requests {
		if r.Operation.Node != ref || r.Operation.ID == "" {
			return SequenceResult{}, refused("InvalidInput", "every request needs an ID and the same Node")
		}
	}
	c.tickMu.Lock()
	c.mu.Lock()
	config := c.config
	c.mu.Unlock()
	node, err := c.admitNodeLocked(ctx, ref, true)
	if err != nil {
		c.tickMu.Unlock()
		return SequenceResult{}, err
	}
	if config.Evidence == nil || config.MeterSource == nil {
		c.tickMu.Unlock()
		return SequenceResult{}, errors.New("operation measurement evidence store is not configured")
	}
	id := sequencePrefix + string(uuid.NewUUID())
	if err = c.setMeasurementTaint(ctx, node, id); err != nil {
		c.tickMu.Unlock()
		return SequenceResult{}, err
	}
	c.mu.Lock()
	c.sequences[id] = true
	c.mu.Unlock()
	sequence := SequenceResult{SequenceID: id, Measurements: []MeasuredOperation{}}
	done, outcome := c.startNodeRun(Operation{ID: id, Kind: operationSequenceKind, Node: ref}, config, func(runCtx context.Context, result *OperationResult) error {
		measureCtx, stop := context.WithCancel(runCtx)
		defer stop()
		defer context.AfterFunc(ctx, stop)()
		var measureErr error
		for i, request := range requests {
			request.Operation.ID = fmt.Sprintf("%s.%d.%s", id, i, request.Operation.ID)
			record, receipt, err := c.measureOperation(measureCtx, config, request)
			if receipt.Object.Key != "" {
				sequence.Measurements = append(sequence.Measurements, MeasuredOperation{Record: record, Receipt: receipt})
			}
			if err == nil && record.Verdict != measurementAccepted {
				err = fmt.Errorf("measurement %s: %s", record.Verdict, record.Reason)
			}
			if err != nil {
				measureErr = err
				break
			}
		}
		if len(sequence.Measurements) < len(requests) || measureErr != nil {
			reason := "stopped"
			if measureErr != nil {
				reason = measureErr.Error()
			}
			saved := make([]string, 0, len(sequence.Measurements))
			for _, m := range sequence.Measurements {
				saved = append(saved, m.Receipt.Object.Key)
			}
			measureErr = errors.Join(measureErr, c.recordSequence(runCtx, config, OperationSequence{
				SequenceID: id, Node: ref, Requested: len(requests), Saved: saved, Reason: reason,
			}))
		}
		return errors.Join(measureErr, c.handBack(runCtx, ref, id, nil, false, result))
	})
	c.tickMu.Unlock()
	<-done
	sequence.Error = outcome.Error
	return sequence, nil
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
	if request.Meter.ID != "" && !reflect.DeepEqual(request.Meter, config.Meters[i]) {
		return record, evidencestore.Receipt{}, refused("InvalidInput", "declared meter differs from the configured meter of the Node")
	}
	if request.Store != "" && request.Store != config.EvidenceStore {
		return record, evidencestore.Receipt{}, refused("InvalidInput", "measurements are stored in "+config.EvidenceStore)
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
	own := ""
	if request.Operation.Actuation != nil {
		own = request.Operation.Actuation.Name
	}
	since := time.Now()
	return c.measurement.Measure(ctx, request, source, config.Evidence, func(checkCtx context.Context) (MeasurementIsolation, error) {
		return c.measurement.measurementIsolation(checkCtx, request.Operation.Node, models, since, own)
	})
}

// measurementIsolation is the final isolation witness: the Node still carries
// the measurement taint, the Workloads of its measured models are recorded,
// and no power change the measurement did not request was applied since.
func (o *Operations) measurementIsolation(ctx context.Context, expected serving.NodeReference, models map[string]bool, since time.Time, own string) (MeasurementIsolation, error) {
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
	var acts edge.NodeActuationList
	if err = o.store.client.List(ctx, &acts, client.InNamespace(o.config.ActuationNamespace)); err != nil {
		return witness, err
	}
	witness.CheckedAt = time.Now().UTC()
	foreign := slices.DeleteFunc(foreignActuations(acts.Items, expected.UID, "", since, witness.CheckedAt), func(name string) bool { return name == own })
	// The taint keeps ChillServer off this Node, so Workloads of the same model are recorded, not refused.
	if witness.Node != expected || !witness.Tainted {
		return witness, errors.New("actual physical Node or taint violates measurement isolation")
	}
	if len(foreign) > 0 {
		return witness, errors.New(powerChangedReason + " " + strings.Join(foreign, ","))
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

// ExecuteManual prepares or removes one instance of a Workload outside
// autonomous control on a Node no run, measurement or power change holds.
func (c *SchedulerController) ExecuteManual(ctx context.Context, request ManualOperation) (OperationResult, error) {
	c.tickMu.Lock()
	c.mu.Lock()
	config, snapshot := c.config, c.state.Snapshot
	c.mu.Unlock()
	operation, err := c.manualOperationLocked(ctx, request, config, snapshot)
	if err != nil {
		c.tickMu.Unlock()
		return OperationResult{}, err
	}
	done, outcome := c.startNodeRun(operation, config, func(runCtx context.Context, result *OperationResult) error {
		var err error
		*result, err = c.operations.Execute(runCtx, operation)
		return err
	})
	c.tickMu.Unlock()
	select {
	case <-done:
		return *outcome, nil
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	}
}

func (c *SchedulerController) manualOperationLocked(ctx context.Context, request ManualOperation, config SchedulerControllerConfig, snapshot serving.SchedulerSnapshot) (Operation, error) {
	if !slices.ContainsFunc(snapshot.Workloads, func(d *serving.Definition) bool { return d.UID == request.WorkloadUID }) {
		return Operation{}, refused("InvalidInput", "workloadUID must name a current Workload")
	}
	if slices.Contains(config.Settings.AutonomousWorkloads, request.WorkloadUID) {
		return Operation{}, refused("OperationConflict", "the Workload is under autonomous control")
	}
	node, err := c.admitNodeLocked(ctx, request.Node, false)
	if err != nil {
		return Operation{}, err
	}
	operation := Operation{ID: "manual-" + request.Kind + ":" + string(uuid.NewUUID()), Kind: request.Kind, Node: request.Node}
	switch request.Kind {
	case operationPrepare:
		spec, ok := config.Preparations[request.ProfileID]
		if !ok || spec.Instance.Node != request.Node || request.Instance != "" {
			return Operation{}, refused("InvalidInput", "profileID must name a prepared profile of the Node")
		}
		if request.FixedBatchSize != 0 {
			// The preparation was validated when it was derived; B must be a measured, initialized batch.
			spec.Config.Policy.FixedBatchSize, spec.Instance.FixedBatchSize = request.FixedBatchSize, request.FixedBatchSize
			checked := spec.Config
			checked.Policy.Batches = spec.Instance.Profile.NativeBundle().Batches
			if err = native.ValidateConfig(&checked); err != nil {
				return Operation{}, refused("InvalidInput", err.Error())
			}
		}
		for _, d := range snapshot.Workloads {
			if d.UID == request.WorkloadUID {
				spec.Owner = &serving.WorkloadReference{Name: d.Name, UID: d.UID}
			}
		}
		i := slices.IndexFunc(snapshot.Nodes, func(n serving.SchedulerNode) bool { return n.Node == request.Node })
		if i < 0 {
			return Operation{}, refused("OperationConflict", "the Node is absent from the last snapshot")
		}
		pods, err := c.kube.CoreV1().Pods("").List(ctx, meta.ListOptions{})
		if err != nil {
			return Operation{}, err
		}
		if err = ValidatePreparationCapacity(c.observer.config.Namespace, node, pods, snapshot.Nodes[i], spec.Instance); err != nil {
			return Operation{}, refused("OperationConflict", err.Error())
		}
		operation.Prepare = &spec
	case operationRemove:
		if request.FixedBatchSize != 0 || request.ProfileID != "" {
			return Operation{}, refused("InvalidInput", "remove takes only the instance")
		}
		if err = removableBy(snapshot, request.Instance, request.WorkloadUID); err != nil {
			return Operation{}, refused("OperationConflict", err.Error())
		}
		for _, weights := range snapshot.Routing {
			if weights[request.Instance] > 0 {
				return Operation{}, refused("OperationConflict", "the instance still receives routing; change staticRouting first")
			}
		}
		for _, r := range snapshot.Outstanding {
			if r.Instance == request.Instance {
				return Operation{}, refused("OperationConflict", "the instance still owns accepted requests")
			}
		}
		operation.Instance = request.Instance
	default:
		return Operation{}, refused("InvalidInput", "kind must be prepare or remove")
	}
	return operation, nil
}

// removableBy allows a remove only of an instance no other Workload owns or is bound to.
func removableBy(snapshot serving.SchedulerSnapshot, id, workloadUID string) error {
	i := slices.IndexFunc(snapshot.Instances, func(s serving.SchedulerInstance) bool { return s.ID == id })
	if i < 0 {
		return errors.New("remove target is absent from the snapshot")
	}
	instance := snapshot.Instances[i]
	if (instance.OwnerWorkloadUID != "" && instance.OwnerWorkloadUID != workloadUID) ||
		(instance.WorkloadUID != "" && instance.WorkloadUID != workloadUID) ||
		slices.Contains(snapshot.Reasons, serving.SchedulerSharedInstance+":"+id) {
		return errors.New("InstanceShared: remove target serves other Workloads; change their distribution through staticRouting")
	}
	return nil
}
