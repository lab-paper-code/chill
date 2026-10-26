package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

//nolint:gocyclo // One end-to-end fixture proves Tick, prepare, route and native completion for each blocked-owner variant.
func TestTickIndependentExpansionExecutesNativeBeforePowerCompletesAC39(t *testing.T) {
	for _, variant := range []string{"slow-power", "unconfirmed-source", "pending-kappa", "preparing-source"} {
		t.Run(variant, func(t *testing.T) {
			var unconfirmed atomic.Bool
			c, ready := tickReadySubsetController(t, func(index int, state *native.State) {
				if index == 0 && unconfirmed.Load() {
					state.Ready = false
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			c.ctx = ctx
			t.Cleanup(func() { cancel(); c.wg.Wait() })
			w1 := c.runtime.SchedulerInputs(time.Now).Workloads[0]
			object, err := c.store.GetWorkload(ctx, w1.Name)
			if err != nil {
				t.Fatal(err)
			}
			secondObject := object.DeepCopy()
			secondObject.Name = "independent-workload"
			secondObject.UID = "independent-uid"
			secondObject.ResourceVersion = ""
			if err = c.store.CreateWorkload(ctx, secondObject); err != nil {
				t.Fatal(err)
			}
			w2 := *w1
			w2.Name, w2.UID = secondObject.Name, string(secondObject.UID)
			if err = c.runtime.Publish(&w2); err != nil {
				t.Fatal(err)
			}
			// b is available physical inventory, not preparing/current capacity.
			old, err := c.kube.CoreV1().Pods("chill-system").Get(ctx, "b", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if err = c.kube.CoreV1().Pods(old.Namespace).Delete(ctx, old.Name, meta.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			if err = c.store.client.Delete(ctx, old); err != nil {
				t.Fatal(err)
			}
			slow, err := c.kube.CoreV1().Nodes().Get(ctx, slowNodeName, meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			nodes, err := c.kube.CoreV1().Nodes().List(ctx, meta.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, node := range nodes.Items {
				if err = c.store.client.Create(ctx, node.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			created := make(chan *core.Pod, 1)
			c.store.client = interceptor.NewClient(c.store.client.(client.WithWatch), interceptor.Funcs{
				Create: func(ctx context.Context, cl client.WithWatch, o client.Object, opts ...client.CreateOption) error {
					pod, ok := o.(*core.Pod)
					if !ok {
						return cl.Create(ctx, o, opts...)
					}
					pod.UID = types.UID("new-independent-pod")
					pod.Labels[instancePodUIDLabel] = string(pod.UID)
					pod.Status = old.Status
					if err := cl.Create(ctx, o, opts...); err != nil {
						return err
					}
					if err := c.kube.(*kubefake.Clientset).Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod.DeepCopy(), pod.Namespace); err != nil {
						return err
					}
					follow, stop := context.WithCancel(c.observer.ctx)
					c.observer.mu.Lock()
					c.observer.sources[string(pod.UID)] = &nativeSource{pod: types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, uid: pod.UID, containerID: firstfloorStatus(pod).ContainerID, follow: follow, cancel: stop}
					c.observer.mu.Unlock()
					created <- pod.DeepCopy()
					return nil
				},
				Patch: func(ctx context.Context, cl client.WithWatch, o client.Object, p client.Patch, opts ...client.PatchOption) error {
					if err := cl.Patch(ctx, o, p, opts...); err != nil {
						return err
					}
					if node, ok := o.(*core.Node); ok {
						node.Labels[chillmeta.PowerModeState] = chillmeta.PowerModePending
						if err := cl.Update(ctx, node); err != nil {
							return err
						}
						return c.kube.(*kubefake.Clientset).Tracker().Update(core.SchemeGroupVersion.WithResource("nodes"), node.DeepCopy(), "")
					}
					return nil
				},
			})
			c.kube.(*kubefake.Clientset).PrependReactor("create", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
				action.(ktesting.CreateAction).GetObject().(*core.Service).Spec.ClusterIP = "10.96.0.12"
				return false, nil, nil
			})
			profiles := c.observer.Profiles()
			settings := &c.config.Settings
			settings.ObservationWindow = 100 * time.Millisecond
			settings.BootstrapCapacityWait = 10 * time.Minute
			settings.ScaleDownStabilization = 300 * time.Second
			settings.Controls = serving.SchedulerControls{Placement: true, Resources: true, Membership: true, Replicas: true, Routing: true, Batching: true}
			settings.AutonomousWorkloads = []string{w1.UID, w2.UID}
			settings.MinimumDemand = map[string]float64{w2.UID: 0}
			c.config.Preparations = map[string]PrepareSpec{}
			for _, profile := range profiles {
				profile.IdleBaselineWatts = 10
				spec := measuredCPUPreparationFixture()
				spec.Instance = serving.SchedulerInstance{ID: "option-" + profile.ID, Node: serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}, Profile: &profile, Allocation: serving.SchedulerAllocation{CPUs: []int{0}, CPUCount: 1, Threads: 1, MemoryBytes: 1024}, MaxBatch: 1, Capacity: 4}
				spec.Config.Policy.ProfileID = profile.ID
				c.config.Preparations[profile.ID] = spec
				c.config.DispatchOccupancy = append(c.config.DispatchOccupancy, serving.DispatchOccupancy{Node: spec.Instance.Node, PowerMode: profile.Condition.PowerMode, RuntimeImage: profile.Condition.RuntimeImage, Model: profile.Condition.LogicalModel, Source: "AC-39 measured fixture"})
				ref := evidencestore.ObjectIdentity{Store: "raw", Key: "baseline-" + profile.ID, Size: 1}
				c.config.Baselines = append(c.config.Baselines, serving.NodeBaseline{ID: ref.Key, Node: spec.Instance.Node, PowerMode: "fixed", Watts: 10, Meter: "wall-" + profile.NodeName, Scope: "node-exclusive-wall", Reference: ref, MeasuredAt: time.Now()})
				c.config.Meters = append(c.config.Meters, MeterDeclaration{ID: "wall-" + profile.NodeName, Node: spec.Instance.Node, Scope: "node-exclusive-wall", Source: profile.MetricSource})
				c.config.Costs = append(c.config.Costs, serving.OperationCost{ID: "prepare-" + profile.ID, Node: spec.Instance.Node, Kind: operationPrepare, From: "unloaded", To: serving.SchedulerInstanceState(spec.Instance), PowerMode: "fixed", Trials: []serving.OperationTrial{{Duration: time.Millisecond, Joules: 0.01}}, Meter: "wall-" + profile.NodeName, Scope: "node-exclusive-wall", EnergyScope: "whole-wall", Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "prepare-" + profile.ID, Size: 1}})
			}
			// Build real capacity rows from the fixture's measured durations/occupancy.
			settings.CapacityWindow = 10 * time.Millisecond
			c.capacity = serving.NewCapacityTable(1, nil, nil)
			first, err := c.capture(ctx, time.Now(), c.config)
			if err != nil {
				t.Fatal(err)
			}
			c.state.Snapshot = first
			warm := *settings
			if variant == "pending-kappa" {
				warm.AutonomousWorkloads = []string{w2.UID}
			}
			c.capacity.Ensure(ctx, warm, first)
			c.capacity.Wait()
			if !slices.ContainsFunc(c.capacity.Rows(), func(row serving.CapacityRow) bool { return row.Input.WorkloadUID == w2.UID && row.Kappa != nil }) {
				t.Fatalf("W2 capacity fixture rejected: %+v", c.capacity.Rows())
			}
			if err = c.runtime.ApplySchedulerWorkloadsTarget(serving.SchedulerCandidate{ID: "retain-w1", Instances: first.Instances, Routing: first.Routing}, []string{w1.UID}); err != nil {
				t.Fatal(err)
			}
			destination := c.config.Preparations["b-profile"].Instance
			waiting := destination
			waiting.ID = "w1-future"
			waiting.Node = serving.NodeReference{Name: slow.Name, UID: string(slow.UID)}
			waiting.WorkloadUID = w1.UID
			frozen := serving.SchedulerCandidate{ID: "slow-w1", Instances: []serving.SchedulerInstance{waiting}, Routing: map[string]map[string]int32{w1.UID: {waiting.ID: 1000}}, Actions: []serving.Action{
				{ID: "w1-power", Kind: operationPower, Node: waiting.Node, To: "fixed"},
				{ID: "w1-prepare", Kind: operationPrepare, Node: waiting.Node, Instance: waiting.ID, DependsOn: []string{"w1-power"}},
				{ID: "w1-route", Kind: operationRoute, DependsOn: []string{"w1-prepare"}},
			}}
			c.powerTargets = map[string]time.Duration{waiting.Node.UID + "/fixed": time.Hour}
			c.launch(frozen, c.config)
			waitReservationTest(t, ctx, func() bool {
				node, _ := c.kube.CoreV1().Nodes().Get(ctx, slow.Name, meta.GetOptions{})
				return node != nil && node.Labels[chillmeta.PowerModeState] == chillmeta.PowerModePending
			})
			if variant == "unconfirmed-source" {
				unconfirmed.Store(true)
			}
			if variant == "preparing-source" {
				pending := old.DeepCopy()
				pending.Name, pending.UID, pending.ResourceVersion = "preparing-w1", "preparing-w1-uid", ""
				pending.Spec.NodeName = slow.Name
				pending.Status = core.PodStatus{Phase: core.PodPending}
				pending.Labels[instancePodUIDLabel] = string(pending.UID)
				pending.Annotations[ServingSlotAnnotation] = waiting.ID
				pending.OwnerReferences = []meta.OwnerReference{{APIVersion: object.APIVersion, Kind: "Workload", Name: object.Name, UID: object.UID}}
				if _, err = c.kube.CoreV1().Pods(pending.Namespace).Create(ctx, pending, meta.CreateOptions{}); err != nil {
					t.Fatal(err)
				}
			}
			// A new demand is evaluated by Tick while the actual power wait remains live.
			settings.MinimumDemand[w2.UID] = 2
			time.Sleep(2 * time.Millisecond)
			decision, err := c.Tick(ctx, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if variant == "preparing-source" {
				captured := c.State().Snapshot
				if !slices.ContainsFunc(captured.Instances, func(i serving.SchedulerInstance) bool {
					return i.PodUID == "preparing-w1-uid" && !i.Ready && i.Capacity == 0 && i.MaxBatch == 0
				}) {
					t.Fatal("preparing occupancy was lost or assigned invented native evidence")
				}
			}
			var pod *core.Pod
			select {
			case pod = <-created:
			case <-ctx.Done():
				t.Fatalf("independent prepare did not execute: %v reasons=%v target=%+v results=%+v capacity=%+v", ctx.Err(), decision.Reasons, decision.Target, c.State().Results, decision.Capacity)
			}
			ready.Store(true)
			waitReservationTest(t, ctx, func() bool {
				c.mu.Lock()
				defer c.mu.Unlock()
				for _, run := range c.runs {
					if run.action.Kind == operationPrepare && run.result.Operation.Prepare != nil && run.result.Operation.Prepare.Owner.UID == w2.UID {
						if run.action.Status == nativeOperationFailed || run.action.Status == nativeOperationPartial {
							t.Fatalf("prepare failed: %s physical=%+v", run.result.Error, c.runtime.Instances())
						}
						return run.action.Status == nativeOperationComplete
					}
				}
				return false
			})
			if _, err = c.Tick(ctx, time.Now()); err != nil {
				t.Fatal(err)
			}
			physical := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
			waitReservationTest(t, ctx, func() bool { route, _ := c.runtime.SchedulerRouting(); return route[w2.UID][physical] > 0 })
			executed := make(chan native.Request, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request native.Request
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if r.URL.Host != "10.96.0.12:8080" {
					t.Errorf("request reached wrong physical service: %s", r.URL.Host)
				}
				progress := native.Progress{RequestID: request.ID, WorkloadUID: request.Context.WorkloadUID, Definition: request.Context.Definition, Phase: "finished", At: time.Now(), BatchID: 1, BatchSize: 1, ProfileID: "b-profile", ExecutionComplete: true, Units: 1}
				_ = json.NewEncoder(w).Encode(native.Response{ModelName: "chill_model", ID: request.ID, ExecutionComplete: true, Progress: progress, Outputs: []native.Field{{Name: "output", Data: [][]byte{[]byte("result")}}}})
				executed <- request
			}))
			defer server.Close()
			proxy, _ := url.Parse(server.URL)
			transport := c.observer.native.HTTPClient.Transport.(*http.Transport)
			previous := transport.Proxy
			c.observer.native.HTTPClient.Transport = &http.Transport{Proxy: func(r *http.Request) (*url.URL, error) {
				if r.Method == http.MethodPost {
					return proxy, nil
				}
				return previous(r)
			}}
			receipt, err := c.runtime.Receive(w2.Name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			call, err := c.runtime.Accept(receipt, managerSample(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = call.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case request := <-executed:
				if request.Context.WorkloadUID != w2.UID {
					t.Fatal("native execution lost immutable Workload identity")
				}
			case <-ctx.Done():
				t.Fatal("no native execution")
			}
			if err = c.runtime.Response(receipt, "ok", ""); err != nil {
				t.Fatal(err)
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if c.runs["w1-power"].action.Status != nativeOperationRunning || c.runs[transitionRunKind+"/slow-w1"].result.Operation.Target.ID != frozen.ID {
				t.Fatal("independent native execution completed or replaced W1")
			}
			if c.runs["w1-prepare"] != nil {
				t.Fatal("W1 bypassed its own power dependency")
			}
		})
	}
}

func waitReservationTest(t *testing.T, ctx context.Context, ready func() bool) {
	t.Helper()
	for !ready() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
}
