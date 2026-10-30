package kube

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// OperationStepProjection contains no raw power, preparation or private payload.
// Only the exact immutable record named by a saved CR step can populate it.
type OperationStepProjection struct {
	OperationID   string
	DurationNanos *int64
	WallJoules    *float64
	Reason        string
}

// LoadOperationCostSteps reads recorded steps, not latest accepted costs. Failed
// and insufficient attempts are history too, even without an actual window.
func LoadOperationCostSteps(ctx context.Context, cr edge.OperationCostMeasurement, objects evidencestore.LocationReader, store string, maxBytes int64) ([]OperationStepProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(cr.Status.Steps) > len(cr.Spec.Steps) {
		return nil, errors.New("operation status has more steps than its specification")
	}
	steps := make([]OperationStepProjection, len(cr.Status.Steps))
	for i, saved := range cr.Status.Steps {
		if saved.Name != cr.Spec.Steps[i].Name {
			return nil, errors.New("operation status step index/name mismatch")
		}
		if saved.Key == "" {
			continue
		}
		if objects == nil || maxBytes < 1 || maxBytes == 1<<63-1 {
			return nil, errors.New("bounded operation Evidence Store access is required")
		}
		location := evidencestore.ObjectLocation{Store: store, Key: saved.Key}
		if err := location.Validate(); err != nil {
			return nil, err
		}
		body, err := objects.OpenLocation(ctx, location)
		if err != nil {
			return nil, err
		}
		raw, readErr := io.ReadAll(io.LimitReader(body, maxBytes+1))
		if err := errors.Join(readErr, body.Close(), ctx.Err()); err != nil {
			return nil, err
		}
		if int64(len(raw)) > maxBytes {
			return nil, errors.New("operation evidence exceeds bound")
		}
		var record OperationMeasurement
		if err := decodeCostJSON(raw, &record); err != nil {
			return nil, fmt.Errorf("decode operation evidence: %w", err)
		}
		projection, err := projectOperationStep(cr, i, store, record)
		if err != nil {
			return nil, err
		}
		steps[i] = projection
	}
	return steps, nil
}

func projectOperationStep(cr edge.OperationCostMeasurement, index int, store string, record OperationMeasurement) (OperationStepProjection, error) {
	step, saved := cr.Spec.Steps[index], cr.Status.Steps[index]
	operation := record.Request.Operation
	id := fmt.Sprintf("%s.%d.%s", cr.UID, index, step.Name)
	if cr.UID == "" || strings.ContainsAny(string(cr.UID)+step.Name+operation.Node.UID, "/\\") ||
		record.SchemaVersion != OperationMeasurementSchema || operation.ID != id || operation.Kind != string(step.Kind) ||
		operation.Node.Name != cr.Spec.Node || operation.Node.Name == "" || operation.Node.UID == "" ||
		record.Request.Store != store || saved.Key != OperationMeasurementKey(operation) || saved.Verdict != record.Verdict {
		return OperationStepProjection{}, errors.New("operation evidence CR/step/kind/Node/key identity mismatch")
	}
	switch step.Kind {
	case edge.OperationCostStepPower, edge.OperationCostStepPrepare, edge.OperationCostStepRemove, edge.OperationCostStepIdle:
	default:
		return OperationStepProjection{}, errors.New("unsupported operation measurement step kind")
	}
	result := record.Result
	// No first power sample means the operation was never invoked, so the writer
	// legitimately retained an empty result. An invoked result must bind exactly.
	emptyResult := result.Operation.ID == "" && result.Operation.Kind == "" && result.Operation.Node.Name == "" && result.Operation.Node.UID == "" && result.StartedAt.IsZero() && result.EndedAt.IsZero() && result.Status == ""
	if !emptyResult && (result.Operation.ID != operation.ID || result.Operation.Kind != operation.Kind || result.Operation.Node != operation.Node) {
		return OperationStepProjection{}, errors.New("operation request/result identity mismatch")
	}
	p := OperationStepProjection{OperationID: id, Reason: record.Reason}
	if !result.StartedAt.IsZero() && !result.EndedAt.IsZero() && result.EndedAt.After(result.StartedAt) {
		duration := result.EndedAt.Sub(result.StartedAt).Nanoseconds()
		p.DurationNanos = &duration
	}
	switch record.Verdict {
	case measurementAccepted:
		var err error
		p.WallJoules, err = acceptedOperationStepEnergy(record, evidencestore.ObjectIdentity{Store: store, Key: saved.Key})
		if err != nil {
			return OperationStepProjection{}, err
		}
	case operationFailed, measurementInsufficient:
		// Failed/insufficient records need not have accepted power, isolation or
		// complete state effects. Their actual interval remains useful history.
	default:
		return OperationStepProjection{}, errors.New("unsupported operation measurement verdict")
	}
	return p, nil
}

func acceptedOperationStepEnergy(record OperationMeasurement, ref evidencestore.ObjectIdentity) (*float64, error) {
	if record.Result.StartedAt.IsZero() {
		return nil, errors.New("accepted operation lacks actual result")
	}
	if err := validateRetainedMeasurement(record, ref); err != nil {
		return nil, err
	}
	if record.Request.Operation.Kind == operationIdle {
		if _, err := idleMeasurementBaseline(record, ref); err != nil {
			return nil, err
		}
	} else if _, err := measurementCost(record, ref); err != nil {
		return nil, err
	}
	return record.WallJoules, nil
}
