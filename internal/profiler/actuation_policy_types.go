package profiler

import "github.com/lab-paper-code/chill/internal/profilingrun"

const (
	ActuationPolicyCatalogSchemaVersion = "chill.dacs.io/profiler-actuation-policies.v1alpha1"
	ActuationPolicySchemaVersion        = "chill.dacs.io/profiler-actuation-policy.v1alpha1"
	FrozenActuationPolicySchemaVersion  = "chill.dacs.io/frozen-profiler-actuation-policy.v1alpha1"

	ConfigMapActuationPolicySourceKind = "ConfigMap"
)

// ActuationPolicyCatalog is installation-time authorization and timing
// configuration for Node-wide mode changes. It does not contain observed Node
// state or a caller-selected actuator target.
type ActuationPolicyCatalog struct {
	SchemaVersion string                `json:"schemaVersion"`
	Policies      []ActuationPolicySpec `json:"policies"`
}

// ActuationPolicySpec authorizes one exact DeviceClass/mode/actuator tuple and
// fixes the timing bounds that the later durable handoff must enforce.
type ActuationPolicySpec struct {
	Name                        string `json:"name"`
	DeviceClass                 string `json:"deviceClass"`
	PowerMode                   string `json:"powerMode"`
	ActuatorKind                string `json:"actuatorKind"`
	SettlingDurationSeconds     int    `json:"settlingDurationSeconds"`
	MaxActuationDurationSeconds int    `json:"maxActuationDurationSeconds"`
}

// ActuationPolicy is the canonical selected authorization. Its digest is
// bound into the immutable Run intent.
type ActuationPolicy struct {
	SchemaVersion string `json:"schemaVersion"`
	ActuationPolicySpec
}

type ActuationPolicySource struct {
	Kind                 string `json:"kind"`
	Namespace            string `json:"namespace,omitempty"`
	Name                 string `json:"name"`
	UID                  string `json:"uid,omitempty"`
	ResourceVersion      string `json:"resourceVersion,omitempty"`
	Key                  string `json:"key,omitempty"`
	CatalogContentDigest string `json:"catalogContentDigest"`
}

type FrozenActuationPolicy struct {
	SchemaVersion string                `json:"schemaVersion"`
	Source        ActuationPolicySource `json:"source"`
	Policy        ActuationPolicy       `json:"policy"`
}

// ActuationSelection is derived from the verified DeviceClass selection, not
// supplied by the ProfilingRun caller independently of that catalog object.
type ActuationSelection struct {
	DeviceClass string
	PowerMode   string
	Actuator    profilingrun.ActuatorTarget
}
