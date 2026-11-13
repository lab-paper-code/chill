package observation

import (
	"context"
	"testing"
	"time"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// The peak is the kernel's lifetime counter where a sample carries it,
// otherwise the largest current-usage sample, over the Pod's containers
// received in the window.
func TestPodMemoryPeakReportsItsBasis(t *testing.T) {
	ctx := context.Background()
	log, clock := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	node := serving.NodeReference{Name: "node", UID: "node-uid"}
	bytes := func(v uint64) *uint64 { return &v }
	record := func(at time.Time, containers ...ContainerResources) {
		t.Helper()
		clock.set(at)
		row := ResourceObservation{Kind: "node", Node: &node, ReceivedAt: &at, Samples: []ResourceSample{{SampledAt: at, Containers: containers}}}
		if err := log.ResourcesRecord(row); err != nil {
			t.Fatal(err)
		}
	}
	sampled := func(pod, container string, current uint64) ContainerResources {
		return ContainerResources{ResourceTarget: ResourceTarget{PodUID: pod, ContainerID: container}, CgroupVersion: 2, MemoryCurrentBytes: bytes(current)}
	}
	start := observationTestHour.Add(time.Minute)
	record(start, sampled("current-pod", "init", 50), sampled("other-pod", "main", 900))
	record(start.Add(time.Second), sampled("current-pod", "main", 300))
	record(start.Add(2*time.Second), sampled("current-pod", "main", 200))
	peak := sampled("peak-pod", "main", 100)
	peak.MemoryPeakBytes = bytes(400)
	record(start.Add(3*time.Second), peak)
	record(start.Add(time.Hour), sampled("current-pod", "main", 999)) // after the window
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	from, to := start, start.Add(time.Minute)
	if got, err := log.PodMemoryPeak(ctx, "current-pod", from, to); err != nil || got != (PodMemoryPeak{Bytes: 300}) {
		t.Fatalf("current-usage peak %+v %v", got, err)
	}
	if got, err := log.PodMemoryPeak(ctx, "peak-pod", from, to); err != nil || got != (PodMemoryPeak{Bytes: 400, Lifetime: true}) {
		t.Fatalf("lifetime peak %+v %v", got, err)
	}
	if _, err := log.PodMemoryPeak(ctx, "absent-pod", from, to); err == nil {
		t.Fatal("a Pod without memory samples has a peak")
	}
}
