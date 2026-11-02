package kube

import (
	"context"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"strings"
	"testing"
)

func TestNativeObservationRejectsAdmissionInjectedCPUIncludingZero(t *testing.T) {
	for _, limit := range []bool{false, true} {
		container := core.Container{Name: "firstfloor"}
		if limit {
			container.Resources.Limits = core.ResourceList{core.ResourceCPU: resource.MustParse("0")}
		} else {
			container.Resources.Requests = core.ResourceList{core.ResourceCPU: resource.MustParse("0")}
		}
		pod := &core.Pod{Spec: core.PodSpec{Containers: []core.Container{container}}}
		if _, err := (&Observer{}).nativeInstance(context.Background(), pod, &container); err == nil || !strings.Contains(err.Error(), "CPU") {
			t.Fatalf("CPU resource passed native source boundary: %v", err)
		}
	}
}
