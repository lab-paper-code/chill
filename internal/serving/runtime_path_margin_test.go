package serving

import (
	"context"
	"testing"
	"time"
)

// Each routing path keeps its own POST reserve (design 1.5.4): in control402
// jetson-nano's 49 ms POST tail was applied to the other PLC paths.
func TestRuntimePathMarginIgnoresOtherPathTail(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	estimate := func(slowPost time.Duration) AdmissionEstimate {
		pooled := max(slowPost, time.Millisecond)
		r.SetPathFeedback([]PathFeedback{{WorkloadUID: "w1", PostNanos: float64(pooled), Source: pathFeedbackEmpirical, Paths: []PathPost{
			{Instance: "slow-path", PostNanos: float64(slowPost)}, {Instance: wanted.ID, PostNanos: float64(time.Millisecond)},
		}}})
		r.mu.Lock()
		defer r.mu.Unlock()
		now := time.Now()
		r.beginAdmissionLocked(now)
		defer r.endAdmissionLocked()
		definition := r.workloads["w1"].definition
		c := &Call{receipt: Receipt{ID: "candidate", UID: "w1", ReceivedAt: now}, definition: definition, identity: r.workloads["w1"].identity}
		return r.estimateLocked(r.instances[wanted.ID], c, now)
	}
	fast, slow := estimate(time.Millisecond), estimate(49*time.Millisecond)
	if !fast.Known || fast != slow {
		t.Fatalf("another path's POST tail changed this path's admission: %+v vs %+v", fast, slow)
	}
	// The routed call freezes its own path's POST, not the pooled 49 ms tail.
	c, receipt := admissionRuntimeCall(t, r, "w1", time.Now(), time.Now().Add(time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.Wait(ctx); err != nil || c.completionMarginMS != 1 {
		t.Fatalf("routed call lost its path reserve: %v margin=%g", err, c.completionMarginMS)
	}
	if err := r.Response(receipt, outcomeCompleted, ""); err != nil {
		t.Fatal(err)
	}
}
