package deviceclass

import (
	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

// SpecEqual compares DeviceClass specs using Kubernetes API semantic equality.
func SpecEqual(a, b edgev1alpha1.DeviceClassSpec) bool {
	return apiequality.Semantic.DeepEqual(a, b)
}
