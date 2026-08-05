package controller

import (
	"errors"

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
	if r.Prepared.IntentDigest == "" {
		return errors.New("frozen Run prepared intent is required")
	}
	if err := r.AdmissionPolicy.Validate(); err != nil {
		return err
	}
	if err := r.ExecutionPolicy.Validate(); err != nil {
		return err
	}
	if err := r.Prepared.ValidateActuationBinding(); err != nil {
		return err
	}
	if r.AdmissionPolicy.Source.Kind == profileradmission.ConfigMapPolicySourceKind &&
		r.ExecutionPolicy.Source.Kind == profiler.ConfigMapExecutionPolicySourceKind &&
		(r.AdmissionPolicy.Source.Namespace != r.ExecutionPolicy.Source.Namespace ||
			r.AdmissionPolicy.Source.Name != r.ExecutionPolicy.Source.Name ||
			r.AdmissionPolicy.Source.UID != r.ExecutionPolicy.Source.UID ||
			r.AdmissionPolicy.Source.ResourceVersion != r.ExecutionPolicy.Source.ResourceVersion) {
		return errors.New("frozen admission and execution policies came from different ConfigMap snapshots")
	}
	if r.Prepared.ActuationPolicy != nil {
		source := r.Prepared.ActuationPolicy.Source
		if source.Kind != profiler.ConfigMapActuationPolicySourceKind ||
			r.AdmissionPolicy.Source.Kind != profileradmission.ConfigMapPolicySourceKind ||
			source.Namespace != r.AdmissionPolicy.Source.Namespace ||
			source.Name != r.AdmissionPolicy.Source.Name ||
			source.UID != r.AdmissionPolicy.Source.UID ||
			source.ResourceVersion != r.AdmissionPolicy.Source.ResourceVersion {
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
	if policy.Policy.Name != run.Spec.AdmissionPolicy {
		return invalidAdmissionPolicy("frozen admission policy does not match immutable ProfilingRun spec")
	}
	if policy.Policy.WorkloadMode != prepared.Intent.Measurement.WorkloadMode {
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
