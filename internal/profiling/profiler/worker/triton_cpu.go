package worker

import profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"

const tritonCPURequestSchemaVersion = "chill.dacs.io/triton-cpu-runtime-request.v1alpha15"

// NewTritonCPURuntime uses the same process handoff and immutable typed evidence
// as CPU ORT, but never accepts an ORT execution-family result as Triton evidence.
func NewTritonCPURuntime(config CPURuntimeConfig) (*CPURuntime, error) {
	return newCPURuntime(config, "Triton CPU", profilingrun.TritonRuntimeFamily, profilingrun.TritonPythonBackend, tritonCPURequestSchemaVersion)
}
