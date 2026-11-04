package firstfloor

import (
	"errors"

	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// RuntimeContainer projects the frozen performance conditions. Callers add
// lifecycle commands, policy delivery, datasets and observation plumbing.
func RuntimeContainer(config *Config) (core.Container, error) {
	if config.RuntimeImage == "" || config.MemoryRequestBytes <= 0 || config.MemoryLimitBytes < config.MemoryRequestBytes {
		return core.Container{}, errors.New("runtime image and positive bounded memory envelope are required")
	}
	container := core.Container{
		Image: config.RuntimeImage,
		Resources: core.ResourceRequirements{
			Requests: core.ResourceList{core.ResourceMemory: *resource.NewQuantity(config.MemoryRequestBytes, resource.BinarySI)},
			Limits:   core.ResourceList{core.ResourceMemory: *resource.NewQuantity(config.MemoryLimitBytes, resource.BinarySI)},
		},
		VolumeMounts: []core.VolumeMount{
			{Name: "artifact", MountPath: "/model", ReadOnly: true},
			{Name: "preprocessor", MountPath: "/preprocessor", ReadOnly: true},
		},
	}
	if config.AcceleratorResource != "" {
		name := core.ResourceName(config.AcceleratorResource)
		quantity := *resource.NewQuantity(1, resource.DecimalSI)
		container.Resources.Requests[name] = quantity
		container.Resources.Limits[name] = quantity
	}
	if config.GPUUUID != "" {
		container.Env = []core.EnvVar{{Name: "CUDA_VISIBLE_DEVICES", Value: config.GPUUUID}}
	}
	return container, nil
}
