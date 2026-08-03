package profilingrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	SchemaVersion = "chill.dacs.io/profiling-run-intent.v1alpha6"

	SaturatedSerialFixedBatchWorkloadMode = "saturated-serial-fixed-batch"
	IdleModelLoadedWorkloadMode           = "idle-model-loaded"
	NoInputGenerator                      = "none"
	NoOutputValidation                    = "none"
)

type Intent struct {
	SchemaVersion string              `json:"schemaVersion"`
	Candidate     CandidateIdentity   `json:"candidate"`
	State         ExecutionState      `json:"state"`
	Images        Images              `json:"images"`
	TargetNode    NodeSnapshot        `json:"targetNode"`
	CPU           CPUContract         `json:"cpu"`
	Measurement   MeasurementContract `json:"measurement"`
	Metrics       MetricContract      `json:"metrics"`
	Workload      WorkloadContract    `json:"workload"`
}

// UnmarshalJSON strictly decodes the current wire field names.
func (i *Intent) UnmarshalJSON(payload []byte) error {
	type wire Intent
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("intent contains multiple JSON values")
		}
		return err
	}
	*i = Intent(decoded)
	return nil
}

type CandidateIdentity struct {
	ReportDigest             string `json:"reportDigest"`
	ModelSpecContentDigest   string `json:"modelSpecContentDigest"`
	DeviceClassContentDigest string `json:"deviceClassContentDigest"`
	RuntimeDeclarationDigest string `json:"runtimeDeclarationDigest"`
	ExecutionPath            string `json:"executionPath"`
	Verdict                  string `json:"verdict"`
}

type ExecutionState struct {
	DeviceClass    string `json:"deviceClass"`
	Model          string `json:"model"`
	Artifact       string `json:"artifact"`
	ArtifactDigest string `json:"artifactDigest"`
	ArtifactPath   string `json:"artifactPath,omitempty"`
	RuntimeFamily  string `json:"runtimeFamily"`
	Backend        string `json:"backend"`
	PowerMode      string `json:"powerMode"`
}

type Images struct {
	Runtime          string `json:"runtime"`
	Artifact         string `json:"artifact"`
	MetricObserver   string `json:"metricObserver"`
	WorkloadObserver string `json:"workloadObserver,omitempty"`
}

type NodeSnapshot struct {
	Name            string `json:"name"`
	UID             string `json:"uid"`
	ResourceVersion string `json:"resourceVersion"`
	Architecture    string `json:"architecture"`
	AllocatableCPU  string `json:"allocatableCPU"`
}

type CPUContract struct {
	Policy             string `json:"policy"`
	PolicyVersion      string `json:"policyVersion"`
	Request            string `json:"request"`
	Limit              string `json:"limit"`
	ORTIntraOpThreads  int    `json:"ortIntraOpThreads"`
	ORTInterOpThreads  int    `json:"ortInterOpThreads"`
	ExclusivityClaimed bool   `json:"exclusivityClaimed"`
}

type MeasurementContract struct {
	Batch                   int           `json:"batch"`
	WarmupIterations        int           `json:"warmupIterations"`
	SettlingDurationSeconds int           `json:"settlingDurationSeconds,omitempty"`
	Duration                time.Duration `json:"-"`
	DurationSeconds         int           `json:"durationSeconds"`
	Repetitions             int           `json:"repetitions"`
	WorkloadMode            string        `json:"workloadMode"`
	InputGenerator          string        `json:"inputGenerator"`
	OutputValidation        string        `json:"outputValidation"`
}

type MetricContract struct {
	SourceKind                 string `json:"sourceKind"`
	NodeName                   string `json:"nodeName"`
	Endpoint                   string `json:"endpoint"`
	PowerMetric                string `json:"powerMetric"`
	ConfigurationContentDigest string `json:"configurationContentDigest,omitempty"`
	IntervalMilliseconds       int    `json:"intervalMilliseconds"`
	TimeoutMilliseconds        int    `json:"timeoutMilliseconds"`
	DurationSeconds            int    `json:"durationSeconds"`
}
