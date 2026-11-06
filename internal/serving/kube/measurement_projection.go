package kube

import (
	"context"
	"errors"
	"fmt"
	"io"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// OperationStepProjection contains no raw power, preparation or private payload.
// The immutable record named by a saved CR step supplies its stored facts.
type OperationStepProjection struct {
	OperationID   string
	Verdict       string
	DurationNanos *int64
	WallJoules    *float64
	Reason        string
	FromPowerMode string
	ToPowerMode   string
	Model         string
}

// LoadOperationCostSteps reads recorded steps, not latest accepted costs. Failed
// and insufficient attempts are history too, even without an actual window.
func LoadOperationCostSteps(ctx context.Context, cr edge.OperationCostMeasurement, objects evidencestore.LocationReader, store string, maxBytes int64) ([]OperationStepProjection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	steps := make([]OperationStepProjection, len(cr.Status.Steps))
	for i, saved := range cr.Status.Steps {
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
		var record servingops.OperationMeasurement
		if err := decodeCostJSON(raw, &record); err != nil {
			return nil, fmt.Errorf("decode operation evidence: %w", err)
		}
		projection, err := projectOperationStep(record)
		if err != nil {
			return nil, err
		}
		steps[i] = projection
	}
	return steps, nil
}

func projectOperationStep(record servingops.OperationMeasurement) (OperationStepProjection, error) {
	if record.SchemaVersion != servingops.OperationMeasurementSchema {
		return OperationStepProjection{}, errors.New("unsupported operation measurement schema")
	}
	operation, result := record.Request.Operation, record.Result
	p := OperationStepProjection{OperationID: operation.ID, Verdict: record.Verdict, Reason: record.Reason}
	p.FromPowerMode, p.ToPowerMode = result.Before.PowerMode, result.Actual.PowerMode
	if operation.Prepare != nil && operation.Prepare.Instance.Profile != nil {
		p.Model = operation.Prepare.Instance.Profile.Condition.LogicalModel
	}
	if !result.StartedAt.IsZero() && !result.EndedAt.IsZero() && result.EndedAt.After(result.StartedAt) {
		duration := result.EndedAt.Sub(result.StartedAt).Nanoseconds()
		p.DurationNanos = &duration
	}
	switch record.Verdict {
	case servingstate.MeasurementAccepted:
		p.WallJoules = record.WallJoules
	case string(edge.MeasurementPhaseFailed), servingops.MeasurementInsufficient:
		// Failed/insufficient records need not have accepted power, isolation or
		// complete state effects. Their actual interval remains useful history.
	default:
		return OperationStepProjection{}, errors.New("unsupported operation measurement verdict")
	}
	return p, nil
}

