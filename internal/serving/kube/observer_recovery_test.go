package kube

import (
	"context"
	"encoding/json"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type recoveryBootObservation struct {
	serving.RuntimeObservation
	started time.Time
}

func (o recoveryBootObservation) Recovery() serving.ObservationRecovery {
	return serving.ObservationRecovery{Unknown: true, Started: o.started}
}

// Reuse the real recorder and source/operation adapters with a boot that has a
// lost tail. No HTTP request is reconstructed from the missing evidence.
func recoveryBootRuntime(t *testing.T, c *SchedulerController) {
	t.Helper()
	definitions := c.runtime.SchedulerInputs(time.Now).Workloads
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	rt, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 8,
		ObservationLog: recoveryBootObservation{RuntimeObservation: c.observationLog, started: time.Now().UTC()}})
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range definitions {
		if err := rt.Publish(definition); err != nil {
			t.Fatal(err)
		}
	}
	c.runtime, c.observer.runtime, c.operations.Runtime = rt, rt, rt
	rt.EnableSchedulerRouting()
}

func recoveryPeerScenario(t *testing.T, condition string) (*SchedulerController, *atomic.Bool) {
	t.Helper()
	recovering := &atomic.Bool{}
	recovering.Store(true)
	c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
		if index != 1 || !recovering.Load() {
			return
		}
		switch condition {
		case "busy":
			state.Requests = []native.RequestState{{Phase: "running"}}
		case "failed read with stale empty state":
			state.At = time.Time{}
		case "metadata mismatch":
			state.Metadata.ProfileID = "not-the-measured-profile"
		}
	})
	prepared.Store(true)
	recoveryBootRuntime(t, c)
	// A failed read must not recycle this previous boot's empty actor.
	for _, profile := range c.observer.Profiles() {
		if profile.NodeName == "b" {
			c.observer.sources["b-pod"].instance = serving.ReadyInstance{Profile: &profile,
				State: serving.InstanceState{ID: "b-pod/containerd://running", PodUID: "b-pod",
					Node: serving.NodeReference{Name: "b", UID: "b-node"}, Profile: profile.ID,
					ObservedAt: c.runtime.Recovery().Started.Add(-time.Second), Native: &native.State{Ready: true}}}
		}
	}
	kube := c.kube.(*kubefake.Clientset)
	bPod, err := kube.CoreV1().Pods("chill-system").Get(context.Background(), "b", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("list", "pods", func(action ktesting.Action) (bool, runtime.Object, error) {
		list := action.(ktesting.ListAction)
		if !recovering.Load() || list.GetListRestrictions().Labels.String() != servingstate.InstancePodUIDLabel+"=b-pod" ||
			condition != "ambiguous Service" && condition != "changed container" {
			return false, nil, nil
		}
		pod := bPod.DeepCopy()
		result := &core.PodList{Items: []core.Pod{*pod}}
		if condition == "ambiguous Service" {
			duplicate := pod.DeepCopy()
			duplicate.Name, duplicate.UID = "duplicate-native-source", types.UID("ambiguous-pod")
			result.Items = append(result.Items, *duplicate)
		} else {
			result.Items[0].Status.ContainerStatuses[0].ContainerID = "containerd://changed-during-recovery"
		}
		return true, result, nil
	})
	return c, recovering
}

func TestRecoveryFreshExecutorRoutesWhileUnconfirmedPeerRetainsPhysicalHold(t *testing.T) {
	for _, condition := range []string{"busy", "failed read with stale empty state", "metadata mismatch", "ambiguous Service", "changed container"} {
		t.Run(condition, func(t *testing.T) {
			c, recovering := recoveryPeerScenario(t, condition)
			ctx := context.Background()
			if err := c.observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			a := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
			b := schedulerPhysicalState("b-pod/containerd://running", c.runtime.Instances())
			if a == nil || !a.Ready || a.RecoveryUnknown || b == nil || !b.RecoveryUnknown || b.Ready {
				t.Fatalf("fresh A did not recover independently of B: A=%+v B=%+v", a, b)
			}
			uid := c.runtime.SchedulerInputs(time.Now).Workloads[0].UID
			var profile *serving.Profile
			for _, p := range c.observer.Profiles() {
				if p.ID == a.Profile {
					copy := p
					profile = &copy
				}
			}
			instance := serving.SchedulerInstance{ID: a.ID, PodUID: a.PodUID, Node: a.Node, Profile: profile,
				WorkloadUID: uid, Allocation: a.Allocation, Ready: true, Weight: 1, MaxBatch: 1, Capacity: 4}
			target := serving.SchedulerCandidate{ID: "fresh-executor-route", Instances: []serving.SchedulerInstance{instance},
				Routing: map[string]map[string]int32{uid: {a.ID: 1000}}}
			result := &servingstate.OperationResult{Operation: servingstate.Operation{Kind: servingstate.OperationRoute, WorkloadUID: uid}}
			if err := c.operations.ApplyTarget(ctx, target, result); err != nil {
				t.Fatal("healthy exact executor could not accept a route", err)
			}
			routing, _ := c.runtime.SchedulerRouting()
			if routing[uid][a.ID] <= 0 || routing[uid][b.ID] != 0 {
				t.Fatalf("healthy route was globally fenced or unknown B was routed: %v", routing)
			}
			var operations []servingstate.Operation
			for _, kind := range []string{servingstate.OperationPower, servingstate.OperationPrepare, servingstate.OperationResize, servingstate.OperationRemove, servingstate.OperationStop, servingstate.OperationDrain} {
				operation := servingstate.Operation{Kind: kind, Node: b.Node, Instance: b.ID, PodUID: b.PodUID}
				operation.Prepare = &servingstate.PrepareSpec{Instance: serving.SchedulerInstance{ID: "replacement", Node: b.Node}}
				operation.Target = &serving.SchedulerCandidate{Instances: []serving.SchedulerInstance{operation.Prepare.Instance}}
				operations = append(operations, operation)
				if err := c.operations.ValidateOperationBinding(ctx, operation); err == nil {
					t.Fatalf("%s authorized unknown physical executor", kind)
				}
			}
			if err := c.operations.RequestPowerMode(ctx, b.Node, "", "fixed", time.Second); err == nil {
				t.Fatal("direct power operation bypassed unknown occupancy")
			}
			if err := c.operations.StopInstance(ctx, b.ID); err == nil {
				t.Fatal("direct stop bypassed unknown executor")
			}
			if err := c.operations.ApplyPolicy(ctx, b.ID, native.Policy{}, result); err == nil {
				t.Fatal("direct native policy mutation bypassed unknown executor")
			}
			if _, err := c.operations.MeasurementIsolation(ctx, b.Node, nil, nil, 0); err == nil {
				t.Fatal("measurement certified isolation over unknown physical occupancy")
			}
			recovering.Store(false)
			if err := c.observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			b = schedulerPhysicalState(b.ID, c.runtime.Instances())
			if b == nil || b.RecoveryUnknown || !b.Ready {
				t.Fatalf("fresh verified empty B did not recover: %+v", b)
			}
			for _, operation := range operations {
				if err := c.operations.ValidateOperationBinding(ctx, operation); err != nil {
					t.Fatalf("%s recovery fence survived fresh exact proof: %v", operation.Kind, err)
				}
			}
		})
	}
}

func TestRecoveryLatePendingPodKeepsAllocationWithoutFencingHealthyRoute(t *testing.T) {
	c, prepared := tickReadySubsetController(t, nil)
	prepared.Store(true)
	recoveryBootRuntime(t, c)
	ctx := context.Background()
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "a", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pending := pod.DeepCopy()
	pending.Name, pending.UID = "late-pending", "late-pending-pod"
	pending.Labels[servingstate.InstancePodUIDLabel] = string(pending.UID)
	pending.Annotations[servingstate.ServingRoutingAnnotation] = "{}"
	pending.Status = core.PodStatus{Phase: core.PodPending}
	if _, err := c.kube.CoreV1().Pods(pending.Namespace).Create(ctx, pending, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := c.capture(ctx, time.Now(), c.config)
	if err != nil {
		t.Fatal(err)
	}
	index := slices.IndexFunc(snapshot.Instances, func(i serving.SchedulerInstance) bool { return i.PodUID == string(pending.UID) })
	if index < 0 || snapshot.Instances[index].Ready || snapshot.Instances[index].Allocation.MemoryBytes == 0 {
		t.Fatalf("late pending source became free capacity: %+v", snapshot.Instances)
	}
	a := schedulerPhysicalState("a-pod/containerd://running", c.runtime.Instances())
	if a == nil || !a.Ready || a.RecoveryUnknown {
		t.Fatalf("pending peer fenced healthy inference destination: %+v", a)
	}
	if err := c.operations.RequestPowerMode(ctx, a.Node, "", "fixed", time.Second); err == nil {
		t.Fatal("healthy co-resident authorized node-wide power over pending unknown peer")
	}
	if err := c.operations.ClearNode(ctx, a.Node); err == nil {
		t.Fatal("node cleanup discarded pending unknown peer")
	}
}

func TestRecoveryPhysicalReleaseRequiresExactTerminatedContainer(t *testing.T) {
	c, prepared := tickReadySubsetController(t, func(index int, state *native.State) {
		if index == 1 {
			state.Requests = []native.RequestState{{Phase: "running"}}
		}
	})
	prepared.Store(true)
	recoveryBootRuntime(t, c)
	ctx := context.Background()
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "b", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	operation := servingstate.Operation{Kind: servingstate.OperationPower, Node: serving.NodeReference{Name: "b", UID: "b-node"}}
	// A last termination for another container cannot certify this executor.
	pod.Status.ContainerStatuses[0].LastTerminationState.Terminated = &core.ContainerStateTerminated{ContainerID: "containerd://other"}
	if _, err := c.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.operations.ValidateOperationBinding(ctx, operation); err == nil {
		t.Fatal("another container's termination freed the busy executor's Node")
	}
	pod.Status.Phase = core.PodFailed
	pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: "containerd://running"}}
	if _, err := c.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.observer.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.operations.ValidateOperationBinding(ctx, operation); err != nil {
		t.Fatal("exact terminated executor retained its inference recovery hold", err)
	}
}

type recoveryRemovalAPI struct {
	*resetAPI
	deleted      chan struct{}
	precondition *meta.Preconditions
}

func (a *recoveryRemovalAPI) Delete(ctx context.Context, object client.Object, opts ...client.DeleteOption) error {
	if _, ok := object.(*core.Pod); ok {
		options := &client.DeleteOptions{}
		for _, option := range opts {
			option.ApplyToDelete(options)
		}
		a.precondition = options.Preconditions
	}
	if err := a.resetAPI.Delete(ctx, object, opts...); err != nil {
		return err
	}
	if _, ok := object.(*core.Pod); ok {
		close(a.deleted)
	}
	return nil
}

func TestRecoveryMissingCatalogProfileAllowsOnlyExactPhysicalRetirement(t *testing.T) {
	for _, phase := range []string{"missing at boot", "removed after hydration"} {
		t.Run(phase, func(t *testing.T) {
			c, prepared := tickReadySubsetController(t, nil)
			prepared.Store(true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(func() { cancel(); c.wg.Wait() })
			var profile serving.Profile
			for _, candidate := range c.observer.Profiles() {
				if candidate.NodeName == "b" {
					profile = candidate
				}
			}
			if profile.ID == "" {
				t.Fatal("fixture lacks original physical profile")
			}
			pod, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "b", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			cm := &core.ConfigMap{}
			if err := c.store.client.Get(ctx, client.ObjectKeyFromObject(pod), cm); err != nil {
				t.Fatal(err)
			}
			var config native.Config
			if err := json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
				t.Fatal(err)
			}
			if config.Model != profile.Condition.Actor.Model {
				t.Fatalf("startup model %q differs from observed actor %q", config.Model, profile.Condition.Actor.Model)
			}
			config.Mode = servingstate.NativeServingMode
			config.Policy.DecisionID = "retire-catalog-rejected-source"
			config.Policy.ProfileID = profile.ID
			// The original mounted model/policy and production native actor remain
			// unchanged when the current catalog ceases admitting this source.
			c.observer.native.HTTPClient, _ = preparationNativeHTTPClient(t, ctx, config, profile)
			log, err := observation.NewObservationLog(observation.ObservationLogConfig{
				Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: managerArchive{t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = log.Close(context.Background()) })
			c.observationLog = log
			recoveryBootRuntime(t, c)
			id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
			if phase == "removed after hydration" {
				if err := c.observer.Refresh(ctx); err != nil {
					t.Fatal(err)
				}
				hydrated := schedulerPhysicalState(id, c.runtime.Instances())
				if hydrated == nil || !hydrated.Ready || hydrated.RecoveryUnknown || c.observer.sources[string(pod.UID)].instance.Execute == nil {
					t.Fatalf("original source never hydrated: %+v issues=%+v", hydrated, c.observer.Issues())
				}
			}
			c.observer.SetProfiles(nil)
			if err := c.observer.Refresh(ctx); err != nil {
				t.Fatal(err)
			}
			physical := assertRejectedSourceRecovered(t, c, id)
			readGap := func() serving.ObservationGap {
				return recoveryInfluenceGap(t, ctx, log, id, physical.ObservedAt)
			}
			gap := readGap()
			if gap.ClearAt == nil || !gap.Open {
				t.Fatalf("empty native state closed historical physical influence: %+v", gap)
			}
			kube := c.kube.(*kubefake.Clientset)
			api := &recoveryRemovalAPI{resetAPI: &resetAPI{Client: c.store.client, kube: kube}, deleted: make(chan struct{})}
			c.store.client = api
			c.config.Settings.AutonomousWorkloads = nil
			uid := c.runtime.SchedulerInputs(time.Now).Workloads[0].UID
			type outcome struct {
				result servingstate.OperationResult
				err    error
			}
			finished := make(chan outcome, 1)
			go func() {
				result, err := c.ExecuteManual(ctx, servingstate.ManualOperation{
					Kind: servingstate.OperationRemove, WorkloadUID: uid, Node: physical.Node, Instance: id, Reason: "retire original catalog-rejected executor",
				})
				finished <- outcome{result: result, err: err}
			}()
			select {
			case <-api.deleted:
			case result := <-finished:
				t.Fatalf("normal remove failed before retained deletion: %+v err=%v", result.result, result.err)
			case <-ctx.Done():
				t.Fatal("normal remove did not reach retained deletion", ctx.Err())
			}
			assertRecoveryRemovalRetained(t, ctx, c, api, pod, id)
			if current := readGap(); !current.Open || current.ClearAt == nil || !current.ClearAt.Equal(*gap.ClearAt) {
				t.Fatalf("logical stop/drain closed historical unknown: %+v", current)
			}
			// Supply kubelet's exact termination only after inspecting retention.
			current := &core.Pod{}
			if err := api.Get(ctx, client.ObjectKeyFromObject(pod), current); err != nil {
				t.Fatal(err)
			}
			status := servingstate.FirstfloorStatus(current)
			status.Ready = false
			status.State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ContainerID: status.ContainerID}}
			current.Status.Phase = core.PodSucceeded
			// Publish kubelet status to the clientset before the store read can
			// let normal removal release the finalizer and delete both copies.
			if _, err := kube.CoreV1().Pods(current.Namespace).UpdateStatus(ctx, current, meta.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if err := api.Status().Update(ctx, current); err != nil {
				t.Fatal(err)
			}
			select {
			case result := <-finished:
				assertRecoveryRemovalComplete(t, result.result, result.err, id, config.Model, profile.ID)
			case <-ctx.Done():
				t.Fatal("normal remove did not finish exact termination/404 retirement", ctx.Err())
			}
			assertRecoverySourceRetired(t, ctx, c, pod, gap, readGap)
		})
	}
}

func assertRejectedSourceRecovered(t *testing.T, c *SchedulerController, id string) *serving.InstanceState {
	t.Helper()
	physical := schedulerPhysicalState(id, c.runtime.Instances())
	if physical == nil || physical.RecoveryUnknown || !physical.NativeVerified || physical.Native == nil ||
		!physical.Native.Ready || len(physical.Native.Requests) != 0 || physical.Ready || physical.Terminated {
		t.Fatalf("fresh exact empty physical source was not recovered independently of admission: %+v issues=%+v", physical, c.observer.Issues())
	}
	for uid, routable := range physical.Routable {
		if routable {
			t.Fatalf("catalog-rejected source remained routable for %s", uid)
		}
	}
	instance := c.observer.sources[physical.PodUID].instance
	if instance.Profile != nil || instance.Execute != nil || instance.Capacity != 0 || len(instance.Routing) != 0 {
		t.Fatalf("catalog withdrawal retained execution capability: %+v", instance)
	}
	if !c.runtime.InstanceRecoveryUnknown("a-pod/containerd://running") {
		t.Fatal("B's exact empty observation cleared A's mismatching physical identity")
	}
	return physical
}

func recoveryInfluenceGap(t *testing.T, ctx context.Context, log *observation.ObservationLog, id string, at time.Time) serving.ObservationGap {
	t.Helper()
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	index, err := log.RawHour(ctx, at.UTC().Format("20060102T15"))
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range index.Gaps {
		if gap.Instance == id && gap.Reason == "UnknownExecutorInfluence" {
			return gap
		}
	}
	t.Fatal("physical recovery erased historical unknown influence")
	return serving.ObservationGap{}
}

func assertRecoveryRemovalRetained(t *testing.T, ctx context.Context, c *SchedulerController, api *recoveryRemovalAPI, pod *core.Pod, id string) {
	t.Helper()
	retained, err := c.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{})
	if err != nil || retained.UID != pod.UID || retained.DeletionTimestamp == nil ||
		!slices.Contains(retained.Finalizers, servingstate.SourceFinalizer) || servingstate.PodTerminated(retained) ||
		api.precondition == nil || api.precondition.UID == nil || *api.precondition.UID != pod.UID {
		t.Fatalf("normal remove did not retain running exact UID/termination proof: %+v preconditions=%+v err=%v", retained, api.precondition, err)
	}
	stopped := schedulerPhysicalState(id, c.runtime.Instances())
	if stopped == nil || !stopped.Stopped || stopped.Terminated {
		t.Fatalf("stop/drain bypassed retained physical ownership: %+v", stopped)
	}
	if _, err := c.kube.CoreV1().Services(pod.Namespace).Get(ctx, observerServiceFixture(pod).Name, meta.GetOptions{}); err != nil {
		t.Fatal("Service disappeared before exact termination", err)
	}
}

func assertRecoveryRemovalComplete(t *testing.T, result servingstate.OperationResult, err error, id, model, profileID string) {
	t.Helper()
	proof, observed := result.DrainNative[id]
	if err != nil || result.Status != servingstate.NativeOperationComplete || result.Error != "" ||
		!observed || !proof.Ready || len(proof.Requests) != 0 || proof.Metadata.Model != model || proof.Metadata.ProfileID != profileID {
		t.Fatalf("normal remove did not retain actual native drain proof: %+v err=%v", result, err)
	}
}

func assertRecoverySourceRetired(t *testing.T, ctx context.Context, c *SchedulerController, pod *core.Pod, gap serving.ObservationGap, readGap func() serving.ObservationGap) {
	t.Helper()
	if _, err := c.kube.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("retired Pod was not 404: %v", err)
	}
	if _, err := c.kube.CoreV1().Services(pod.Namespace).Get(ctx, observerServiceFixture(pod).Name, meta.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("retired Service survived exact termination: %v", err)
	}
	if err := c.store.client.Get(ctx, client.ObjectKeyFromObject(pod), &core.ConfigMap{}); !apierrors.IsNotFound(err) {
		t.Fatalf("original mounted ConfigMap survived retirement: %v", err)
	}
	if schedulerPhysicalState(gap.Instance, c.runtime.Instances()) != nil {
		t.Fatal("404 source retained retired physical executor")
	}
	if current := readGap(); current.Open || current.To.IsZero() || current.ClearAt == nil || !current.ClearAt.Equal(*gap.ClearAt) {
		t.Fatalf("exact termination failed to bound retained historical unknown: %+v", current)
	}
}
