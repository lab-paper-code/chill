package controller

import (
	"errors"

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
	if r.AdmissionPolicy.Source.Kind == profileradmission.ConfigMapPolicySourceKind &&
		r.ExecutionPolicy.Source.Kind == profiler.ConfigMapExecutionPolicySourceKind &&
		(r.AdmissionPolicy.Source.Namespace != r.ExecutionPolicy.Source.Namespace ||
			r.AdmissionPolicy.Source.Name != r.ExecutionPolicy.Source.Name ||
			r.AdmissionPolicy.Source.UID != r.ExecutionPolicy.Source.UID ||
			r.AdmissionPolicy.Source.ResourceVersion != r.ExecutionPolicy.Source.ResourceVersion) {
		return errors.New("frozen admission and execution policies came from different ConfigMap snapshots")
	}
	return nil
}
