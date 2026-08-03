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
}

func (r FrozenRun) Validate() error {
	if r.Prepared.IntentDigest == "" {
		return errors.New("frozen Run prepared intent is required")
	}
	return r.AdmissionPolicy.Validate()
}
