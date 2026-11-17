package kube

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

const (
	unansweredTimeout int32 = iota + 1
	unansweredGate
)

// unansweredHarness returns a two-instance controller whose B native read
// answers, hangs past nativeProbeTimeout, or waits for gate.
func unansweredHarness(t *testing.T) (*SchedulerController, *atomic.Int32, chan struct{}, chan struct{}) {
	t.Helper()
	var mode atomic.Int32
	hung, gate, entered := make(chan struct{}), make(chan struct{}), make(chan struct{}, 8)
	c, prepared := tickReadySubsetController(t, func(index int, _ *native.State) {
		if index != 1 {
			return
		}
		switch mode.Load() {
		case unansweredTimeout:
			<-hung
		case unansweredGate:
			entered <- struct{}{}
			<-gate
		}
	})
	// Runs before the harness cleanup closes the server (LIFO).
	t.Cleanup(func() {
		close(hung)
		select {
		case <-gate:
		default:
			close(gate)
		}
	})
	prepared.Store(true)
	if err := c.observer.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a-pod/containerd://running", "b-pod/containerd://running"} {
		if s := runtimeInstance(c, id); s == nil || !s.Ready {
			t.Fatalf("fixture %s not Ready: %+v %+v", id, s, c.observer.Issues())
		}
	}
	mode.Store(unansweredTimeout)
	started := time.Now()
	if err := c.observer.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.observer.mu.Lock()
	unanswered := c.observer.sources["b-pod"].unanswered
	c.observer.mu.Unlock()
	if !unanswered || time.Since(started) < nativeProbeTimeout {
		t.Fatalf("B not marked unanswered after %v", time.Since(started))
	}
	return c, &mode, gate, entered
}

func runtimeInstance(c *SchedulerController, id string) *serving.InstanceState {
	for _, s := range c.runtime.Instances() {
		if s.ID == id {
			return &s
		}
	}
	return nil
}

func waitDetachedRead(t *testing.T, o *Observer, uid string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		o.mu.Lock()
		s := o.sources[uid]
		busy := s != nil && s.reading
		o.mu.Unlock()
		if !busy {
			return
		}
	}
	t.Fatal("detached read never finished")
}

// A detached read of B publishes the source ledger outside any refresh; A
// stays closed while its Node changes power mode.
func TestDetachedReadKeepsMovingNodeClosed(t *testing.T) {
	c, mode, gate, entered := unansweredHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mode.Store(unansweredGate)
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	<-entered // B's detached read is in flight; the refresh has returned.
	if err := c.observer.BeginNodeMove(ctx, "a-node"); err != nil {
		t.Fatal(err)
	}
	if a := runtimeInstance(c, "a-pod/containerd://running"); a == nil || a.Ready {
		t.Fatalf("BeginNodeMove left A Ready: %+v", a)
	}
	close(gate)
	waitDetachedRead(t, c.observer, "b-pod")
	if a := runtimeInstance(c, "a-pod/containerd://running"); a != nil && a.Ready {
		t.Fatalf("detached read of B re-enabled A during its Node power transition: ready=%t reason=%q", a.Ready, a.Reason)
	}
}

// The mark is per Pod source, not per executor: a restarted container whose
// native server never missed a deadline is read detached and the refresh
