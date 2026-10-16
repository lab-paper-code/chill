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
		{"declared GPU", "    node: lattepanda\n    access:\n      gpuUUID: GPU-actual-device\n      resources: {nvidia.com/gpu: '1'}\n", false},
		{"missing node", "    access:\n      gpuUUID: GPU-actual-device\n      resources: {nvidia.com/gpu: '1'}\n", true},
		{"missing access", "    node: lattepanda\n", true},
		{"missing UUID", "    node: lattepanda\n    access:\n      resources: {nvidia.com/gpu: '1'}\n", true},
		{"missing allocation", "    node: lattepanda\n    access:\n      gpuUUID: GPU-actual-device\n", true},
		{"shared allocation", "    node: lattepanda\n    access:\n      gpuUUID: GPU-actual-device\n      resources: {nvidia.com/gpu: '2'}\n", true},
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

func TestFirstfloorCUDAResolverSelectsEachNodeOwnGPU(t *testing.T) {
	objects := validCandidateObjects()
	model := objects[0].(*edgev1alpha1.ModelSpec)
	model.Spec.ExecutionPaths[0].Runtime.Backend = profilingrun.FirstfloorORTCUDABackend
	facts := strings.Replace(validExecutionFacts(), `"backends":["ort-cpu"]`, `"backends":["ort-cuda"]`, 1)
	declaration := facts[strings.Index(facts, "  - deviceClass:"):]
	objects[2].(*corev1.ConfigMap).Data["execution-facts.yaml"] = facts +
		"    node: genesis2\n    access:\n      gpuUUID: GPU-genesis2\n      resources: {nvidia.com/gpu: '1'}\n" +
		declaration + "    node: genesis3\n    access:\n      gpuUUID: GPU-genesis3\n      resources: {nvidia.com/gpu: '1'}\n"
	resolver := newCandidateResolver(t, objects...)
	for _, node := range []string{"genesis2", "genesis3"} {
		selection := validCandidateSelection()
		selection.Node = node
		candidate, err := resolver.ResolveCandidate(context.Background(), selection)
		if err != nil {
			t.Fatal(err)
		}
		report, err := profilingrun.DecodeCandidateReport(candidate.ReportBytes)
		if err != nil {
			t.Fatal(err)
		}
		if report.RuntimeAccess == nil || report.RuntimeAccess.GPUUUID != "GPU-"+node {
			t.Fatalf("%s resolved another Node's GPU: %#v", node, report.RuntimeAccess)
		}
	}
	selection := validCandidateSelection()
	selection.Node = "genesis1"
	if _, err := resolver.ResolveCandidate(context.Background(), selection); err == nil ||
		!strings.Contains(err.Error(), "no trusted execution declaration for genesis1") {
		t.Fatalf("Node without its own GPU declaration resolved: %v", err)
	}
}
