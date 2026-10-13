package controller

import (
	"errors"
	"fmt"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

const (
	cpuORTModelDestination = "/model/model.onnx"
	rknnModelDestination   = "/model/model.rknn"
)

type runtimeJobProjection struct {
	ArtifactDestination string
	SupplementalGroups  []int64
	SecurityContext     *corev1.SecurityContext
	VolumeMounts        []corev1.VolumeMount
	Volumes             []corev1.Volume
	AcceleratorResource corev1.ResourceName
}

func projectRuntimeJob(
	config profilingrun.RunConfig,
) (runtimeJobProjection, error) {
	firstfloor := profilingrun.IsSupportedFirstfloorRunConfig(config)
	if profilingrun.IsSupportedCPUORTRunConfig(config) ||
		(firstfloor && config.State.Backend == profilingrun.FirstfloorORTCPUBackend) {
		return runtimeJobProjection{
			ArtifactDestination: cpuORTModelDestination,
			SecurityContext:     restrictedContainerSecurityContext(),
		}, nil
	}
	cuda := firstfloor && config.State.Backend == profilingrun.FirstfloorORTCUDABackend
	rknn := profilingrun.IsSupportedRKNNLiteRunConfig(config) ||
		(firstfloor && config.State.Backend == profilingrun.FirstfloorRKNNLiteBackend)
	if !cuda && !rknn {
		return runtimeJobProjection{}, errors.New("no Job projection exists for the resolved RunConfig")
	}
	delivery := config.Delivery
	if delivery.ArtifactCapability == nil || delivery.RuntimeAccess == nil {
		return runtimeJobProjection{}, errors.New("verified accelerator delivery contract is incomplete")
	}
	projection := runtimeJobProjection{
		ArtifactDestination: cpuORTModelDestination,
		SupplementalGroups:  append([]int64(nil), delivery.RuntimeAccess.SupplementalGroups...),
		SecurityContext:     runtimeAccessSecurityContext(*delivery.RuntimeAccess),
	}
	if cuda {
		if delivery.RuntimeAccess.GPUUUID == "" || len(delivery.RuntimeAccess.Resources) != 1 ||
			delivery.RuntimeAccess.Resources["nvidia.com/gpu"] != "1" {
			return runtimeJobProjection{}, errors.New("CUDA Job requires declared GPU identity and exactly one GPU resource")
		}
		projection.AcceleratorResource = "nvidia.com/gpu"
		if delivery.HostContract == nil {
			return projection, nil
		}
	} else {
		if delivery.HostContract == nil || delivery.HostContract.Platform == nil {
			return runtimeJobProjection{}, errors.New("verified RKNNLite delivery contract is incomplete")
		}
		projection.ArtifactDestination = rknnModelDestination
		projection.AcceleratorResource = corev1.ResourceName(delivery.HostContract.Allocation.ResourceName)
	}
	seenHostPaths := make(map[string]struct{})
	seenMountPaths := make(map[string]struct{})
	appendHost := func(name, hostPath, mountPath string, pathType corev1.HostPathType, readOnly bool) error {
		if _, duplicate := seenHostPaths[hostPath]; duplicate {
			return fmt.Errorf("accelerator host path %q is projected more than once", hostPath)
		}
		if _, duplicate := seenMountPaths[mountPath]; duplicate {
			return fmt.Errorf("accelerator container path %q is projected more than once", mountPath)
		}
		seenHostPaths[hostPath] = struct{}{}
		seenMountPaths[mountPath] = struct{}{}
		projection.Volumes = append(projection.Volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{
				Path: hostPath, Type: &pathType,
			}},
		})
		projection.VolumeMounts = append(projection.VolumeMounts, corev1.VolumeMount{
			Name: name, MountPath: mountPath, ReadOnly: readOnly,
		})
		return nil
	}
	for index, library := range delivery.HostContract.Libraries {
		if err := appendHost(
			fmt.Sprintf("runtime-host-library-%d", index),
			library.HostPath,
			library.ContainerPath,
			corev1.HostPathFile,
			true,
		); err != nil {
			return runtimeJobProjection{}, err
		}
	}
	if err := appendHost(
		"runtime-host-driver",
		delivery.HostContract.Driver.VersionPath,
		delivery.HostContract.Driver.VersionPath,
		corev1.HostPathFile,
		true,
	); err != nil {
		return runtimeJobProjection{}, err
	}
	if delivery.HostContract.Platform != nil {
		if err := appendHost(
			"runtime-host-platform",
			delivery.HostContract.Platform.CompatiblePath,
			delivery.HostContract.Platform.CompatiblePath,
			corev1.HostPathFile,
			true,
		); err != nil {
			return runtimeJobProjection{}, err
		}
	}
	for index, device := range delivery.HostContract.Devices {
		if err := appendHost(
			fmt.Sprintf("runtime-host-device-%d", index),
			device.Path,
			device.Path,
			corev1.HostPathCharDev,
			false,
		); err != nil {
			return runtimeJobProjection{}, err
		}
	}
	return projection, nil
}

func runtimeAccessSecurityContext(access profilingrun.CandidateReportRuntimeAccess) *corev1.SecurityContext {
	security := restrictedContainerSecurityContext()
	security.Privileged = ptr.To(access.Privileged)
	security.AllowPrivilegeEscalation = ptr.To(access.Privileged)
	return security
}
