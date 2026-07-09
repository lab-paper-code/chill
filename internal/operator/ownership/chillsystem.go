package ownership

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

// BelongsToChillSystem reports whether an object is owned by or labeled for a ChillSystem.
func BelongsToChillSystem(object metav1.Object, system *edgev1alpha1.ChillSystem) bool {
	if system.UID != "" {
		for _, owner := range object.GetOwnerReferences() {
			if owner.APIVersion == edgev1alpha1.GroupVersion.String() &&
				owner.Kind == "ChillSystem" &&
				owner.Name == system.Name &&
				owner.UID == system.UID {
				return true
			}
		}
	}
	return object.GetLabels()[chillmeta.System] == system.Name
}

// EnsureSystemLabel adds the ChillSystem scope label to an object.
func EnsureSystemLabel(object metav1.Object, systemName string) {
	labels := object.GetLabels()
	if labels == nil {
		labels = map[string]string{}
		object.SetLabels(labels)
	}
	labels[chillmeta.System] = systemName
}
