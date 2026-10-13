package evidence

import (
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	ExecutionEvidenceSchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha15"
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

// ActuationFacts are Profiler-observed Kubernetes facts from the one-way
// transition that established the measured state. Worker-owned raw evidence
// deliberately cannot assert these external host-state observations.
type ActuationFacts struct {
	Run     NamespacedObjectFacts `json:"run"`
	Handoff NamespacedObjectFacts `json:"handoff"`
	Target  ActuationObservation  `json:"target"`
	ReadyAt time.Time             `json:"readyAt"`
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
	Job          JobTerminalFacts    `json:"job"`
	Pod          PodTerminalFacts    `json:"pod"`
	Node         NodeTerminalFacts   `json:"node"`
	Containers   []ContainerTerminal `json:"containers"`
	NodeWorkload NodeWorkload        `json:"nodeWorkload"`
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

type ContainerTerminal struct {
	Name           string    `json:"name"`
	RequestedImage string    `json:"requestedImage"`
	ImageID        string    `json:"imageID"`
	RestartCount   int32     `json:"restartCount"`
	ExitCode       int32     `json:"exitCode"`
	Reason         string    `json:"reason"`
	StartedAt      time.Time `json:"startedAt"`
	FinishedAt     time.Time `json:"finishedAt"`
}

type NodeWorkload struct {
	ObservedAt time.Time     `json:"observedAt"`
	Pods       []WorkloadPod `json:"pods"`
}

type WorkloadPod struct {
	Namespace string   `json:"namespace"`
	Name      string   `json:"name"`
	UID       string   `json:"uid"`
	Phase     string   `json:"phase"`
	Images    []string `json:"images"`
}
