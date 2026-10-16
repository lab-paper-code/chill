package kube

import (
	"context"
	"errors"
	"math"
	"reflect"
	"slices"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
)

// LoadOperationMeasurements validates retained raw evidence once at selection;
// no missing/short trial becomes a usable zero. Power costs retain their existing
// directed transition-cost loader instead of masquerading as generic records.
func LoadOperationMeasurements(ctx context.Context, reader evidencestore.Reader, references []evidencestore.ObjectIdentity, maxBytes int64) ([]serving.OperationCost, []serving.NodeBaseline, error) {
	if reader == nil || maxBytes < 1 {
		return nil, nil, errors.New("bounded evidence reader required")
	}
	costs := make([]serving.OperationCost, 0, len(references))
	var baselines []serving.NodeBaseline
	meters := map[string]serving.NodeReference{}
	var failures []error
	series := make([]MeterDeclaration, 0, len(references))
	for _, ref := range references {
		record, err := readOperationMeasurement(ctx, reader, ref, maxBytes)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		if err := validateRetainedMeasurement(record); err != nil {
			failures = append(failures, err)
			continue
		}
		meter := record.Request.Meter
		if previous, ok := meters[meter.ID]; ok && previous != meter.Node {
			failures = append(failures, errors.New("one meter declared node-exclusive for multiple Nodes"))
			continue
		}
		meters[meter.ID] = meter.Node
		ambiguous := false
		for _, previous := range series {
			if previous.Node != meter.Node && sameWallSeries(previous.Source, meter.Source) {
				ambiguous = true
				break
			}
		}
		if ambiguous {
			failures = append(failures, errors.New("one physical wall series declared for multiple Nodes"))
			continue
		}
		series = append(series, meter)
		if record.Isolation != nil && (record.Isolation.Error != "" || record.Isolation.Node != record.Request.Operation.Node || len(record.Isolation.Workloads) != 0 || record.Isolation.CheckedAt.Before(record.Result.EndedAt)) {
			failures = append(failures, errors.New("operation isolation boundary failed"))
			continue
		}
		if record.Request.Operation.Kind == operationIdle {
			baseline, err := idleMeasurementBaseline(record, ref)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			baselines = append(baselines, baseline)
			continue
		}
		cost, err := measurementCost(record, ref)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		costs = append(costs, cost)
	}
	return costs, baselines, errors.Join(failures...)
}
func readOperationMeasurement(ctx context.Context, reader evidencestore.Reader, ref evidencestore.ObjectIdentity, maxBytes int64) (OperationMeasurement, error) {
	var record OperationMeasurement
	raw, err := boundedCostRead(ctx, reader, ref, maxBytes)
	if err != nil {
		return record, err
	}
	err = strictCostDecode(raw, &record)
	return record, err
}
func validateRetainedMeasurement(record OperationMeasurement) error {
	if record.Result.Operation.ID != record.Request.Operation.ID || record.Result.Operation.Node != record.Request.Operation.Node || record.Result.Operation.Kind != record.Request.Operation.Kind || record.Request.Coverage.MaximumBoundaryDistance <= 0 || record.Request.Coverage.MaximumGap <= 0 || record.Request.Coverage.MinimumCoverage <= 0 || record.Request.Coverage.MinimumCoverage > 1 || !record.Result.EndedAt.After(record.Result.StartedAt) {
		return errors.New("raw operation conditions/window mismatch")
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
