package admission

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/lab-paper-code/chill/internal/profilingrun"
)

const (
	stateIdentitySchemaVersion               = "chill.dacs.io/execution-state-identity.v1alpha1"
	hostBoundStateIdentitySchemaVersion      = "chill.dacs.io/execution-state-identity.v1alpha2"
	actuatedStateIdentitySchemaVersion       = "chill.dacs.io/execution-state-identity.v1alpha3"
	environmentIdentitySchemaVersion         = "chill.dacs.io/external-environment-identity.v1alpha1"
	measurementProtocolIdentitySchemaVersion = "chill.dacs.io/measurement-protocol-identity.v1alpha1"
)

// RunIdentities separates baseline-compatible execution state from the
// measurement protocol family used to observe one Run.
type RunIdentities struct {
	StateIdentity               string
	MeasurementProtocolIdentity string
}

type metricSourceIdentity struct {
	SourceKind                 string `json:"sourceKind"`
	NodeName                   string `json:"nodeName"`
	Endpoint                   string `json:"endpoint"`
	PowerMetric                string `json:"powerMetric"`
	ConfigurationContentDigest string `json:"configurationContentDigest,omitempty"`
}

type metricSamplingProtocol struct {
	IntervalMilliseconds int `json:"intervalMilliseconds"`
	TimeoutMilliseconds  int `json:"timeoutMilliseconds"`
	DurationSeconds      int `json:"durationSeconds"`
}

// ProjectRunIdentities derives versioned identities from one frozen intent
// and the actual Node UID recorded in finalized evidence.
func ProjectRunIdentities(intent profilingrun.Intent, nodeUID string) (RunIdentities, error) {
	stateSchemaVersion := stateIdentitySchemaVersion
	if intent.State.HostContractDigest != "" {
		stateSchemaVersion = hostBoundStateIdentitySchemaVersion
	}
	if intent.Actuation != nil {
		stateSchemaVersion = actuatedStateIdentitySchemaVersion
	}
	state, err := digestIdentity(stateSchemaVersion, struct {
		State        profilingrun.ExecutionState     `json:"state"`
		RuntimeImage string                          `json:"runtimeImage"`
		NodeUID      string                          `json:"nodeUID"`
		CPU          profilingrun.CPUContract        `json:"cpu"`
		MetricSource metricSourceIdentity            `json:"metricSource"`
		Actuation    *profilingrun.ActuationContract `json:"actuation,omitempty"`
	}{
		State: intent.State, RuntimeImage: intent.Images.Runtime, NodeUID: nodeUID, CPU: intent.CPU,
		Actuation: intent.Actuation,
		MetricSource: metricSourceIdentity{
			SourceKind: intent.Metrics.SourceKind, NodeName: intent.Metrics.NodeName,
			Endpoint: intent.Metrics.Endpoint, PowerMetric: intent.Metrics.PowerMetric,
			ConfigurationContentDigest: intent.Metrics.ConfigurationContentDigest,
		},
	})
	if err != nil {
		return RunIdentities{}, fmt.Errorf("derive execution-state identity: %w", err)
	}

	measurement := intent.Measurement
	measurement.Batch = 0
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
			IntervalMilliseconds: intent.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  intent.Metrics.TimeoutMilliseconds,
			DurationSeconds:      intent.Metrics.DurationSeconds,
		},
		Images: struct {
			Artifact         string `json:"artifact"`
			MetricObserver   string `json:"metricObserver"`
			WorkloadObserver string `json:"workloadObserver,omitempty"`
		}{
			Artifact: intent.Images.Artifact, MetricObserver: intent.Images.MetricObserver,
			WorkloadObserver: intent.Images.WorkloadObserver,
		},
		Workload: intent.Workload,
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
