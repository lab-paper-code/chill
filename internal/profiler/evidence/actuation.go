package evidence

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

func (e ExecutionEvidence) validateActuation() error {
	if e.Intent.Actuation == nil {
		if e.SchemaVersion != ExecutionEvidenceSchemaVersion {
			return fmt.Errorf("fixed Run requires execution evidence schemaVersion %q", ExecutionEvidenceSchemaVersion)
		}
		if e.Actuation != nil {
			return errors.New("fixed Run cannot contain actuation facts")
		}
		return nil
	}
	if e.SchemaVersion != ActuatedExecutionEvidenceSchemaVersion {
		return fmt.Errorf(
			"actuated Run requires execution evidence schemaVersion %q",
			ActuatedExecutionEvidenceSchemaVersion,
		)
	}
	if e.Actuation == nil {
		return errors.New("actuated Run requires finalized actuation facts")
	}
	return e.Actuation.validate(e.Intent, e.Terminal, e.FinalizedAt)
}

func (a ActuationFacts) validate(
	intent profilingrun.Intent,
	terminal TerminalFacts,
	finalizedAt time.Time,
) error {
	if err := validateNamespacedObject(a.Run, "Run"); err != nil {
		return err
	}
	if err := validateNamespacedObject(a.Handoff, "handoff"); err != nil {
		return err
	}
	if a.Run.Namespace != terminal.Job.Namespace || a.Handoff.Namespace != a.Run.Namespace {
		return errors.New("actuation Run, handoff, and terminal Job namespaces do not match")
	}
	if err := a.Previous.validate("previous"); err != nil {
		return err
	}
	if err := a.Target.validate("target"); err != nil {
		return err
	}
	if err := a.Restored.validate("restored"); err != nil {
		return err
	}
	if intent.Actuation == nil || a.Target.PowerMode != intent.Actuation.PowerMode ||
		!reflect.DeepEqual(a.Target.Actuator, intent.Actuation.Actuator) {
		return errors.New("actuation target observation does not match frozen intent")
	}
	if a.Restored.PowerMode != a.Previous.PowerMode ||
		!reflect.DeepEqual(a.Restored.Actuator, a.Previous.Actuator) {
		return errors.New("restored observation does not match retained previous state")
	}
	if a.ReadyAt.IsZero() || a.RestoreRequestedAt.IsZero() || a.RestoredAt.IsZero() {
		return errors.New("actuation lifecycle timestamps are required")
	}
	if a.Target.ObservedAt.Before(a.Previous.ObservedAt) || a.ReadyAt.Before(a.Target.ObservedAt) {
		return errors.New("actuation apply/read-back/ready timestamps are out of order")
	}
	if terminal.Job.CompletionTime.After(a.RestoreRequestedAt) {
		return errors.New("actuation restore request predates measurement Job completion")
	}
	if a.Restored.ObservedAt.Before(a.RestoreRequestedAt) || a.RestoredAt.Before(a.Restored.ObservedAt) ||
		finalizedAt.Before(a.RestoredAt) {
		return errors.New("actuation restore/read-back/finalization timestamps are out of order")
	}
	for _, container := range terminal.Containers {
		if container.StartedAt.Before(a.ReadyAt) {
			return fmt.Errorf("container %q started before actuation became ready", container.Name)
		}
	}
	return nil
}

func (o ActuationObservation) validate(label string) error {
	if strings.TrimSpace(o.PowerMode) == "" || strings.TrimSpace(o.PowerMode) != o.PowerMode {
		return fmt.Errorf("%s actuation power mode is required without surrounding whitespace", label)
	}
	if err := o.Actuator.Validate(); err != nil {
		return fmt.Errorf("validate %s actuation target: %w", label, err)
	}
	if o.ObservedAt.IsZero() {
		return fmt.Errorf("%s actuation observation time is required", label)
	}
	return nil
}

func validateNamespacedObject(object NamespacedObjectFacts, label string) error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "namespace", value: object.Namespace},
		{name: "name", value: object.Name},
		{name: "UID", value: object.UID},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" || strings.TrimSpace(field.value) != field.value {
			return fmt.Errorf("actuation %s %s is required without surrounding whitespace", label, field.name)
		}
	}
	return nil
}
