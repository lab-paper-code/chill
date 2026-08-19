package evidence

import (
	"errors"
	"fmt"
	"reflect"
	"strings"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func (e ExecutionEvidence) validateActuation() error {
	if e.Intent.Actuation == nil {
		if e.Actuation != nil {
			return permanentValidationError(errors.New("fixed Run cannot contain actuation facts"))
		}
		return nil
	}
	if e.Actuation == nil {
		return permanentValidationError(errors.New("actuated Run requires finalized actuation facts"))
	}
	return e.Actuation.validate(e.Intent, e.Terminal)
}

func (a ActuationFacts) validate(
	intent profilingrun.Intent,
	terminal TerminalFacts,
) error {
	if err := validateNamespacedObject(a.Run, "Run"); err != nil {
		return permanentValidationError(err)
	}
	if err := validateNamespacedObject(a.Handoff, "handoff"); err != nil {
		return permanentValidationError(err)
	}
	if a.Run.Namespace != terminal.Job.Namespace || a.Handoff.Namespace != a.Run.Namespace {
		return permanentValidationError(errors.New("actuation Run, handoff, and terminal Job namespaces do not match"))
	}
	if err := a.Target.validate("target"); err != nil {
		return permanentValidationError(err)
	}
	if intent.Actuation == nil || a.Target.PowerMode != intent.Actuation.PowerMode ||
		!reflect.DeepEqual(a.Target.Actuator, intent.Actuation.Actuator) {
		return permanentValidationError(errors.New("actuation target observation does not match frozen intent"))
	}
	if a.ReadyAt.IsZero() {
		return permanentValidationError(errors.New("actuation transition completion time is required"))
	}
	if a.ReadyAt.Before(a.Target.ObservedAt) {
		return permanentValidationError(errors.New("actuation read-back and completion timestamps are out of order"))
	}
	for _, container := range terminal.Containers {
		if container.StartedAt.Before(a.ReadyAt) {
			return permanentValidationf("container %q started before actuation became ready", container.Name)
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
