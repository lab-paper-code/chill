package deviceclass

import (
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
)

const (
	CatalogDataKey = "catalog.yaml"
)

// Catalog is the device-class discovery catalog loaded by the operator.
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
