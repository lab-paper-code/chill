package kube

import (
	"context"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// Native B is held by a channel, not a timing window. A must finish its real
// probe/verification/publication before the assertions inspect the runtime.
func TestObserverRefreshRetainsReadyUntilProbeOutcome(t *testing.T) {
	const timeoutOutcome = "timeout"
	for _, outcome := range []string{"success", "error", "cancel", timeoutOutcome} {
		t.Run(outcome, func(t *testing.T) {
			var hold atomic.Bool
			entered, release := make(chan struct{}, 1), make(chan struct{})
			c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
				if index == 1 && hold.Load() {
					entered <- struct{}{}
					<-release
					if outcome == "error" {
						state.At = time.Time{}
					}
				}
			})
			prepared.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			before := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
			if before == nil || !before.Ready {
				t.Fatal("fixture lacks Ready A")
			}
			hold.Store(true)
			refreshCtx, stop := context.WithCancel(ctx)
			if outcome == timeoutOutcome {
				stop()
				refreshCtx, stop = context.WithTimeout(ctx, time.Second)
			}
			defer stop()
			done := make(chan error, 1)
			go func() { done <- c.observer.Refresh(refreshCtx) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			defer close(release)
			for {
				a := schedulerPhysicalState(before.ID, c.runtime.Instances())
				if a != nil && a.AttemptedAt.After(before.AttemptedAt) {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal("A did not publish", ctx.Err())
				default:
					runtime.Gosched()
				}
			}
			for _, state := range c.runtime.Instances() {
				if !state.Ready {
					t.Fatalf("partial publication invalidated healthy executor: %+v", state)
				}
			}
			if outcome == "cancel" {
				stop()
			}
			// Release before waiting; the handler's successful response is still
			// rejected when its probe context was cancelled.
			if outcome != timeoutOutcome {
				release <- struct{}{}
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			b := schedulerPhysicalState("b-pod/containerd://running", c.runtime.Instances())
			if b == nil || b.Ready != (outcome == "success") {
				t.Fatalf("wrong probe outcome: %+v", b)
			}
		})
	}
}

func TestObserverWatchInvalidationRecoversThroughTickAC37(t *testing.T) {
	for _, condition := range []string{"container unready", "pod unready"} {
		t.Run(condition, func(t *testing.T) {
			c, _ := tickReadySubsetController(t, nil)
			ctx := context.Background()
			if _, err := c.Tick(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "a", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			invalid := pod.DeepCopy()
			if condition == "container unready" {
				invalid.Status.ContainerStatuses[0].Ready = false
			} else {
				invalid.Status.Conditions = nil
			}
			w := watch.NewRaceFreeFake()
			w.Modify(invalid)
			w.Modify(pod) // Ready events alone must not restore eligibility.
			w.Stop()
			c.observer.wg.Add(1)
			c.observer.watchPods(w)
			if err := c.observer.publishLedger(nil); err != nil {
				t.Fatal(err)
			}
			if state := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances()); state == nil || state.Ready {
				t.Fatalf("watch failed closed: %+v", state)
			}
			if _, err := c.Tick(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			state := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
			if state == nil || !state.Ready {
				t.Fatalf("Tick did not restore dispatch: %+v", state)
			}
			for _, workload := range c.State().Decision.Workloads {
				if strings.Contains(workload.Reason, "UnconfirmedInstance:a-pod/") {
					t.Fatalf("recovered executor remains unconfirmed: %+v", workload)
				}
			}
			for _, node := range c.State().Snapshot.Nodes {
				if node.Node.Name == "a" && slices.Contains(node.Reasons, serving.NodeInstanceUnavailable) {
					t.Fatalf("recovered node remains unavailable: %+v", node)
				}
			}
			if actual, _ := c.runtime.SchedulerRouting(); actual[state.OwnerWorkloadUID][state.ID] == 0 {
				t.Fatal("recovered executor lost dispatch allocation")
			}
		})
	}
}

func TestTickCaptureDuringIndependentRefreshRetainsReady(t *testing.T) {
	var hold atomic.Bool
	entered, release := make(chan struct{}, 1), make(chan struct{})
	defer close(release)
	c, prepared := tickReadySubsetController(t, func(index int, _ *native.State) {
		if index == 1 && hold.Load() {
			entered <- struct{}{}
			<-release
		}
	})
	prepared.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Pause capture's class read after its own refresh, before runtime copy.
	paused, resume := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	var lists atomic.Int32
	c.store.client = interceptor.NewClient(c.store.client.(client.WithWatch), interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			_, classes := list.(*edge.DeviceClassList)
			if classes && armed.Load() && lists.Add(1) == 2 {
				close(paused)
				select {
				case <-resume:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return cl.List(ctx, list, opts...)
		},
	})
	tickDone := make(chan error, 1)
	go func() { _, err := c.Tick(ctx, time.Now()); tickDone <- err }()
	select {
	case <-paused:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	armed.Store(false)
	before := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
	if before == nil {
		t.Fatal("fixture lacks A")
	}
	hold.Store(true)
	refreshDone := make(chan error, 1)
	go func() { refreshDone <- c.observer.Refresh(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for {
		a := schedulerPhysicalState(before.ID, c.runtime.Instances())
		if a != nil && a.AttemptedAt.After(before.AttemptedAt) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("A did not publish", ctx.Err())
		default:
			runtime.Gosched()
		}
	}
	close(resume)
	select {
	case err := <-tickDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for _, instance := range c.State().Snapshot.Instances {
		if !instance.Ready {
			t.Fatalf("capture invented unconfirmed/fixed executor: %+v", instance)
		}
	}
	for _, workload := range c.State().Decision.Workloads {
		if workload.State == "Fixed" || strings.Contains(workload.Reason, "UnconfirmedInstance:") {
			t.Fatalf("refresh race fixed a healthy workload: %+v", workload)
		}
	}
	release <- struct{}{}
	select {
	case err := <-refreshDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestObserverRelabelledSourceClosesDispatch(t *testing.T) {
	c, _ := tickReadySubsetController(t, nil)
	ctx := context.Background()
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "a", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	delete(pod.Labels, "app.kubernetes.io/component")
	if _, err := c.kube.CoreV1().Pods(pod.Namespace).Update(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	state := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
	if state == nil || state.Ready || !strings.Contains(state.Reason, "not labelled") {
		t.Fatalf("label mismatch did not fail closed: %+v", state)
	}
}
