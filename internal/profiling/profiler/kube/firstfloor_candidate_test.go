package kube

import (
	"context"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
)

func TestFirstfloorCUDAResolverRequiresDeclaredPhysicalGPU(t *testing.T) {
	for _, test := range []struct {
		name, access string
		wantError    bool
	}{
		{"declared GPU", "    access:\n      gpuUUID: GPU-actual-device\n      resources: {nvidia.com/gpu: '1'}\n", false},
		{"missing access", "", true},
		{"missing UUID", "    access:\n      resources: {nvidia.com/gpu: '1'}\n", true},
		{"missing allocation", "    access:\n      gpuUUID: GPU-actual-device\n", true},
		{"shared allocation", "    access:\n      gpuUUID: GPU-actual-device\n      resources: {nvidia.com/gpu: '2'}\n", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			objects := validCandidateObjects()
			model := objects[0].(*edgev1alpha1.ModelSpec)
			model.Spec.ExecutionPaths[0].Runtime.Backend = profilingrun.FirstfloorORTCUDABackend
			facts := strings.Replace(validExecutionFacts(), `"backends":["ort-cpu"]`, `"backends":["ort-cuda"]`, 1)
			objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = facts + test.access
			candidate, err := newCandidateResolver(t, objects...).ResolveCandidate(context.Background(), validCandidateSelection())
			if (err != nil) != test.wantError {
				t.Fatalf("ResolveCandidate() = %v, want error %t", err, test.wantError)
			}
			if test.wantError {
				return
			}
			report, err := profilingrun.DecodeCandidateReport(candidate.ReportBytes)
			if err != nil {
				t.Fatal(err)
			}
			if report.RuntimeAccess == nil || report.RuntimeAccess.GPUUUID != "GPU-actual-device" || report.RuntimeAccess.Resources["nvidia.com/gpu"] != "1" || candidate.State.Firstfloor == nil {
				t.Fatal("selected CUDA device or native settings were not frozen")
			}
		})
	}
}
