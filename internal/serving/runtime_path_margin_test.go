package serving

import (
	"context"
	"testing"
	"time"
)

// Each routing path keeps its own POST reserve: in control402 jetson-nano's
// 49 ms POST tail was applied to the other PLC paths.
func TestRuntimePathMarginIgnoresOtherPathTail(t *testing.T) {
	r, wanted, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.SetPathFeedback([]PathFeedback{{WorkloadUID: "w1", PostNanos: float64(49 * time.Millisecond), Source: pathFeedbackEmpirical, Paths: []PathPost{
		{Instance: "slow-path", PostNanos: float64(49 * time.Millisecond)}, {Instance: wanted.ID, PostNanos: float64(time.Millisecond)},
	}}})
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
