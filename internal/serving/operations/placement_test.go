package operations

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestPreparationPlacementKeepsOwnerAndPlannerRouting(t *testing.T) {
	owner := &serving.WorkloadReference{Name: "tight", UID: "w1"}
	instance := serving.SchedulerInstance{ID: "target-w1-profile-1", WorkloadUID: "w1", Weight: 1, MaxBatch: 1}
	pod := &core.Pod{ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{}}}
	(&Operations{}).placeServingPod(pod, owner, instance)
	if actual := servingstate.WorkloadOwner(pod.OwnerReferences); actual != "w1" || pod.Annotations[servingstate.ServingRoutingAnnotation] != "{}" {
		t.Fatalf("pod owner %q, routing %s", actual, pod.Annotations[servingstate.ServingRoutingAnnotation])
	}
	pod = &core.Pod{ObjectMeta: meta.ObjectMeta{Annotations: map[string]string{}}}
	(&Operations{}).placeServingPod(pod, owner, serving.SchedulerInstance{Weight: 3})
	if pod.Annotations[servingstate.ServingRoutingAnnotation] != `{"w1":3}` {
		t.Fatalf("a manual preparation must serve its owner at once: %s", pod.Annotations[servingstate.ServingRoutingAnnotation])
	}
}
