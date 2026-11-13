package evidence

import (
	"encoding/json"
	"errors"
	"fmt"
)

func (e ExecutionEvidence) Validate(raw RawMeasurement) error {
	if err := raw.validate(); err != nil {
		return fmt.Errorf("validate raw measurement: %w", err)
	}
	if err := e.RawMeasurement.Validate(); err != nil {
		return fmt.Errorf("validate raw measurement identity: %w", err)
	}
	if err := raw.validateExecution(e); err != nil {
		return err
	}
	return nil
}

func (e ExecutionEvidence) ValidateIdle(raw IdleRawMeasurement) error {
	if err := raw.validate(); err != nil {
		return fmt.Errorf("validate idle raw measurement: %w", err)
	}
	if err := e.RawMeasurement.Validate(); err != nil {
		return fmt.Errorf("validate raw measurement identity: %w", err)
	}
	if err := raw.validateExecution(e); err != nil {
		return err
	}
	return nil
}

func (e ExecutionEvidence) validateLinkedRaw(rawPodUID string) error {
	if !IsSupportedExecutionEvidenceSchemaVersion(e.SchemaVersion) {
		return fmt.Errorf("unsupported execution evidence schemaVersion %q", e.SchemaVersion)
	}
	if err := e.Terminal.validate(e.RunConfig, rawPodUID); err != nil {
		return err
	}
	if err := e.validateActuation(); err != nil {
		return fmt.Errorf("validate execution actuation: %w", err)
	}
	if e.FinalizedAt.IsZero() || e.FinalizedAt.Before(e.Terminal.Job.CompletionTime) {
		return errors.New("execution evidence finalization time is invalid")
	}
	return nil
}

func (e ExecutionEvidence) CanonicalJSON(raw RawMeasurement) ([]byte, error) {
	if err := e.Validate(raw); err != nil {
		return nil, err
	}
	return e.canonicalJSON()
}

func (e ExecutionEvidence) CanonicalIdleJSON(raw IdleRawMeasurement) ([]byte, error) {
	if err := e.ValidateIdle(raw); err != nil {
		return nil, err
	}
	return e.canonicalJSON()
}

func (e ExecutionEvidence) canonicalJSON() ([]byte, error) {
	canonical := e
	canonical.FinalizedAt = canonical.FinalizedAt.UTC()
	canonical.Terminal.Job.CompletionTime = canonical.Terminal.Job.CompletionTime.UTC()
	if canonical.Actuation != nil {
		actuation := *canonical.Actuation
		actuation.Target.ObservedAt = actuation.Target.ObservedAt.UTC()
		actuation.ReadyAt = actuation.ReadyAt.UTC()
		canonical.Actuation = &actuation
	}
	return json.Marshal(canonical)
}
