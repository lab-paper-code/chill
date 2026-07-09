package deviceclasscatalog

import (
	"fmt"
	"math"
	"regexp"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	chilllabels "github.com/lab-paper-code/chill/internal/labels"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	CatalogDataKey = "catalog.yaml"

	nodeArchBetaLabelKey = "beta.kubernetes.io/arch"
)

// Options configures pure DeviceClass discovery policy.
type Options struct {
	LabelKey            string
	RequireCatalogMatch bool
}

// Catalog is the device-class discovery catalog loaded by the controller.
type Catalog struct {
	Classes []CatalogEntry `json:"classes"`
}

// CatalogEntry maps observed node labels to one DeviceClass spec.
type CatalogEntry struct {
	Name         string                   `json:"name"`
	MatchLabels  map[string]string        `json:"matchLabels,omitempty"`
	Architecture string                   `json:"architecture,omitempty"`
	Memory       resource.Quantity        `json:"memory,omitempty"`
	Accelerator  string                   `json:"accelerator,omitempty"`
	PowerModes   []edgev1alpha1.PowerMode `json:"powerModes,omitempty"`
}

// DiscoveredClass is the class name and spec inferred for one node.
type DiscoveredClass struct {
	Name string
	Spec edgev1alpha1.DeviceClassSpec
}

// ValidateCatalog validates the discovery catalog before it is used for node matching.
func ValidateCatalog(catalog Catalog) error {
	var allErrs field.ErrorList
	seen := map[string]struct{}{}
	for i, entry := range catalog.Classes {
		path := field.NewPath("classes").Index(i)
		if entry.Name == "" {
			allErrs = append(allErrs, field.Required(path.Child("name"), ""))
		} else {
			if errs := validation.IsDNS1123Subdomain(entry.Name); len(errs) > 0 {
				allErrs = append(allErrs, field.Invalid(path.Child("name"), entry.Name, strings.Join(errs, "; ")))
			}
			if _, ok := seen[entry.Name]; ok {
				allErrs = append(allErrs, field.Duplicate(path.Child("name"), entry.Name))
			}
			seen[entry.Name] = struct{}{}
		}
		if len(entry.MatchLabels) == 0 {
			allErrs = append(allErrs, field.Required(path.Child("matchLabels"), ""))
		}
		if len(entry.PowerModes) == 0 {
			allErrs = append(allErrs, field.Required(path.Child("powerModes"), ""))
		}
	}
	return allErrs.ToAggregate()
}

// Discover matches a node against the catalog and returns the DeviceClass to ensure.
func Discover(node *corev1.Node, catalog Catalog, opts Options) (DiscoveredClass, bool, error) {
	labelKey := opts.LabelKey
	if labelKey == "" {
		labelKey = chilllabels.DeviceClass
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
		architecture = node.Status.NodeInfo.Architecture
	}
	if architecture == "" {
		architecture = labels[corev1.LabelArchStable]
	}
	if architecture == "" {
		architecture = labels[nodeArchBetaLabelKey]
	}
	if architecture == "" {
		architecture = "unknown"
	}

	accelerator := entry.Accelerator
	if accelerator == "" {
		accelerator = inferredAccelerator(labels)
	}

	powerModes := entry.PowerModes
	if len(powerModes) == 0 && !matched {
		powerModes = []edgev1alpha1.PowerMode{{Name: "fixed"}}
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

// SpecEqual compares DeviceClass specs using Kubernetes API semantic equality.
func SpecEqual(a, b edgev1alpha1.DeviceClassSpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}

func (c Catalog) match(labels map[string]string) (CatalogEntry, bool) {
	for _, entry := range c.Classes {
		if matchLabels(labels, entry.MatchLabels) {
			return entry, true
		}
	}
	return CatalogEntry{}, false
}

func matchLabels(labels, selector map[string]string) bool {
	if len(selector) == 0 {
		return false
	}
	for key, value := range selector {
		if labels[key] != value {
			return false
		}
	}
	return true
}

func inferredClassName(node *corev1.Node) string {
	labels := node.GetLabels()
	base := firstNonEmpty(labels[chilllabels.DeviceModel], labels["jetson-model"], labels[chilllabels.DeviceFamily], labels["device-family"], "generic")
	if base == "generic" {
		base = fmt.Sprintf("%s-%s", base, firstNonEmpty(node.Status.NodeInfo.Architecture, labels[corev1.LabelArchStable], labels[nodeArchBetaLabelKey], "unknown"))
	}
	return sanitizeDNS1123(fmt.Sprintf("%s-%s", base, memorySuffix(inferredMemory(node))))
}

func inferredMemory(node *corev1.Node) resource.Quantity {
	nodeLabels := node.GetLabels()
	if gpuMemoryMi, ok := nodeLabels["nvidia.com/gpu.memory"]; ok && firstNonEmpty(nodeLabels[chilllabels.DeviceFamily], nodeLabels["device-family"]) == "jetson" {
		parsed, err := resource.ParseQuantity(gpuMemoryMi + "Mi")
		if err == nil && parsed.Sign() > 0 {
			return parsed
		}
	}

	memory := node.Status.Capacity.Memory()
	if memory == nil || memory.Sign() <= 0 {
		return resource.MustParse("1Gi")
	}
	gib := int64(math.Ceil(float64(memory.Value()) / float64(1024*1024*1024)))
	if gib < 1 {
		gib = 1
	}
	return resource.MustParse(fmt.Sprintf("%dGi", gib))
}

func memorySuffix(memory resource.Quantity) string {
	bytes := memory.Value()
	if bytes <= 0 {
		return "unknown-memory"
	}
	gib := int64(math.Round(float64(bytes) / float64(1024*1024*1024)))
	if gib < 1 {
		gib = 1
	}
	return fmt.Sprintf("%dg", gib)
}

func inferredAccelerator(labels map[string]string) string {
	if value := firstNonEmpty(labels[chilllabels.Accelerator], labels["accelerator"]); value != "" {
		return value
	}
	if labels["nvidia.com/gpu.present"] == "true" || labels["nvidia.com/gpu.count"] != "" {
		return "nvidia-gpu"
	}
	if firstNonEmpty(labels[chilllabels.DeviceFamily], labels["device-family"]) == "jetson" {
		return "nvidia-jetson"
	}
	return "none"
}

var dns1123Cleanup = regexp.MustCompile(`[^a-z0-9.-]+`)

func sanitizeDNS1123(value string) string {
	out := strings.ToLower(value)
	out = dns1123Cleanup.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-.")
	if out == "" {
		return "unknown"
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
