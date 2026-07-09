package deviceclass

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const fallbackPowerModeName = "fixed"

// Options configures pure DeviceClass discovery policy.
type Options struct {
	LabelKey            string
	RequireCatalogMatch bool
}

// DiscoveredClass is the class name and spec inferred for one node.
type DiscoveredClass struct {
	Name string
	Spec edgev1alpha1.DeviceClassSpec
}

// Discover matches a node against the catalog and returns the DeviceClass to ensure.
func Discover(node *corev1.Node, catalog Catalog, opts Options) (DiscoveredClass, bool, error) {
	labelKey := opts.LabelKey
	if labelKey == "" {
		labelKey = chillmeta.DeviceClass
	}

	labels := node.GetLabels()
	entry, matched := catalog.match(labels)
	if !matched && opts.RequireCatalogMatch {
		return DiscoveredClass{}, false, nil
	}

	name := entry.Name
	if name == "" {
		name = inferredClassName(node)
	}

	memory := entry.Memory
	if memory.Cmp(resource.Quantity{}) == 0 {
		memory = inferredMemory(node)
	}

	architecture := entry.Architecture
	if architecture == "" {
		architecture = inferredArchitecture(node)
	}

	accelerator := entry.Accelerator
	if accelerator == "" {
		accelerator = inferredAccelerator(labels)
	}

	powerModes := entry.PowerModes
	if len(powerModes) == 0 && !matched {
		powerModes = []edgev1alpha1.PowerMode{{Name: fallbackPowerModeName}}
	}

	return DiscoveredClass{
		Name: name,
		Spec: edgev1alpha1.DeviceClassSpec{
			NodeSelector: map[string]string{
				labelKey: name,
			},
			Architecture: architecture,
			MemoryBytes:  memory,
			Accelerator:  accelerator,
			PowerModes:   powerModes,
		},
	}, true, nil
}
