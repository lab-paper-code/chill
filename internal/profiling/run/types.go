package profilingrun

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const (
	RunConfigSchemaVersion = "chill.dacs.io/profiling-run-config.v1alpha14"

	AllocatableCPUQuotaPolicy = "AllocatableCPUQuota"
	ConfiguredCPUQuotaPolicy  = "ConfiguredCPUQuota"
	CPUQuotaPolicyVersion     = "v1"

	SaturatedSerialFixedBatchWorkloadMode = "saturated-serial-fixed-batch"
	IdleModelLoadedWorkloadMode           = "idle-model-loaded"
	NoInputGenerator                      = "none"
	NoOutputValidation                    = "none"
	RKNNLiteExecutionPath                 = "rknnlite"
	RKNNLiteRuntimeFamily                 = "rknnlite"
	RKNNLiteBackend                       = "not-applicable"
	FixedPowerMode                        = "fixed"
	ActuatorKindJetsonNVPModel            = "JetsonNVPModel"
	ActuatorKindNvidiaLockedGPUClock      = "NvidiaLockedGPUClock"
)

// RunConfig is the exact model, runtime, Node, workload, and observation
// configuration used by one profiling execution.
type RunConfig struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Candidate     CandidateIdentity        `json:"candidate"`
	State         ExecutionState           `json:"state"`
	Images        Images                   `json:"images"`
	Delivery      CandidateRuntimeDelivery `json:"delivery"`
	TargetNode    NodeSnapshot             `json:"targetNode"`
	CPU           CPUContract              `json:"cpu"`
	Measurement   MeasurementContract      `json:"measurement"`
	Metrics       MetricContract           `json:"metrics"`
	Workload      WorkloadContract         `json:"workload"`
	Actuation     *ActuationContract       `json:"actuation,omitempty"`
}

// UnmarshalJSON strictly decodes the current wire fields.
func (c *RunConfig) UnmarshalJSON(payload []byte) error {
	type wire RunConfig
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("RunConfig contains multiple JSON values")
		}
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return err
	}
	if actuationPayload, present := fields["actuation"]; present {
		var actuation *ActuationContract
		if err := json.Unmarshal(actuationPayload, &actuation); err != nil {
			return err
		}
		if actuation == nil {
			return errors.New("runConfig.actuation must be an object when present")
		}
	}
	*c = RunConfig(decoded)
	return nil
}

type CandidateIdentity struct {
	Inputs        CandidateReportInputs `json:"inputs"`
	ExecutionPath string                `json:"executionPath"`
	Verdict       string                `json:"verdict"`
}

type ExecutionState struct {
	DeviceClass   string                       `json:"deviceClass"`
	Model         string                       `json:"model"`
	Artifact      string                       `json:"artifact"`
	ArtifactPath  string                       `json:"artifactPath,omitempty"`
	RuntimeFamily string                       `json:"runtimeFamily"`
	Backend       string                       `json:"backend"`
	PowerMode     string                       `json:"powerMode"`
	HostContract  *CandidateReportHostContract `json:"hostContract,omitempty"`
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
	ORTIntraOpThreads  int    `json:"ortIntraOpThreads,omitempty"`
	ORTInterOpThreads  int    `json:"ortInterOpThreads,omitempty"`
	ExclusivityClaimed bool   `json:"exclusivityClaimed"`
}

// ActuationContract binds a non-fixed selected power mode to the typed target
// and configured policy entry that must converge before execution.
// Mutable observations remain outside the RunConfig.
type ActuationContract struct {
	PowerMode  string         `json:"powerMode"`
	Actuator   ActuatorTarget `json:"actuator"`
	PolicyName string         `json:"policyName"`
}

// ActuatorTarget is a strict discriminated union matching the catalogued
// Node-wide actuator selected for this Run.
type ActuatorTarget struct {
	Kind                 string                      `json:"kind"`
	JetsonNVPModel       *JetsonNVPModelTarget       `json:"jetsonNVPModel,omitempty"`
	NvidiaLockedGPUClock *NvidiaLockedGPUClockTarget `json:"nvidiaLockedGPUClock,omitempty"`
}

type JetsonNVPModelTarget struct {
	ID int32 `json:"id"`
}

type NvidiaLockedGPUClockTarget struct {
	MinMHz int32  `json:"minMHz"`
	MaxMHz int32  `json:"maxMHz"`
	Mode   *int32 `json:"mode,omitempty"`
}

type MeasurementContract struct {
	Batch                   int           `json:"batch"`
	WarmupIterations        int           `json:"warmupIterations"`
	SettlingDurationSeconds int           `json:"settlingDurationSeconds,omitempty"`
	Duration                time.Duration `json:"-"`
	DurationSeconds         int           `json:"durationSeconds"`
	WorkloadMode            string        `json:"workloadMode"`
	InputGenerator          string        `json:"inputGenerator"`
	OutputValidation        string        `json:"outputValidation"`
}

type MetricContract struct {
	SourceKind           string                `json:"sourceKind"`
	NodeName             string                `json:"nodeName"`
	Endpoint             string                `json:"endpoint"`
	PowerMetric          string                `json:"powerMetric"`
	MetricLabels         map[string]string     `json:"metricLabels"`
	ThermalEndpoint      string                `json:"thermalEndpoint"`
	ThermalMetrics       ThermalMetricContract `json:"thermalMetrics"`
	IntervalMilliseconds int                   `json:"intervalMilliseconds"`
	TimeoutMilliseconds  int                   `json:"timeoutMilliseconds"`
	DurationSeconds      int                   `json:"durationSeconds"`
}

// ThermalMetricContract assigns configured metric names to the thermal facts
// interpreted by MetricObserver.
type ThermalMetricContract struct {
	ObservationAvailable      string `json:"observationAvailable"`
	Active                    string `json:"active"`
	CPUCoreThrottleEvents     string `json:"cpuCoreThrottleEvents"`
	CPUPackageThrottleEvents  string `json:"cpuPackageThrottleEvents"`
	Latched                   string `json:"latched"`
	MaximumTemperatureCelsius string `json:"maximumTemperatureCelsius"`
}
