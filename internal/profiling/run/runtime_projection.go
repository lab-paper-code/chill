package profilingrun

import (
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

// RuntimeDeliveryProjection is the Kubernetes host access that reproduces a
// frozen CUDA or RKNNLite delivery for the firstfloor runtime container.
type RuntimeDeliveryProjection struct {
	Privileged          bool
	SupplementalGroups  []int64
	VolumeMounts        []corev1.VolumeMount
	Volumes             []corev1.Volume
	AcceleratorResource corev1.ResourceName
}

// ProjectRuntimeDelivery projects an accelerator backend's frozen delivery.
// Profiling Jobs and serving Pods share it so both receive the same host
// libraries, driver, platform, devices, groups and privilege.
func ProjectRuntimeDelivery(backend string, delivery CandidateRuntimeDelivery) (RuntimeDeliveryProjection, error) {
	if delivery.ArtifactCapability == nil || delivery.RuntimeAccess == nil {
		return RuntimeDeliveryProjection{}, errors.New("verified accelerator delivery contract is incomplete")
	}
	projection := RuntimeDeliveryProjection{
		Privileged:         delivery.RuntimeAccess.Privileged,
		SupplementalGroups: append([]int64(nil), delivery.RuntimeAccess.SupplementalGroups...),
	}
	if backend == FirstfloorORTCUDABackend {
		if delivery.RuntimeAccess.GPUUUID == "" || len(delivery.RuntimeAccess.Resources) != 1 ||
			delivery.RuntimeAccess.Resources["nvidia.com/gpu"] != "1" {
			return RuntimeDeliveryProjection{}, errors.New("CUDA Job requires declared GPU identity and exactly one GPU resource")
		}
		projection.AcceleratorResource = "nvidia.com/gpu"
		if delivery.HostContract == nil {
			return projection, nil
		}
	} else {
		if backend != FirstfloorRKNNLiteBackend || delivery.HostContract == nil || delivery.HostContract.Platform == nil {
			return RuntimeDeliveryProjection{}, errors.New("verified RKNNLite delivery contract is incomplete")
		}
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
			return RuntimeDeliveryProjection{}, err
		}
	}
	if err := appendHost(
		"runtime-host-driver",
		delivery.HostContract.Driver.VersionPath,
		delivery.HostContract.Driver.VersionPath,
		corev1.HostPathFile,
		true,
	); err != nil {
		return RuntimeDeliveryProjection{}, err
	}
	if delivery.HostContract.Platform != nil {
		if err := appendHost(
			"runtime-host-platform",
			delivery.HostContract.Platform.CompatiblePath,
			delivery.HostContract.Platform.CompatiblePath,
			corev1.HostPathFile,
			true,
		); err != nil {
			return RuntimeDeliveryProjection{}, err
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
			return RuntimeDeliveryProjection{}, err
		}
	}
	return projection, nil
}
