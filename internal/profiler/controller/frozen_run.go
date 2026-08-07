package controller

import (
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiler"
	profileradmission "github.com/lab-paper-code/chill/internal/profiler/admission"
)

// FrozenRun is the complete durable controller input for one Run. Execution
// preparation stays in the Kubernetes-free Profiler application package;
// configured scientific policy stays in the admission package.
type FrozenRun struct {
	Prepared        profiler.PreparedRun
	AdmissionPolicy profileradmission.FrozenPolicy
	ExecutionPolicy profiler.FrozenExecutionPolicy
}

func (r FrozenRun) Validate() error {
	if err := r.Prepared.Intent.Validate(); err != nil {
		return fmt.Errorf("validate frozen Run intent: %w", err)
	}
	admissionSource, _, err := r.AdmissionPolicy.Records()
	if err != nil {
		return err
	}
	if err := r.ExecutionPolicy.Validate(); err != nil {
		return err
	}
	if err := r.Prepared.ValidateActuationBinding(); err != nil {
		return err
	}
	if admissionSource.Kind == profileradmission.ConfigMapPolicySourceKind &&
		r.ExecutionPolicy.Source.Kind == profiler.ConfigMapExecutionPolicySourceKind &&
		(admissionSource.Namespace != r.ExecutionPolicy.Source.Namespace ||
			admissionSource.Name != r.ExecutionPolicy.Source.Name ||
			admissionSource.UID != r.ExecutionPolicy.Source.UID ||
			admissionSource.ResourceVersion != r.ExecutionPolicy.Source.ResourceVersion) {
		return errors.New("frozen admission and execution policies came from different ConfigMap snapshots")
	}
	if r.Prepared.ActuationPolicy != nil {
		source := r.Prepared.ActuationPolicy.Source
		if source.Kind != profiler.ConfigMapActuationPolicySourceKind ||
			admissionSource.Kind != profileradmission.ConfigMapPolicySourceKind ||
			source.Namespace != admissionSource.Namespace ||
			source.Name != admissionSource.Name ||
			source.UID != admissionSource.UID ||
			source.ResourceVersion != admissionSource.ResourceVersion {
			return errors.New("frozen actuation and scientific policies came from different ConfigMap snapshots")
		}
	}
	return nil
}

func validateSelectedPolicies(run *edgev1alpha1.ProfilingRun, frozen FrozenRun) error {
	if err := validateSelectedAdmissionPolicy(run, frozen.Prepared, frozen.AdmissionPolicy); err != nil {
		return err
	}
	return validateSelectedExecutionPolicy(frozen.Prepared, frozen.ExecutionPolicy)
}

func validateSelectedAdmissionPolicy(
	run *edgev1alpha1.ProfilingRun,
	prepared profiler.PreparedRun,
	policy profileradmission.FrozenPolicy,
) error {
	_, selected, err := policy.Records()
	if err != nil {
		return err
	}
	if selected.Name != run.Spec.AdmissionPolicy {
		return invalidAdmissionPolicy("frozen admission policy does not match immutable ProfilingRun spec")
	}
	if selected.WorkloadMode != prepared.Intent.Measurement.WorkloadMode {
		return invalidAdmissionPolicy("frozen admission policy workload mode does not match immutable Run intent")
	}
	return nil
}

func validateSelectedExecutionPolicy(
	prepared profiler.PreparedRun,
	policy profiler.FrozenExecutionPolicy,
) error {
	if (policy.Policy.DeviceClass != "*" && policy.Policy.DeviceClass != prepared.Intent.State.DeviceClass) ||
		policy.Policy.ExecutionPath != prepared.Intent.Candidate.ExecutionPath ||
		policy.Policy.WorkloadMode != prepared.Intent.Measurement.WorkloadMode {
		return invalidExecutionPolicy("frozen execution policy does not match immutable Run intent")
	}
	return nil
}
