package profilingrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	legacyIntentSchemaVersion       = "chill.dacs.io/profiling-run-intent.v1alpha1"
	previousIntentSchemaVersion     = "chill.dacs.io/profiling-run-intent.v1alpha2"
	workloadFreeIntentSchemaVersion = "chill.dacs.io/profiling-run-intent.v1alpha3"
	SchemaVersion                   = "chill.dacs.io/profiling-run-intent.v1alpha4"
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

// UnmarshalJSON accepts retained immutable intent schemas while keeping the
// current domain model free of obsolete component and field names.
func (i *Intent) UnmarshalJSON(payload []byte) error {
	var wire struct {
		SchemaVersion string                `json:"schemaVersion"`
		Candidate     CandidateIdentity     `json:"candidate"`
		State         ExecutionState        `json:"state"`
		Images        Images                `json:"images"`
		TargetNode    NodeSnapshot          `json:"targetNode"`
		CPU           CPUContract           `json:"cpu"`
		Measurement   MeasurementContract   `json:"measurement"`
		Metrics       *MetricContract       `json:"metrics"`
		LegacyMetrics *legacyMetricContract `json:"power"`
		Workload      WorkloadContract      `json:"workload"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("intent contains multiple JSON values")
		}
		return err
	}
	if wire.Metrics != nil && wire.LegacyMetrics != nil {
		return errors.New("intent contains multiple metric contracts")
	}
	if wire.Metrics == nil && wire.LegacyMetrics == nil {
		return errors.New("intent metric contract is required")
	}
	i.SchemaVersion = wire.SchemaVersion
	i.Candidate = wire.Candidate
	i.State = wire.State
	i.Images = wire.Images
	i.TargetNode = wire.TargetNode
	i.CPU = wire.CPU
	i.Measurement = wire.Measurement
	i.Workload = wire.Workload
	if wire.Metrics != nil {
		i.Metrics = *wire.Metrics
	} else {
		i.Metrics = wire.LegacyMetrics.current()
	}
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

// UnmarshalJSON accepts retained intent schemas while exposing only the
// current component name to callers.
func (i *Images) UnmarshalJSON(payload []byte) error {
	var wire struct {
		Runtime          string `json:"runtime"`
		Artifact         string `json:"artifact"`
		MetricObserver   string `json:"metricObserver"`
		LegacyObserver   string `json:"powerObserver"`
		WorkloadObserver string `json:"workloadObserver"`
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("images contains multiple JSON values")
		}
		return err
	}
	if wire.MetricObserver != "" && wire.LegacyObserver != "" {
		return errors.New("images must contain exactly one observer image field")
	}
	i.Runtime = wire.Runtime
	i.Artifact = wire.Artifact
	i.MetricObserver = wire.MetricObserver
	i.WorkloadObserver = wire.WorkloadObserver
	if i.MetricObserver == "" {
		i.MetricObserver = wire.LegacyObserver
	}
	return nil
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
	Batch            int           `json:"batch"`
	WarmupIterations int           `json:"warmupIterations"`
	Duration         time.Duration `json:"-"`
	DurationSeconds  int           `json:"durationSeconds"`
	Repetitions      int           `json:"repetitions"`
	WorkloadMode     string        `json:"workloadMode"`
	InputGenerator   string        `json:"inputGenerator"`
	OutputValidation string        `json:"outputValidation"`
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

type legacyMetricContract struct {
	SourceKind                 string `json:"sourceKind"`
	NodeName                   string `json:"nodeName"`
	Endpoint                   string `json:"endpoint"`
	Metric                     string `json:"metric"`
	ConfigurationContentDigest string `json:"configurationContentDigest,omitempty"`
	IntervalMilliseconds       int    `json:"intervalMilliseconds"`
	TimeoutMilliseconds        int    `json:"timeoutMilliseconds"`
	DurationSeconds            int    `json:"durationSeconds"`
}

func (c legacyMetricContract) current() MetricContract {
	return MetricContract{
		SourceKind: c.SourceKind, NodeName: c.NodeName, Endpoint: c.Endpoint, PowerMetric: c.Metric,
		ConfigurationContentDigest: c.ConfigurationContentDigest,
		IntervalMilliseconds:       c.IntervalMilliseconds, TimeoutMilliseconds: c.TimeoutMilliseconds,
		DurationSeconds: c.DurationSeconds,
	}
}

func legacyMetricContractFromCurrent(c MetricContract) legacyMetricContract {
	return legacyMetricContract{
		SourceKind: c.SourceKind, NodeName: c.NodeName, Endpoint: c.Endpoint, Metric: c.PowerMetric,
		ConfigurationContentDigest: c.ConfigurationContentDigest,
		IntervalMilliseconds:       c.IntervalMilliseconds, TimeoutMilliseconds: c.TimeoutMilliseconds,
		DurationSeconds: c.DurationSeconds,
	}
}
