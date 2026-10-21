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
	"strings"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// OperationMeasurementPrefix holds every serving-operation measurement record.
const OperationMeasurementPrefix = "serving/operation-measurements/"

// Cost issue codes.
const (
	CostEvidenceInvalid = "CostEvidenceInvalid"
	MeterUndeclared     = "MeterUndeclared"
)

// OperationMeasurementKey is the fixed store key of one operation measurement.
func OperationMeasurementKey(operation Operation) string {
	return OperationMeasurementPrefix + operation.Node.UID + "/" + operation.ID + ".json"
}

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
	meter    MeterDeclaration
	cost     *serving.OperationCost
	baseline *serving.NodeBaseline
	err      error
	skip     bool // a recorded non-Accepted attempt: valid history, never a cost or issue
	// invalid is the isolation failure of a record another power change invalidated.
	invalid string
}

type powerEvidence struct {
	node   serving.NodeReference
	source metricobserver.SourceIdentity
	costs  []serving.OperationCost
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
func (e *CostEvidence) Refresh(ctx context.Context, namespaces []string, meters []MeterDeclaration, profiles []serving.Profile) (CostView, error) {
	view := CostView{Issues: []CostIssue{}}
	identities, err := e.objects.List(ctx, OperationMeasurementPrefix)
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
		case !slices.ContainsFunc(meters, func(m MeterDeclaration) bool { return reflect.DeepEqual(m, evidence.meter) }):
			view.Issues = append(view.Issues, CostIssue{Source: identity.Key, Code: MeterUndeclared, Reason: "record meter " + evidence.meter.ID + " differs from every configured meter"})
		case evidence.baseline != nil:
			view.Baselines = append(view.Baselines, *evidence.baseline)
		default:
			view.Costs = append(view.Costs, *evidence.cost)
		}
	}
	maps.DeleteFunc(e.measurements, func(identity evidencestore.ObjectIdentity, _ operationEvidence) bool { return !listed[identity] })
	if err = e.refreshPower(ctx, namespaces, meters, profiles, &view); err != nil {
		return CostView{}, err
	}
	if err = e.refreshOccupancy(ctx, &view); err != nil {
		return CostView{}, err
	}
	view.Costs = latestCosts(view.Costs)
	view.Baselines = latestBaselines(view.Baselines)
	slices.Sort(view.Sources)
	return view, nil
}

func (e *CostEvidence) refreshPower(ctx context.Context, namespaces []string, meters []MeterDeclaration, profiles []serving.Profile, view *CostView) error {
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
				if evidence, err = e.loadPowerCost(ctx, measurement, profiles); err != nil {
					view.Issues = append(view.Issues, CostIssue{Source: name, Code: CostEvidenceInvalid, Reason: err.Error()})
					continue
				}
				e.power[identity] = evidence
			}
			i := slices.IndexFunc(meters, func(m MeterDeclaration) bool {
				return m.Node == evidence.node && reflect.DeepEqual(m.Source, evidence.source)
			})
			if i < 0 {
				view.Issues = append(view.Issues, CostIssue{Source: name, Code: MeterUndeclared, Reason: "no configured meter has the measured Node and power source"})
				continue
			}
			for _, cost := range evidence.costs {
				cost.Meter, cost.Scope = meters[i].ID, meters[i].Scope
				view.Costs = append(view.Costs, cost)
			}
		}
	}
	maps.DeleteFunc(e.power, func(identity string, _ powerEvidence) bool { return !listed[identity] })
	return nil
}

// refreshOccupancy keeps the latest accepted dispatch occupancy record per
// Node, power mode, runtime image and model; a negative d counts as 0.
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
		if record.Verdict == measurementAccepted {
			accepted = append(accepted, measured{record: record, key: identity.Key})
		}
	}
	maps.DeleteFunc(e.occupancy, func(identity evidencestore.ObjectIdentity, _ *DispatchOccupancyRecord) bool { return !listed[identity] })
	latest := latestBy(accepted, func(m measured) string {
		return strings.Join([]string{m.record.Node.UID, m.record.PowerMode, m.record.RuntimeImage, m.record.Model}, "\x00")
	}, func(m measured) (string, int64) { return m.key, m.record.MeasuredAt.UnixNano() })
	for _, m := range latest {
		view.DispatchOccupancy = append(view.DispatchOccupancy, serving.DispatchOccupancy{Node: m.record.Node, PowerMode: m.record.PowerMode,
			RuntimeImage: m.record.RuntimeImage, Model: m.record.Model, PerRequest: time.Duration(max(m.record.PerRequestNanos, 0)), Source: m.key})
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
	var record OperationMeasurement
	evidence := operationEvidence{err: decodeCostJSON(raw, &record)}
	if evidence.err != nil {
		return evidence, nil
	}
	if record.Verdict != measurementAccepted {
		if record.Isolation != nil && strings.HasPrefix(record.Isolation.Error, powerChangedReason) {
			return operationEvidence{skip: true, invalid: record.Isolation.Error}, nil
		}
		return operationEvidence{skip: true}, nil
	}
	evidence.meter = record.Request.Meter
	if evidence.err = validateRetainedMeasurement(record, ref); evidence.err != nil {
		return evidence, nil
	}
	if record.Request.Operation.Kind == operationIdle {
		baseline, err := idleMeasurementBaseline(record, ref)
		evidence.baseline, evidence.err = &baseline, err
		return evidence, nil
	}
	cost, err := measurementCost(record, ref)
	evidence.cost, evidence.err = &cost, err
	return evidence, nil
}

// latestCosts keeps the latest cost per condition: Node, kind, from, to and
// the Node power mode for operations; Node, from, to and the loaded state for
// power. Equal times keep the smaller ID.
func latestCosts(costs []serving.OperationCost) []serving.OperationCost {
	key := func(c serving.OperationCost) string {
		condition := c.PowerMode
		if c.Kind == operationPower {
			condition = strings.Join(c.BeforeState, "\x00")
		}
		return strings.Join([]string{c.Node.UID, c.Kind, c.From, c.To, condition}, "\x01")
	}
	return latestBy(costs, key, func(c serving.OperationCost) (string, int64) { return c.ID, c.MeasuredAt.UnixNano() })
}

// latestBaselines keeps one unloaded or loaded baseline per Node, mode and state.
func latestBaselines(baselines []serving.NodeBaseline) []serving.NodeBaseline {
	key := func(b serving.NodeBaseline) string {
		return strings.Join(append([]string{b.Node.UID, b.PowerMode}, b.State...), "\x01")
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
func validateRetainedMeasurement(record OperationMeasurement, ref evidencestore.ObjectIdentity) error {
	if ref.Key != OperationMeasurementKey(record.Request.Operation) {
		return errors.New("record is not at its fixed operation measurement key")
	}
	if record.Result.Operation.ID != record.Request.Operation.ID || record.Result.Operation.Node != record.Request.Operation.Node || record.Result.Operation.Kind != record.Request.Operation.Kind || record.Request.Coverage.MaximumBoundaryDistance <= 0 || record.Request.Coverage.MaximumGap <= 0 || record.Request.Coverage.MinimumCoverage <= 0 || record.Request.Coverage.MinimumCoverage > 1 || !record.Result.EndedAt.After(record.Result.StartedAt) {
		return errors.New("raw operation conditions/window mismatch")
	}
	if record.Isolation != nil && (record.Isolation.Error != "" || record.Isolation.Node != record.Request.Operation.Node || record.Isolation.CheckedAt.Before(record.Result.EndedAt)) {
		return errors.New("operation isolation boundary failed")
	}
	if err := record.Request.Sampling.Validate(); err != nil {
		return err
	}
	meter := record.Request.Meter
	if meter.ID == "" || meter.Scope != measurementWallScope || meter.Node != record.Request.Operation.Node || meter.Source.NodeName != meter.Node.Name || !reflect.DeepEqual(record.Power.Source, meter.Source) {
		return errors.New("raw operation meter scope/node mismatch")
	}
	return nil
}
func idleMeasurementBaseline(record OperationMeasurement, ref evidencestore.ObjectIdentity) (serving.NodeBaseline, error) {
	if record.SchemaVersion != OperationMeasurementSchema || record.Verdict != measurementAccepted || record.MeanWatts == nil || math.IsNaN(*record.MeanWatts) || math.IsInf(*record.MeanWatts, 0) || *record.MeanWatts < 0 || len(record.StateObservations) < 2 {
		return serving.NodeBaseline{}, errors.New("idle baseline lacks accepted measured stability evidence")
	}
	for _, state := range record.StateObservations {
		if err := stableIdle(record.Result.Before, state); err != nil {
			return serving.NodeBaseline{}, err
		}
	}
	samples := make([]powerwindow.Sample, len(record.Power.Samples))
	for i, s := range record.Power.Samples {
		samples[i] = powerwindow.Sample{ObservedAt: s.ObservedAt, Watts: s.Watts}
	}
	joules, err := powerwindow.Integrate(powerwindow.Window{Start: record.Result.StartedAt, End: record.Result.EndedAt, Samples: samples}, record.Request.Coverage)
	if err != nil {
		return serving.NodeBaseline{}, err
	}
	if record.WallJoules == nil || joules != *record.WallJoules || joules/record.Result.EndedAt.Sub(record.Result.StartedAt).Seconds() != *record.MeanWatts || record.Result.Status != nativeOperationComplete {
		return serving.NodeBaseline{}, errors.New("idle aggregate differs from retained raw integral/window")
	}
	keys := stateProfileKeys(record.Result.Actual)
	slices.Sort(keys)
	meter := record.Request.Meter
	return serving.NodeBaseline{ID: record.Request.Operation.ID, Node: meter.Node, PowerMode: record.Result.Actual.PowerMode, State: keys, Watts: *record.MeanWatts, Meter: meter.ID, Scope: meter.Scope, MeasuredAt: record.Result.EndedAt, Reference: ref}, nil
}
