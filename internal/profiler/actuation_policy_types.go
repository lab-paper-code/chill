package profiler

import (
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const (
	ActuationPolicyCatalogSchemaVersion = "chill.dacs.io/profiler-actuation-policies.v1alpha1"
	ActuationPolicySchemaVersion        = nodeactuator.ActuationPolicySchemaVersion
	FrozenActuationPolicySchemaVersion  = "chill.dacs.io/frozen-profiler-actuation-policy.v1alpha2"

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
type ActuationPolicySpec = nodeactuator.ActuationPolicySpec

// ActuationPolicy is the selected authorization retained in the immutable Run
// intent.
type ActuationPolicy = nodeactuator.ActuationPolicy

type ActuationPolicySource struct {
	Kind            string `json:"kind"`
	Namespace       string `json:"namespace,omitempty"`
	Name            string `json:"name"`
	UID             string `json:"uid,omitempty"`
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Key             string `json:"key,omitempty"`
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
