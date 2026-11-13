package evidence

import (
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	ExecutionEvidenceSchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha18"
)

// ExecutionEvidence is finalized after the worker exits. It references rather
// than duplicates the immutable raw measurement object.
type ExecutionEvidence struct {
	SchemaVersion  string                       `json:"schemaVersion"`
	RunConfig      profilingrun.RunConfig       `json:"runConfig"`
	RawMeasurement evidencestore.ObjectIdentity `json:"rawMeasurement"`
	Terminal       TerminalFacts                `json:"terminal"`
	Actuation      *ActuationFacts              `json:"actuation,omitempty"`
	FinalizedAt    time.Time                    `json:"finalizedAt"`
}

// ActuationFacts are Profiler-observed Node facts of the power-mode apply
// that established the measured state. Worker-owned raw evidence deliberately
// cannot assert these external host-state observations. Handoff names the
// retired actuation object of records written before Node power-mode labels;
// new records carry Generation instead.
type ActuationFacts struct {
	Run        NamespacedObjectFacts  `json:"run"`
	Handoff    *NamespacedObjectFacts `json:"handoff,omitempty"`
	Target     ActuationObservation   `json:"target"`
	ReadyAt    time.Time              `json:"readyAt"`
	Generation *PowerModeGeneration   `json:"generation,omitempty"`
}

// PowerModeGeneration is the Node power-mode generation of the established
// apply and at finalization; any difference is a host power change.
type PowerModeGeneration struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

type NamespacedObjectFacts struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
}

type ActuationObservation struct {
	PowerMode  string                      `json:"powerMode"`
	Actuator   profilingrun.ActuatorTarget `json:"actuator"`
	ObservedAt time.Time                   `json:"observedAt"`
}

func IsSupportedExecutionEvidenceSchemaVersion(version string) bool {
	return version == ExecutionEvidenceSchemaVersion
}

type TerminalFacts struct {
	Job  JobTerminalFacts  `json:"job"`
	Pod  PodTerminalFacts  `json:"pod"`
	Node NodeTerminalFacts `json:"node"`
}

type JobTerminalFacts struct {
	Namespace      string    `json:"namespace"`
	Name           string    `json:"name"`
	UID            string    `json:"uid"`
	Succeeded      int32     `json:"succeeded"`
	Failed         int32     `json:"failed"`
	CompletionTime time.Time `json:"completionTime"`
}

type PodTerminalFacts struct {
	Namespace          string `json:"namespace"`
	Name               string `json:"name"`
	UID                string `json:"uid"`
	ControllerOwnerUID string `json:"controllerOwnerUID"`
	NodeName           string `json:"nodeName"`
	Phase              string `json:"phase"`
}

type NodeTerminalFacts struct {
	Name string `json:"name"`
	UID  string `json:"uid"`
}
