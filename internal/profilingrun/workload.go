package profilingrun

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	WorkloadContractSchemaVersion = "chill.dacs.io/workload-contract.v1alpha1"
	MeasurementWorkloadLabelKey   = "edge.dacs.io/workload-role"
	MeasurementWorkloadLabelValue = "profiling-run"
)

// WorkloadContract freezes only the observation semantics used by current
// Runs. External workloads are evidence, not an installation-specific allow list.
type WorkloadContract struct {
	SchemaVersion string `json:"schemaVersion"`
}

func CurrentWorkloadContract() WorkloadContract {
	return WorkloadContract{SchemaVersion: WorkloadContractSchemaVersion}
}

func (c WorkloadContract) Validate() error {
	if c.SchemaVersion == WorkloadContractSchemaVersion {
		return nil
	}
	return fmt.Errorf("unsupported workload contract schemaVersion %q", c.SchemaVersion)
}

func (c WorkloadContract) Digest() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", fmt.Errorf("encode workload contract: %w", err)
	}
	return sha256Digest(payload), nil
}

// LabelKeys returns only the labels required to distinguish another CHILL
// measurement Pod. External workload identities remain generic Pod facts.
func (c WorkloadContract) LabelKeys() ([]string, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return []string{
		"app.kubernetes.io/component",
		"app.kubernetes.io/name",
		MeasurementWorkloadLabelKey,
	}, nil
}

func IsMeasurementWorkload(ownerKind string, labels map[string]string) bool {
	if ownerKind != "Job" {
		return false
	}
	if labels[MeasurementWorkloadLabelKey] == MeasurementWorkloadLabelValue {
		return true
	}
	// Recognize retained CHILL profiling Jobs created before the dedicated role
	// label existed while current Jobs roll through the cluster.
	return labels["app.kubernetes.io/name"] == "chill" &&
		labels["app.kubernetes.io/component"] == "profiler"
}

func sha256Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
