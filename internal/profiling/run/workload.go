package profilingrun

import (
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
	return ownerKind == "Job" && labels[MeasurementWorkloadLabelKey] == MeasurementWorkloadLabelValue
}
