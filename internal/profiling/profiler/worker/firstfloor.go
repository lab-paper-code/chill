package worker

import (
	"errors"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const firstfloorRequestSchemaVersion = "chill.dacs.io/firstfloor-runtime-request.v1alpha1"

// NewFirstfloorRuntime binds the actual backend to the verified frozen run.
func NewFirstfloorRuntime(config CPURuntimeConfig, frozen profilingrun.VerifiedRunConfig) (*CPURuntime, error) {
	record, err := frozen.Record()
	if err != nil {
		return nil, err
	}
	if !profilingrun.IsSupportedFirstfloorRunConfig(record) {
		return nil, errors.New("firstfloor adapter requires a supported verified firstfloor run")
	}
	return newCPURuntime(config, "firstfloor", record.State.RuntimeFamily, record.State.Backend, firstfloorRequestSchemaVersion)
}
