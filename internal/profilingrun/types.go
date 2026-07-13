package profilingrun

import "time"

const SchemaVersion = "chill.dacs.io/profiling-run-intent.v1alpha1"

type Intent struct {
	SchemaVersion string              `json:"schemaVersion"`
	Candidate     CandidateIdentity   `json:"candidate"`
	State         ExecutionState      `json:"state"`
	Images        Images              `json:"images"`
	TargetNode    NodeSnapshot        `json:"targetNode"`
	CPU           CPUContract         `json:"cpu"`
	Measurement   MeasurementContract `json:"measurement"`
	Power         PowerContract       `json:"power"`
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
	RuntimeFamily  string `json:"runtimeFamily"`
	Backend        string `json:"backend"`
	PowerMode      string `json:"powerMode"`
}

type Images struct {
	Runtime       string `json:"runtime"`
	Artifact      string `json:"artifact"`
	PowerObserver string `json:"powerObserver"`
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

type PowerContract struct {
	SourceKind           string `json:"sourceKind"`
	NodeName             string `json:"nodeName"`
	Endpoint             string `json:"endpoint"`
	Metric               string `json:"metric"`
	IntervalMilliseconds int    `json:"intervalMilliseconds"`
	TimeoutMilliseconds  int    `json:"timeoutMilliseconds"`
	DurationSeconds      int    `json:"durationSeconds"`
}
