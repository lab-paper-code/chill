package kube

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	typedcore "k8s.io/client-go/kubernetes/typed/core/v1"
)

func TestObserverKeepsServingAcrossRuntimeDeclarationChange(t *testing.T) {
	c, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	c.config.Preparations = map[string]servingstate.PrepareSpec{}
	for _, profile := range c.observer.Profiles() {
		c.config.Preparations[profile.ID] = servingstate.PrepareSpec{Instance: serving.SchedulerInstance{
			ID: profile.ID, Node: serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID},
			Profile: &profile, MaxBatch: 1, Capacity: 4, Allocation: serving.SchedulerAllocation{MemoryBytes: 1024},
		}}
	}
	c.observer.config.RuntimeImage = func(context.Context, *serving.Profile) (string, error) {
		return "registry/native@sha256:" + strings.Repeat("b", 64), nil
	}
	if _, err := c.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tick(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, instance := range c.State().Snapshot.Instances {
		if !instance.Ready {
			t.Fatalf("declaration change removed a responding physical executor: %+v", instance)
		}
		if instance.ActualImageID == "" || instance.DesiredRuntimeImage == "" || instance.ReplacementReason != "RunningOlderRuntimeImage" {
			t.Fatalf("serving mixed images lost replacement evidence: %+v", instance)
		}
	}
	for _, workload := range c.State().Decision.Workloads {
		if workload.State != serving.WorkloadControlled {
			t.Fatalf("declaration change stopped workload control: %+v", workload)
		}
	}
	c.observer.config.RuntimeImage = nil
	if err := c.observer.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, instance := range c.runtime.Instances() {
		if !instance.Ready || instance.ActualImageID == "" {
			t.Fatalf("missing desired declaration invalidated an observed executor: %+v", instance)
		}
	}
}

func TestObserverMeasuredImageMismatchClosesDispatch(t *testing.T) {
	c, _ := tickReadySubsetController(t, nil)
	ctx := context.Background()
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "a", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses[0].ImageID = "containerd://sha256:" + strings.Repeat("b", 64)
	if _, err := c.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	state := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
	if state == nil || state.Ready || !strings.Contains(state.Reason, "measured runtime image") {
		t.Fatalf("different actual runtime image left dispatch enabled: %+v", state)
	}
}

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
				if !errors.Is(err, refreshCtx.Err()) {
					t.Fatalf("refresh outcome=%v, context=%v", err, refreshCtx.Err())
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			a := schedulerPhysicalState(before.ID, c.runtime.Instances())
			if a == nil || !a.Ready {
				t.Fatalf("another source's failed probe invalidated verified A: %+v", a)
			}
			b := schedulerPhysicalState("b-pod/containerd://running", c.runtime.Instances())
			if b == nil || b.Ready != (outcome == "success") {
				t.Fatalf("wrong probe outcome: %+v", b)
			}
		})
	}
}

func TestObserverRetriesTimedOutSourceOnNextRefresh(t *testing.T) {
	api := newObserverAPI(t, 1)
	issues := make(chan observation.ClusterObservation, 16)
	api.observer.config.OnCluster = func(row observation.ClusterObservation) error {
		if row.Kind == "issue" {
			issues <- row
		}
		return nil
	}
	api.refresh(t)
	before := api.observer.runtime.Instances()[0]
	api.mu.Lock()
	api.hang = make(chan struct{})
	api.mu.Unlock()
	api.refresh(t)
	failed := api.observer.runtime.Instances()[0]
	if failed.Ready || !failed.AttemptedAt.After(before.AttemptedAt) {
		t.Fatalf("timeout did not exclude physical executor: %+v", failed)
	}
	api.mu.Lock()
	close(api.hang)
	api.hang = nil
	api.mu.Unlock()
	api.refresh(t)
	recovered := api.observer.runtime.Instances()[0]
	if !recovered.Ready || recovered.ID != before.ID || !recovered.ObservedAt.After(before.ObservedAt) {
		t.Fatalf("unchanged Pod did not recover on the next refresh: %+v", recovered)
	}
	var raised, resolved bool
	for len(issues) > 0 {
		row := <-issues
		if row.Source == "chill-system/instance-a" {
			raised = raised || row.Event == "raised"
			resolved = resolved || row.PreviousReason != "" && row.Reason == row.PreviousReason
		}
	}
	if !raised || !resolved {
		t.Fatalf("probe recovery did not record issue lifecycle: raised=%t resolved=%t", raised, resolved)
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
	// Pause capture's POOL Pod read after its own refresh, before runtime copy.
	paused, resume := make(chan struct{}), make(chan struct{})
	var armed atomic.Bool
	armed.Store(true)
	c.kube = gatedObserverKube{Interface: c.kube, core: gatedObserverCore{
		CoreV1Interface: c.kube.CoreV1(),
		gate: func(ctx context.Context) error {
			if armed.CompareAndSwap(true, false) {
				close(paused)
				select {
				case <-resume:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		},
	}}
	tickDone := make(chan error, 1)
	go func() { _, err := c.Tick(ctx, time.Now()); tickDone <- err }()
	select {
	case <-paused:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
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

type gatedObserverKube struct {
	kubernetes.Interface
	core typedcore.CoreV1Interface
}

func (k gatedObserverKube) CoreV1() typedcore.CoreV1Interface { return k.core }

type gatedObserverCore struct {
	typedcore.CoreV1Interface
	gate func(context.Context) error
}

func (c gatedObserverCore) Pods(namespace string) typedcore.PodInterface {
	return gatedObserverPods{PodInterface: c.CoreV1Interface.Pods(namespace), gate: c.gate}
}

type gatedObserverPods struct {
	typedcore.PodInterface
	gate func(context.Context) error
}

func (p gatedObserverPods) List(ctx context.Context, opts meta.ListOptions) (*core.PodList, error) {
	if err := p.gate(ctx); err != nil {
		return nil, err
	}
	return p.PodInterface.List(ctx, opts)
}

func TestObserverNativeFailureClosesDispatchBeforePodVerification(t *testing.T) {
	for _, failure := range []string{"not ready", "metadata mismatch"} {
		t.Run(failure, func(t *testing.T) {
			var armed, gateNext atomic.Bool
			releaseA, releaseList, blocked := make(chan struct{}), make(chan struct{}), make(chan struct{})
			defer close(releaseA)
			defer close(releaseList)
			c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
				if !armed.Load() {
					return
				}
				if index == 0 {
					<-releaseA
					return
				}
				if failure == "not ready" {
					state.Ready = false
				} else {
					state.Metadata.ProfileID = "wrong-profile"
				}
				gateNext.Store(true)
			})
			prepared.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			before := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
			if before == nil {
				t.Fatal("fixture lacks A")
			}
			kube := c.observer.kube
			c.observer.kube = gatedObserverKube{Interface: kube, core: gatedObserverCore{
				CoreV1Interface: kube.CoreV1(),
				gate: func(ctx context.Context) error {
					if gateNext.CompareAndSwap(true, false) {
						close(blocked)
						select {
						case <-releaseList:
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					return nil
				},
			}}
			armed.Store(true)
			done := make(chan error, 1)
			go func() { done <- c.observer.Refresh(ctx) }()
			select {
			case <-blocked:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			// B's HTTP outcome is already known, but identity verification is
			// deliberately outstanding. Dispatch must already be closed.
			b := schedulerPhysicalState("b-pod/containerd://running", c.runtime.Instances())
			if b == nil || b.Ready {
				t.Fatalf("native failure left dispatch enabled: %+v", b)
			}
			releaseA <- struct{}{}
			for {
				a := schedulerPhysicalState(before.ID, c.runtime.Instances())
				if a != nil && a.AttemptedAt.After(before.AttemptedAt) {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				default:
					runtime.Gosched()
				}
			}
			b = schedulerPhysicalState("b-pod/containerd://running", c.runtime.Instances())
			if b == nil || b.Ready {
				t.Fatalf("A publication re-enabled failed B: %+v", b)
			}
			releaseList <- struct{}{}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			b = schedulerPhysicalState("b-pod/containerd://running", c.runtime.Instances())
			if b == nil || b.Ready || b.Native == nil {
				t.Fatalf("verified failure lost native evidence: %+v", b)
			}
		})
	}
}
