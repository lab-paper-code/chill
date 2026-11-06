package projection

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const (
	stateIdentitySchemaVersion               = "chill.dacs.io/execution-state-identity.v1alpha7"
	environmentIdentitySchemaVersion         = "chill.dacs.io/external-environment-identity.v1alpha1"
	measurementProtocolIdentitySchemaVersion = "chill.dacs.io/measurement-protocol-identity.v1alpha2"
)

// RunIdentities separates baseline-compatible execution state from the
// measurement protocol family used to observe one Run.
type RunIdentities struct {
	StateIdentity               string
	MeasurementProtocolIdentity string
}

type metricSamplingProtocol struct {
	IntervalMilliseconds int `json:"intervalMilliseconds"`
	TimeoutMilliseconds  int `json:"timeoutMilliseconds"`
	DurationSeconds      int `json:"durationSeconds"`
}

// ProjectRunIdentities derives versioned identities from one RunConfig
// and the actual Node UID recorded in finalized evidence.
func ProjectRunIdentities(config profilingrun.RunConfig, nodeUID string) (RunIdentities, error) {
	executionState := config.State
	executionState.HostContract = nil
	executionState.Preprocessor.UID = ""
	state, err := digestIdentity(stateIdentitySchemaVersion, struct {
		State        profilingrun.ExecutionState     `json:"state"`
		RuntimeImage string                          `json:"runtimeImage"`
		NodeUID      string                          `json:"nodeUID"`
		Actuation    *profilingrun.ActuationContract `json:"actuation,omitempty"`
	}{
		State: executionState, RuntimeImage: config.Images.Runtime, NodeUID: nodeUID,
		Actuation: config.Actuation,
	})
	if err != nil {
		return RunIdentities{}, fmt.Errorf("derive execution-state identity: %w", err)
	}

	measurement := config.Measurement
	measurement.Batch = 0
	measurement.WorkloadResourceVersion = ""
	measurement.WorkloadUID = ""
	if measurement.Dataset != nil {
		dataset := *measurement.Dataset
		dataset.UID = ""
		measurement.Dataset = &dataset
	}
	protocol, err := digestIdentity(measurementProtocolIdentitySchemaVersion, struct {
		Measurement profilingrun.MeasurementContract `json:"measurement"`
		Sampling    metricSamplingProtocol           `json:"sampling"`
		Images      struct {
			Artifact         string `json:"artifact"`
			MetricObserver   string `json:"metricObserver"`
			WorkloadObserver string `json:"workloadObserver,omitempty"`
		} `json:"images"`
		Workload profilingrun.WorkloadContract `json:"workload"`
	}{
		Measurement: measurement,
		Sampling: metricSamplingProtocol{
			IntervalMilliseconds: config.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  config.Metrics.TimeoutMilliseconds,
			DurationSeconds:      config.Metrics.DurationSeconds,
		},
		Images: struct {
			Artifact         string `json:"artifact"`
			MetricObserver   string `json:"metricObserver"`
			WorkloadObserver string `json:"workloadObserver,omitempty"`
		}{
			Artifact: config.Images.Artifact, MetricObserver: config.Images.MetricObserver,
			WorkloadObserver: config.Images.WorkloadObserver,
		},
		Workload: config.Workload,
	})
	if err != nil {
		return RunIdentities{}, fmt.Errorf("derive measurement-protocol identity: %w", err)
	}
	return RunIdentities{StateIdentity: state, MeasurementProtocolIdentity: protocol}, nil
}

// UnknownEnvironmentIdentity keeps retained workload-free evidence
// distinguishable from a known empty external environment.
func UnknownEnvironmentIdentity() (string, error) {
	return digestIdentity(
		environmentIdentitySchemaVersion,
		struct {
			Known bool `json:"known"`
		}{Known: false},
	)
}

func digestIdentity(schemaVersion string, value any) (string, error) {
	payload, err := json.Marshal(struct {
		SchemaVersion string `json:"schemaVersion"`
		Value         any    `json:"value"`
	}{SchemaVersion: schemaVersion, Value: value})
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", schemaVersion, err)
	}
	sum := sha256.Sum256(payload)
	return schemaVersion + "@sha256:" + hex.EncodeToString(sum[:]), nil
}
