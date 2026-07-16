package evidence

import (
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const ExecutionEvidenceSchemaVersion = "chill.dacs.io/profiling-execution-evidence.v1alpha1"

// ExecutionEvidence is finalized after the worker exits. It references rather
// than duplicates the immutable raw measurement object.
type ExecutionEvidence struct {
	SchemaVersion string              `json:"schemaVersion"`
	IntentDigest  string              `json:"intentDigest"`
	Intent        profilingrun.Intent `json:"intent"`
	// CandidateReportBytes is encoded as base64 by encoding/json so the exact
	// report bytes remain reproducible after an evidence round trip.
	CandidateReportBytes []byte                       `json:"candidateReportBytes"`
	RawMeasurement       evidencestore.ObjectIdentity `json:"rawMeasurement"`
	Terminal             TerminalFacts                `json:"terminal"`
	FinalizedAt          time.Time                    `json:"finalizedAt"`
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
