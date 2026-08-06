package nodeactuator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const ActuationPolicySchemaVersion = "chill.dacs.io/profiler-actuation-policy.v1alpha1"

var actuationPolicyNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

type ActuationPolicySpec struct {
	Name                        string `json:"name"`
	DeviceClass                 string `json:"deviceClass"`
	PowerMode                   string `json:"powerMode"`
	ActuatorKind                string `json:"actuatorKind"`
	SettlingDurationSeconds     int    `json:"settlingDurationSeconds"`
	MaxActuationDurationSeconds int    `json:"maxActuationDurationSeconds"`
}

type ActuationPolicy struct {
	SchemaVersion string `json:"schemaVersion"`
	ActuationPolicySpec
}

func (p ActuationPolicySpec) Validate() error {
	return (ActuationPolicy{
		SchemaVersion:       ActuationPolicySchemaVersion,
		ActuationPolicySpec: p,
	}).Validate()
}

func (p ActuationPolicy) Validate() error {
	if p.SchemaVersion != ActuationPolicySchemaVersion {
		return fmt.Errorf("unsupported actuation-policy schema %q", p.SchemaVersion)
	}
	if len(p.Name) < 1 || len(p.Name) > 63 || !actuationPolicyNamePattern.MatchString(p.Name) {
		return errors.New("actuation-policy name must be a lowercase DNS-like identifier of at most 63 characters")
	}
	for name, value := range map[string]string{
		"deviceClass": p.DeviceClass, "powerMode": p.PowerMode, "actuatorKind": p.ActuatorKind,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("actuation-policy %s is required without surrounding whitespace", name)
		}
	}
	if p.PowerMode == "fixed" {
		return errors.New("actuation policy cannot select fixed power mode")
	}
	if p.ActuatorKind != ActuatorKindJetsonNVPModel && p.ActuatorKind != ActuatorKindNvidiaLockedGPUClock {
		return fmt.Errorf("unsupported actuation-policy actuatorKind %q", p.ActuatorKind)
	}
	if p.SettlingDurationSeconds < 1 || p.SettlingDurationSeconds > 86_400 {
		return errors.New("actuation-policy settlingDurationSeconds must be between 1 second and 24 hours")
	}
	if p.MaxActuationDurationSeconds <= p.SettlingDurationSeconds || p.MaxActuationDurationSeconds > 86_400 {
		return errors.New("actuation-policy maxActuationDurationSeconds must exceed settling and be at most 24 hours")
	}
	return nil
}

func (p ActuationPolicy) Digest() (string, error) {
	if err := p.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
