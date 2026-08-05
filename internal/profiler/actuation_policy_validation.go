package profiler

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

var (
	actuationPolicyDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func (s ActuationSelection) Validate() error {
	for name, value := range map[string]string{
		"DeviceClass": s.DeviceClass,
		"powerMode":   s.PowerMode,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("actuation selection %s is required without surrounding whitespace", name)
		}
	}
	if s.PowerMode == profilingrun.FixedPowerMode {
		return errors.New("fixed power mode does not require an actuation policy")
	}
	return s.Actuator.Validate()
}

func (f FrozenActuationPolicy) Validate() error {
	if f.SchemaVersion != FrozenActuationPolicySchemaVersion {
		return fmt.Errorf("unsupported frozen actuation-policy schema %q", f.SchemaVersion)
	}
	if err := f.Policy.Validate(); err != nil {
		return fmt.Errorf("validate frozen actuation policy: %w", err)
	}
	if !actuationPolicyDigestPattern.MatchString(f.Source.CatalogContentDigest) {
		return errors.New("actuation-policy source catalogContentDigest must be a canonical SHA-256 digest")
	}
	if f.Source.Kind != ConfigMapActuationPolicySourceKind {
		return fmt.Errorf("unsupported actuation-policy source kind %q", f.Source.Kind)
	}
	for name, value := range map[string]string{
		"namespace":       f.Source.Namespace,
		"name":            f.Source.Name,
		"uid":             f.Source.UID,
		"resourceVersion": f.Source.ResourceVersion,
		"key":             f.Source.Key,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("actuation-policy ConfigMap source %s is required without surrounding whitespace", name)
		}
	}
	return nil
}

func validateActuationPolicySelection(policy ActuationPolicy, selection ActuationSelection) error {
	if err := selection.Validate(); err != nil {
		return err
	}
	if policy.DeviceClass != selection.DeviceClass || policy.PowerMode != selection.PowerMode ||
		policy.ActuatorKind != selection.Actuator.Kind {
		return errors.New("frozen actuation policy does not match the selected actuator")
	}
	return nil
}
