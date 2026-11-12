package kube

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Cost issue codes.
const (
	CostEvidenceInvalid = "CostEvidenceInvalid"
	MeterUndeclared     = "MeterUndeclared"
)

// CostIssue is one measurement that contributes no cost, or, with code
// SequenceIncomplete, a record of an operation sequence that stopped early;
// that record keeps its own verdict.
type CostIssue struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// CostView is the cost evidence derived from completed measurements.
type CostView struct {
	Costs     []serving.OperationCost
	Baselines []serving.NodeBaseline
	Issues    []CostIssue
	// DispatchOccupancy is the latest accepted occupancy record per Node,
	// power mode, runtime image and model.
	DispatchOccupancy []serving.DispatchOccupancy
	// Sources names every listed measurement identity.
	Sources []string
}

type costObjects interface {
	evidencestore.Reader
	evidencestore.Lister
}

// CostEvidence derives operation costs, unloaded baselines and power mode
// transition costs from completed measurements. Stored objects are immutable,
// so each identity is read once and dropped when it is no longer listed.
type CostEvidence struct {
	store        *Store
	objects      costObjects
	maxBytes     int64
	measurements map[evidencestore.ObjectIdentity]operationEvidence
	power        map[string]powerEvidence
	occupancy    map[evidencestore.ObjectIdentity]*DispatchOccupancyRecord
}

type operationEvidence struct {
	cost     *serving.OperationCost
	baseline *serving.NodeBaseline
	profile  *serving.Profile // retained immutable preparation body, including old catalog versions
	err      error
	skip     bool // a recorded non-Accepted attempt: valid history, never a cost or issue
	// invalid is the isolation failure of a record another power change invalidated.
	invalid string
}

type powerEvidence struct {
	costs []serving.OperationCost
}

func NewCostEvidence(store *Store, objects costObjects, maxBytes int64) (*CostEvidence, error) {
	if store == nil || objects == nil || maxBytes < 1 {
		return nil, errors.New("native store and bounded evidence lister required")
	}
	return &CostEvidence{store: store, objects: objects, maxBytes: maxBytes,
		measurements: map[evidencestore.ObjectIdentity]operationEvidence{}, power: map[string]powerEvidence{},
		occupancy: map[evidencestore.ObjectIdentity]*DispatchOccupancyRecord{}}, nil
}

// Refresh lists operation measurements under the fixed prefix and Complete
// PowerModeTransitionCost objects in namespaces. A listing failure fails the
// refresh; one failed record or measurement becomes its own CostIssue.
func (e *CostEvidence) Refresh(ctx context.Context, namespaces []string, profiles []serving.Profile) (CostView, error) {
	view := CostView{Issues: []CostIssue{}}
	identities, err := e.objects.List(ctx, servingops.OperationMeasurementPrefix)
	if err != nil {
		return CostView{}, err
	}
	incomplete, err := e.incompleteSequences(ctx)
	if err != nil {
		return CostView{}, err
	}
	listed := map[evidencestore.ObjectIdentity]bool{}
	for _, identity := range identities {
		listed[identity] = true
		if id, _, ok := strings.Cut(path.Base(identity.Key), "."); ok && incomplete[id] {
			view.Issues = append(view.Issues, CostIssue{Source: identity.Key, Code: sequenceIncompleteCode, Reason: "sequence " + id + " stopped before its last request"})
		}
		view.Sources = append(view.Sources, fmt.Sprintf("%s/%s/%d", identity.Store, identity.Key, identity.Size))
		evidence, cached := e.measurements[identity]
		if !cached {
			var readErr error
			evidence, readErr = e.readOperationEvidence(ctx, identity)
			if readErr != nil {
				view.Issues = append(view.Issues, CostIssue{Source: identity.Key, Code: CostEvidenceInvalid, Reason: readErr.Error()})
				continue
			}
			e.measurements[identity] = evidence
		}
		switch {
		case evidence.invalid != "":
			view.Issues = append(view.Issues, CostIssue{Source: identity.Key, Code: measurementInvalidCode, Reason: evidence.invalid})
		case evidence.skip:
		case evidence.err != nil:
			view.Issues = append(view.Issues, CostIssue{Source: identity.Key, Code: CostEvidenceInvalid, Reason: evidence.err.Error()})
		case evidence.baseline != nil:
			view.Baselines = append(view.Baselines, *evidence.baseline)
		default:
			view.Costs = append(view.Costs, *evidence.cost)
		}
	}
	maps.DeleteFunc(e.measurements, func(identity evidencestore.ObjectIdentity, _ operationEvidence) bool { return !listed[identity] })
	profiles = slices.Clone(profiles)
	for _, evidence := range e.measurements {
		if evidence.profile != nil {
			profiles = append(profiles, *evidence.profile)
		}
	}
	if err = e.refreshPower(ctx, namespaces, &view); err != nil {
		return CostView{}, err
	}
	if err = e.refreshOccupancy(ctx, &view); err != nil {
		return CostView{}, err
	}
	profileIdentities := map[string]string{}
	nodeClasses := map[string]string{}
	for index := range profiles {
		profile := &profiles[index]
		profileIdentities[profile.ID] = serving.MeasurementProfileIdentity(profile)
		nodeClasses[profile.Condition.NodeUID] = profile.Condition.DeviceClass
	}
	for index := range view.Costs {
		cost := &view.Costs[index]
		cost.DeviceClass = nodeClasses[cost.Node.UID]
		cost.StateIdentities = retainedStateIdentities(append(slices.Clone(cost.BeforeState), cost.TargetState...), profileIdentities)
		for _, state := range []string{cost.From, cost.To} {
			id, _, _ := strings.Cut(state, ":")
			if cell := profileIdentities[id]; cell != "" {
				if cost.StateIdentities == nil {
					cost.StateIdentities = map[string]string{}
				}
				cost.StateIdentities[id] = cell
			}
		}
	}
	for index := range view.Baselines {
		baseline := &view.Baselines[index]
		baseline.DeviceClass = nodeClasses[baseline.Node.UID]
		baseline.StateIdentities = retainedStateIdentities(baseline.State, profileIdentities)
	}
	view.Costs = latestCosts(view.Costs)
	view.Baselines = latestBaselines(view.Baselines)
	slices.Sort(view.Sources)
	return view, nil
}

func (e *CostEvidence) refreshPower(ctx context.Context, namespaces []string, view *CostView) error {
	listed := map[string]bool{}
	for _, namespace := range namespaces {
		measurements := &edge.PowerModeTransitionCostList{}
		if err := e.store.client.List(ctx, measurements, client.InNamespace(namespace)); err != nil {
			return err
		}
		for i := range measurements.Items {
			measurement := &measurements.Items[i]
			result := measurement.Status.Result
			if measurement.DeletionTimestamp != nil || measurement.Status.ObservedGeneration != measurement.Generation ||
				measurement.Status.Phase != edge.PowerModeTransitionCostPhaseComplete || result == nil {
				continue
			}
			name := measurement.Namespace + "/" + measurement.Name
			if reason, invalid := measurement.Annotations[profilingrun.InvalidAnnotation]; invalid {
				view.Issues = append(view.Issues, CostIssue{Source: name, Code: measurementInvalidCode, Reason: reason})
				continue
			}
			identity := name + "/" + string(measurement.UID) + "/" + result.Store + "/" + result.Key
			listed[identity] = true
			view.Sources = append(view.Sources, identity)
			evidence, cached := e.power[identity]
			if !cached {
				var err error
				if evidence, err = e.loadPowerCost(ctx, measurement); err != nil {
					view.Issues = append(view.Issues, CostIssue{Source: name, Code: CostEvidenceInvalid, Reason: err.Error()})
					continue
				}
				e.power[identity] = evidence
			}
			view.Costs = append(view.Costs, evidence.costs...)
		}
	}
	maps.DeleteFunc(e.power, func(identity string, _ powerEvidence) bool { return !listed[identity] })
	return nil
}

// refreshOccupancy keeps the latest accepted shared-execution occupancy record
// per Node, power mode and model; a negative d counts as 0.
func (e *CostEvidence) refreshOccupancy(ctx context.Context, view *CostView) error {
	identities, err := e.objects.List(ctx, DispatchOccupancyPrefix)
	if err != nil {
		return err
	}
	listed := map[evidencestore.ObjectIdentity]bool{}
	type measured struct {
		record *DispatchOccupancyRecord
		key    string
	}
	var accepted []measured
	for _, identity := range identities {
		listed[identity] = true
		view.Sources = append(view.Sources, fmt.Sprintf("%s/%s/%d", identity.Store, identity.Key, identity.Size))
		record, cached := e.occupancy[identity]
		if !cached {
			raw, readErr := boundedCostRead(ctx, e.objects, identity, e.maxBytes)
			record = &DispatchOccupancyRecord{}
			if readErr == nil {
				readErr = decodeCostJSON(raw, record)
			}
			if readErr != nil {
				view.Issues = append(view.Issues, CostIssue{Source: identity.Key, Code: CostEvidenceInvalid, Reason: readErr.Error()})
				continue
			}
			e.occupancy[identity] = record
		}
		if record.Verdict == servingstate.MeasurementAccepted {
			accepted = append(accepted, measured{record: record, key: identity.Key})
		}
	}
	maps.DeleteFunc(e.occupancy, func(identity evidencestore.ObjectIdentity, _ *DispatchOccupancyRecord) bool { return !listed[identity] })
	latest := latestBy(accepted, func(m measured) string {
		r := m.record
		return strings.Join([]string{r.Node.UID, r.PowerMode, r.Model, r.RuntimeImage, r.ProfileID, r.ExecutionPath, strconv.Itoa(r.Batch)}, "\x00")
	}, func(m measured) (string, int64) { return m.key, m.record.MeasuredAt.UnixNano() })
	for _, m := range latest {
		view.DispatchOccupancy = append(view.DispatchOccupancy, serving.DispatchOccupancy{Node: m.record.Node, PowerMode: m.record.PowerMode,
			RuntimeImage: m.record.RuntimeImage, Model: m.record.Model, ProfileID: m.record.ProfileID,
			ExecutionPath: m.record.ExecutionPath, Batch: m.record.Batch, PerRequest: time.Duration(max(m.record.PerRequestNanos, 0)), Source: m.key})
	}
	return nil
}

// incompleteSequences returns the IDs of the operation sequences that stopped
// before their last request.
func (e *CostEvidence) incompleteSequences(ctx context.Context) (map[string]bool, error) {
	identities, err := e.objects.List(ctx, OperationSequencePrefix)
	if err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, identity := range identities {
		ids[strings.TrimSuffix(path.Base(identity.Key), ".json")] = true
	}
	return ids, nil
}

// readOperationEvidence returns an error only when the object cannot be read;
// a readable record that fails validation is retained with its error.
func (e *CostEvidence) readOperationEvidence(ctx context.Context, ref evidencestore.ObjectIdentity) (operationEvidence, error) {
	raw, err := boundedCostRead(ctx, e.objects, ref, e.maxBytes)
	if err != nil {
		return operationEvidence{}, err
	}
	var record servingops.OperationMeasurement
	evidence := operationEvidence{err: decodeCostJSON(raw, &record)}
	if evidence.err != nil {
		return evidence, nil
	}
	if record.Verdict != servingstate.MeasurementAccepted {
		if record.Isolation != nil && strings.HasPrefix(record.Isolation.Error, servingstate.PowerChangedReason) {
			return operationEvidence{skip: true, invalid: record.Isolation.Error}, nil
		}
		return operationEvidence{skip: true}, nil
	}
	if evidence.err = validateRetainedMeasurement(record); evidence.err != nil {
		return evidence, nil
	}
	if prepare := record.Request.Operation.Prepare; prepare != nil {
		evidence.profile = prepare.Instance.Profile
	}
	if record.Request.Operation.Kind == servingstate.OperationIdle {
		baseline, err := idleMeasurementBaseline(record, ref)
		evidence.baseline, evidence.err = &baseline, err
		return evidence, nil
	}
	cost, err := servingops.MeasurementCost(record, ref)
	evidence.cost, evidence.err = &cost, err
	return evidence, nil
}

// latestCosts keeps the latest cost per condition: Node, kind, from, to and
// the Node power mode for operations; Node, from, to and the loaded state for
// power. Equal times keep the smaller ID.
func latestCosts(costs []serving.OperationCost) []serving.OperationCost {
	key := func(c serving.OperationCost) string {
		condition := c.PowerMode
		if c.Kind == servingstate.OperationPower {
			states := make([]string, len(c.BeforeState))
			for j, state := range c.BeforeState {
				states[j] = serving.MeasurementStateIdentity(state, c.StateIdentities)
			}
			slices.Sort(states)
			condition = strings.Join(states, "\x00")
		}
		return strings.Join([]string{c.Node.UID, c.Kind, serving.MeasurementStateIdentity(c.From, c.StateIdentities), serving.MeasurementStateIdentity(c.To, c.StateIdentities), condition}, "\x01")
	}
	return latestBy(costs, key, func(c serving.OperationCost) (string, int64) { return c.ID, c.MeasuredAt.UnixNano() })
}

// latestBaselines keeps one unloaded or loaded baseline per Node, mode and state.
func latestBaselines(baselines []serving.NodeBaseline) []serving.NodeBaseline {
	key := func(b serving.NodeBaseline) string {
		states := make([]string, len(b.State))
		for j, state := range b.State {
			states[j] = serving.MeasurementStateIdentity(state, b.StateIdentities)
		}
		slices.Sort(states)
		return strings.Join(append([]string{b.Node.UID, b.PowerMode}, states...), "\x01")
	}
	return latestBy(baselines, key, func(b serving.NodeBaseline) (string, int64) { return b.ID, b.MeasuredAt.UnixNano() })
}

func latestBy[T any](items []T, key func(T) string, order func(T) (string, int64)) []T {
	indices := map[string]int{}
	var result []T
	for _, item := range items {
		k := key(item)
		index, found := indices[k]
		if !found {
			indices[k] = len(result)
			result = append(result, item)
			continue
		}
		id, at := order(item)
		oldID, oldAt := order(result[index])
		if at > oldAt || (at == oldAt && id < oldID) {
			result[index] = item
		}
	}
	return result
}
func validateRetainedMeasurement(record servingops.OperationMeasurement) error {
	if record.Result.Operation.ID != record.Request.Operation.ID || record.Result.Operation.Node != record.Request.Operation.Node || record.Result.Operation.Kind != record.Request.Operation.Kind || !record.Result.EndedAt.After(record.Result.StartedAt) {
		return errors.New("raw operation conditions/window mismatch")
	}
	if record.Isolation != nil && (record.Isolation.Error != "" || record.Isolation.Node != record.Request.Operation.Node || record.Isolation.CheckedAt.Before(record.Result.EndedAt)) {
		return errors.New("operation isolation boundary failed")
	}
	if err := record.Request.Sampling.Validate(); err != nil {
		return err
	}
	if _, err := record.Request.PowerTimingAllowance(); err != nil {
		return err
	}
	meter := record.Request.Meter
	if meter.ID == "" || meter.Scope != servingstate.MeasurementWallScope || meter.Node != record.Request.Operation.Node || meter.Source.NodeName != meter.Node.Name || !reflect.DeepEqual(record.Power.Source, meter.Source) {
		return errors.New("raw operation meter scope/node mismatch")
	}
	return nil
}
func idleMeasurementBaseline(record servingops.OperationMeasurement, ref evidencestore.ObjectIdentity) (serving.NodeBaseline, error) {
	if record.SchemaVersion != servingops.OperationMeasurementSchema || record.Verdict != servingstate.MeasurementAccepted || record.MeanWatts == nil || math.IsNaN(*record.MeanWatts) || math.IsInf(*record.MeanWatts, 0) || *record.MeanWatts < 0 || len(record.StateObservations) < 2 {
		return serving.NodeBaseline{}, errors.New("idle baseline lacks accepted measured stability evidence")
	}
	for _, state := range record.StateObservations {
		if err := servingops.StableIdle(record.Result.Before, state); err != nil {
			return serving.NodeBaseline{}, err
		}
	}
	measurement, err := record.EnergyMeasurement()
	if err != nil {
		return serving.NodeBaseline{}, err
	}
	if record.WallJoules == nil || math.IsNaN(*record.WallJoules) || math.IsInf(*record.WallJoules, 0) || *record.WallJoules < 0 || record.Result.Status != servingstate.NativeOperationComplete {
		return serving.NodeBaseline{}, errors.New("idle aggregate lacks finite completed energy")
	}
	keys := servingops.StateProfileKeys(record.Result.Actual)
	slices.Sort(keys)
	meter := record.Request.Meter
	return serving.NodeBaseline{ID: record.Request.Operation.ID, Node: meter.Node, PowerMode: record.Result.Actual.PowerMode, State: keys, Watts: measurement.Joules / record.Result.EndedAt.Sub(record.Result.StartedAt).Seconds(), WattsUncertainty: measurement.UncertaintyJoules / record.Result.EndedAt.Sub(record.Result.StartedAt).Seconds(), Meter: meter.ID, Scope: meter.Scope, MeasuredAt: record.Result.EndedAt, Reference: ref}, nil
}

func retainedStateIdentities(states []string, identities map[string]string) map[string]string {
	var retained map[string]string
	for _, state := range states {
		id, _, _ := strings.Cut(state, ":")
		if cell := identities[id]; cell != "" {
			if retained == nil {
				retained = map[string]string{}
			}
			retained[id] = cell
		}
	}
	return retained
}
