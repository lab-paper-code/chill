package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
)

const measurementResultSchemaVersion = "chill.dacs.io/transition-cost-measurement-result.v1alpha1"

type measurementResultStore interface {
	evidencestore.LocationReader
	evidencestore.ImmutableWriter
}

type measurementResultWriter struct {
	store              measurementResultStore
	maximumObjectBytes int64
}

type storedMeasurementResult struct {
	reference edgev1alpha1.ProfilingRunEvidenceReference
	record    measurementResultRecord
}

type measurementResultRecord struct {
	SchemaVersion string                              `json:"schemaVersion"`
	Outcome       measurementStage                    `json:"outcome"`
	Reason        string                              `json:"reason,omitempty"`
	Directions    []measurementResultDirection        `json:"directions"`
	DirectedTable *transitioncost.DirectedTableRecord `json:"directedTable,omitempty"`
}

type measurementResultDirection struct {
	AcceptedCount int32                          `json:"acceptedCount"`
	FinalEvidence []evidencestore.ObjectIdentity `json:"finalEvidence"`
}

func newMeasurementResultWriter(
	store measurementResultStore,
	maximumObjectBytes int64,
) (*measurementResultWriter, error) {
	if store == nil {
		return nil, errors.New("transition measurement result store is required")
	}
	if maximumObjectBytes < 1 || maximumObjectBytes == 1<<63-1 {
		return nil, errors.New("maximum transition measurement result size is invalid")
	}
	return &measurementResultWriter{store: store, maximumObjectBytes: maximumObjectBytes}, nil
}

func (w *measurementResultWriter) write(
	ctx context.Context,
	measurement *edgev1alpha1.PowerModeTransitionCost,
	progress measurementProgress,
) (storedMeasurementResult, error) {
	location, err := measurementResultLocation(measurement)
	if err != nil {
		return storedMeasurementResult{}, err
	}
	if existing, found, err := w.load(ctx, location); err != nil {
		return storedMeasurementResult{}, err
	} else if found {
		return existing, nil
	}

	record, err := buildMeasurementResult(progress)
	if err != nil {
		return storedMeasurementResult{}, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return storedMeasurementResult{}, fmt.Errorf("encode transition measurement result: %w", err)
	}
	if int64(len(payload)) > w.maximumObjectBytes {
		return storedMeasurementResult{}, fmt.Errorf(
			"transition measurement result exceeds %d-byte limit", w.maximumObjectBytes,
		)
	}
	identity := evidencestore.ObjectIdentity{
		Store: location.Store, Key: location.Key, Size: int64(len(payload)),
	}
	receipt, err := w.store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return storedMeasurementResult{}, fmt.Errorf("create transition measurement result: %w", err)
	}
	if err := receipt.Validate(); err != nil {
		return storedMeasurementResult{}, fmt.Errorf("validate transition measurement result receipt: %w", err)
	}
	if receipt.Object != identity {
		return storedMeasurementResult{}, errors.New(
			"transition measurement result writer returned an unexpected object reference",
		)
	}
	if receipt.Outcome == evidencestore.CreateOutcomeExisting {
		winner, found, err := w.load(ctx, location)
		if err != nil {
			return storedMeasurementResult{}, err
		}
		if !found {
			return storedMeasurementResult{}, errors.New("existing transition measurement result disappeared")
		}
		return winner, nil
	}
	return storedMeasurementResult{reference: measurementResultReference(identity), record: record}, nil
}

func (w *measurementResultWriter) load(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (storedMeasurementResult, bool, error) {
	payload, err := readBoundedTransitionObject(ctx, w.store, location, w.maximumObjectBytes)
	if errors.Is(err, evidencestore.ErrNotFound) {
		return storedMeasurementResult{}, false, nil
	}
	if err != nil {
		return storedMeasurementResult{}, false, fmt.Errorf("read transition measurement result: %w", err)
	}
	record, err := decodeMeasurementResult(payload)
	if err != nil {
		return storedMeasurementResult{}, false, err
	}
	identity := evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: int64(len(payload))}
	return storedMeasurementResult{
		reference: measurementResultReference(identity), record: record,
	}, true, nil
}

func measurementResultLocation(
	measurement *edgev1alpha1.PowerModeTransitionCost,
) (evidencestore.ObjectLocation, error) {
	if measurement == nil || !cleanMeasurementResultText(string(measurement.UID)) {
		return evidencestore.ObjectLocation{}, errors.New("transition measurement and UID are required")
	}
	location := evidencestore.ObjectLocation{
		Store: measurement.Spec.EvidenceStore,
		Key:   fmt.Sprintf("power-mode-transition-costs/%s/result.json", measurement.UID),
	}
	if err := location.Validate(); err != nil {
		return evidencestore.ObjectLocation{}, fmt.Errorf("validate transition measurement result location: %w", err)
	}
	return location, nil
}

func buildMeasurementResult(progress measurementProgress) (measurementResultRecord, error) {
	record := measurementResultRecord{
		SchemaVersion: measurementResultSchemaVersion,
		Outcome:       progress.stage,
		Reason:        progress.reason,
		Directions:    measurementResultDirections(progress.attempts),
	}
	switch progress.stage {
	case measurementStageComplete:
		if progress.table == nil || progress.reason != "" {
			return measurementResultRecord{}, errors.New("complete transition measurement result is incomplete")
		}
		table := progress.table.Record()
		record.DirectedTable = &table
	case measurementStageInsufficient, measurementStageFailed:
		if progress.table != nil || !cleanMeasurementResultText(progress.reason) {
			return measurementResultRecord{}, errors.New("non-complete transition measurement result is incomplete")
		}
	default:
		return measurementResultRecord{}, fmt.Errorf("transition measurement stage %q is not terminal", progress.stage)
	}
	return record, nil
}

func measurementResultDirections(attempts []FinalizedAttempt) []measurementResultDirection {
	directions := []measurementResultDirection{
		{FinalEvidence: make([]evidencestore.ObjectIdentity, 0, (len(attempts)+1)/2)},
		{FinalEvidence: make([]evidencestore.ObjectIdentity, 0, len(attempts)/2)},
	}
	for index, attempt := range attempts {
		direction := &directions[index%2]
		if attempt.Observation.Verdict() == transitioncost.VerdictAccepted {
			direction.AcceptedCount++
		}
		direction.FinalEvidence = append(direction.FinalEvidence, attempt.FinalReference)
	}
	return directions
}

func decodeMeasurementResult(payload []byte) (measurementResultRecord, error) {
	var record measurementResultRecord
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return measurementResultRecord{}, fmt.Errorf("decode transition measurement result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return measurementResultRecord{}, errors.New("transition measurement result contains a trailing JSON value")
	}
	if err := validateStoredMeasurementResult(record); err != nil {
		return measurementResultRecord{}, err
	}
	return record, nil
}

func validateStoredMeasurementResult(record measurementResultRecord) error {
	if record.SchemaVersion != measurementResultSchemaVersion {
		return fmt.Errorf("unsupported transition measurement result schema %q", record.SchemaVersion)
	}
	if len(record.Directions) != 2 {
		return errors.New("transition measurement result must contain exactly two directions")
	}
	for index, direction := range record.Directions {
		if direction.AcceptedCount < 0 || int64(direction.AcceptedCount) > int64(len(direction.FinalEvidence)) ||
			direction.FinalEvidence == nil {
			return fmt.Errorf("transition measurement result direction %d progress is invalid", index)
		}
		for _, reference := range direction.FinalEvidence {
			if err := reference.Validate(); err != nil {
				return fmt.Errorf("validate transition measurement result direction %d final evidence: %w", index, err)
			}
		}
	}
	switch record.Outcome {
	case measurementStageComplete:
		if record.Reason != "" || record.DirectedTable == nil {
			return errors.New("complete transition measurement result is incomplete")
		}
		table, err := transitioncost.RestoreDirectedTable(*record.DirectedTable)
		if err != nil {
			return fmt.Errorf("restore transition measurement directed table: %w", err)
		}
		entries := table.Record().Entries
		for index, direction := range record.Directions {
			if entries[index].AttemptCount != len(direction.FinalEvidence) ||
				int64(entries[index].AcceptedCount) != int64(direction.AcceptedCount) {
				return fmt.Errorf("transition measurement result direction %d disagrees with its table", index)
			}
		}
	case measurementStageInsufficient, measurementStageFailed:
		if !cleanMeasurementResultText(record.Reason) || record.DirectedTable != nil {
			return errors.New("non-complete transition measurement result is incomplete")
		}
	default:
		return fmt.Errorf("unsupported transition measurement result outcome %q", record.Outcome)
	}
	return nil
}

func measurementResultReference(identity evidencestore.ObjectIdentity) edgev1alpha1.ProfilingRunEvidenceReference {
	return edgev1alpha1.ProfilingRunEvidenceReference{
		Store: identity.Store, Key: identity.Key, SchemaVersion: measurementResultSchemaVersion, Size: identity.Size,
	}
}

func cleanMeasurementResultText(value string) bool {
	return strings.TrimSpace(value) != "" && strings.TrimSpace(value) == value
}
