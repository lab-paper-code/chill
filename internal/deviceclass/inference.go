package deviceclass

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	defaultArchitecture      = "unknown"
	defaultMemory            = "1Gi"
	genericDeviceClassPrefix = "generic"
	unknownMemorySuffix      = "unknown-memory"

	nodeArchBetaLabelKey       = "beta.kubernetes.io/arch"
	compatJetsonModelLabelKey  = "jetson-model"
	compatDeviceFamilyLabelKey = "device-family"
	compatAcceleratorLabelKey  = "accelerator"

	nvidiaGPUMemoryLabelKey  = "nvidia.com/gpu.memory"
	nvidiaGPUPresentLabelKey = "nvidia.com/gpu.present"
	nvidiaGPUCountLabelKey   = "nvidia.com/gpu.count"
	nvidiaMemoryUnit         = "Mi"
	nvidiaJetsonFamily       = "jetson"
	labelValueTrue           = "true"

	acceleratorNVIDIAGPU    = "nvidia-gpu"
	acceleratorNVIDIAJetson = "nvidia-jetson"
	acceleratorNone         = "none"

	bytesPerGiB = 1024 * 1024 * 1024
)

func inferredArchitecture(node *corev1.Node) string {
	labels := node.GetLabels()
	return firstNonEmpty(
		node.Status.NodeInfo.Architecture,
		labels[corev1.LabelArchStable],
		labels[nodeArchBetaLabelKey],
		defaultArchitecture,
	)
}

func inferredClassName(node *corev1.Node) string {
	labels := node.GetLabels()
	base := firstNonEmpty(
		labels[chillmeta.DeviceModel],
		labels[compatJetsonModelLabelKey],
		labels[chillmeta.DeviceFamily],
		labels[compatDeviceFamilyLabelKey],
		genericDeviceClassPrefix,
	)
	if base == genericDeviceClassPrefix {
		base = fmt.Sprintf("%s-%s", base, inferredArchitecture(node))
	}
	return sanitizeDNS1123(fmt.Sprintf("%s-%s", base, memorySuffix(inferredMemory(node))))
}

func inferredMemory(node *corev1.Node) resource.Quantity {
	nodeLabels := node.GetLabels()
	if gpuMemoryMi, ok := nodeLabels[nvidiaGPUMemoryLabelKey]; ok && deviceFamily(nodeLabels) == nvidiaJetsonFamily {
		parsed, err := resource.ParseQuantity(gpuMemoryMi + nvidiaMemoryUnit)
		if err == nil && parsed.Sign() > 0 {
			return parsed
		}
	}

	memory := node.Status.Capacity.Memory()
	if memory == nil || memory.Sign() <= 0 {
		return resource.MustParse(defaultMemory)
	}
	gib := int64(math.Ceil(float64(memory.Value()) / float64(bytesPerGiB)))
	if gib < 1 {
		gib = 1
	}
	return resource.MustParse(fmt.Sprintf("%dGi", gib))
}

func memorySuffix(memory resource.Quantity) string {
	bytes := memory.Value()
	if bytes <= 0 {
		return unknownMemorySuffix
	}
	gib := int64(math.Round(float64(bytes) / float64(bytesPerGiB)))
	if gib < 1 {
		gib = 1
	}
	return fmt.Sprintf("%dg", gib)
}

func inferredAccelerator(labels map[string]string) string {
	if value := firstNonEmpty(labels[chillmeta.Accelerator], labels[compatAcceleratorLabelKey]); value != "" {
		return value
	}
	if labels[nvidiaGPUPresentLabelKey] == labelValueTrue || labels[nvidiaGPUCountLabelKey] != "" {
		return acceleratorNVIDIAGPU
	}
	if deviceFamily(labels) == nvidiaJetsonFamily {
		return acceleratorNVIDIAJetson
	}
	return acceleratorNone
}

func deviceFamily(labels map[string]string) string {
	return firstNonEmpty(labels[chillmeta.DeviceFamily], labels[compatDeviceFamilyLabelKey])
}

var dns1123Cleanup = regexp.MustCompile(`[^a-z0-9.-]+`)

func sanitizeDNS1123(value string) string {
	out := strings.ToLower(value)
	out = dns1123Cleanup.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-.")
	if out == "" {
		return defaultArchitecture
	}
	if len(out) > validation.DNS1123SubdomainMaxLength {
		out = strings.Trim(out[:validation.DNS1123SubdomainMaxLength], "-.")
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
