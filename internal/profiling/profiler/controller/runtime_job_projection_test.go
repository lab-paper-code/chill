package controller

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/modelcompat"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
)

func TestFirstfloorCUDAJobProjectsDeclaredAcceleratorAccess(t *testing.T) {
	config := profilingrun.RunConfig{
		SchemaVersion: profilingrun.RunConfigSchemaVersion,
		Candidate:     profilingrun.CandidateIdentity{ExecutionPath: "native-cuda"},
		State: profilingrun.ExecutionState{
			RuntimeFamily: profilingrun.FirstfloorRuntimeFamily, Backend: profilingrun.FirstfloorORTCUDABackend,
			Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 32},
		},
		Measurement: profilingrun.MeasurementContract{WorkloadMode: profilingrun.SaturatedSerialFixedBatchWorkloadMode},
		Delivery: profilingrun.CandidateRuntimeDelivery{
			ArtifactCapability: &profilingrun.CandidateArtifactCapability{},
			RuntimeAccess: &profilingrun.CandidateReportRuntimeAccess{
				GPUUUID: "GPU-actual-device", Resources: map[string]string{"nvidia.com/gpu": "1"},
				SupplementalGroups: []int64{44}, Privileged: true,
			},
		},
	}
	projection, err := projectRuntimeJob(config)
	if err != nil {
		t.Fatal(err)
	}
	if projection.AcceleratorResource != "nvidia.com/gpu" || projection.ArtifactDestination != cpuORTModelDestination ||
		projection.SecurityContext.Privileged == nil || !*projection.SecurityContext.Privileged ||
		len(projection.SupplementalGroups) != 1 || projection.SupplementalGroups[0] != 44 {
		t.Fatalf("CUDA access was lost: %+v", projection)
	}
	config.Delivery.HostContract = &profilingrun.CandidateReportHostContract{
		Libraries: []profilingrun.CandidateReportHostLibrary{{Name: "cuda", HostPath: "/usr/lib/libcuda.so", ContainerPath: "/usr/lib/libcuda.so"}},
		Driver:    profilingrun.CandidateReportHostDriver{VersionPath: "/proc/driver/nvidia/version"},
		Devices:   []profilingrun.CandidateReportHostDevice{{Path: "/dev/nvidia0"}},
	}
	projection, err = projectRuntimeJob(config)
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Volumes) != 3 || len(projection.VolumeMounts) != 3 ||
		projection.Volumes[2].HostPath.Type == nil || *projection.Volumes[2].HostPath.Type != corev1.HostPathCharDev ||
		projection.VolumeMounts[2].ReadOnly {
		t.Fatalf("declared CUDA host boundary was lost: %+v", projection)
	}
	config.Delivery.RuntimeAccess.Resources = nil
	if _, err := projectRuntimeJob(config); err == nil {
		t.Fatal("CUDA silently fell back without a GPU resource")
	}
	config.Delivery.RuntimeAccess = nil
	if _, err := projectRuntimeJob(config); err == nil {
		t.Fatal("CUDA silently fell back without runtime access")
	}
}
