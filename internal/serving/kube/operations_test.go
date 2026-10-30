package kube

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// The ConfigMap interceptor delivers mounted policy changes to real native
// actors. State ACKs come through the production handler and SDK, not annotations.
// Success withdraws routing after both ACKs; synthetic echo is never admitted
// by the observer as newly measured execution capacity.
func routeAcknowledgmentFixture(t *testing.T) (*Operations, serving.SchedulerCandidate, []*core.Pod) {
	t.Helper()
	ctx := context.Background()
	manager, _ := newManagerHarness(t)
	createManagerCatalog(t, manager)
	if _, err := manager.CreateWorkload(ctx, "workload", managerWorkloadSpec()); err != nil {
		t.Fatal(err)
	}
	definition := manager.runtime.SchedulerInputs(time.Now).Workloads[0]
	profile := managerProfile(definition)
	fixture, node, _, class := observerSourceFixture()
	profile.NodeName, profile.Condition.NodeUID = node.Name, string(node.UID)
	manager.runtime.SetProfiles([]serving.Profile{profile})
	kube := kubefake.NewSimpleClientset(node)
	nativeClient := &sdk.Client{}
	actors := map[string]*native.Scheduler{}
	handlers := map[string]http.Handler{}
	objects := []client.Object{&class}
	pods := []*core.Pod{}
	ready := []serving.ReadyInstance{}
	target := serving.SchedulerCandidate{ID: "withdraw-route", Routing: map[string]map[string]int32{definition.UID: {}}}
	for index, name := range []string{"a", "b"} {
		pod := fixture.DeepCopy()
		pod.Name, pod.UID = name, types.UID("pod-"+name)
		pod.ResourceVersion = "1"
		pod.Labels[instancePodUIDLabel] = string(pod.UID)
		pod.Annotations[ServingProfileAnnotation] = profile.ID
		pod.Annotations[ServingPolicyAnnotation] = "old-route"
		cfg := native.Config{Model: "chill_model", Mode: "serving",
			Inputs: []string{"input"}, Outputs: []string{"output"},
			RunnerCommand:   []string{"python3", "../../../runtime/firstfloor/runner.py", "--echo", "--preprocessor", "../../../runtime/firstfloor/testdata/echo", "--warmup-iterations", "1"},
			RunnerTimeoutMS: 3000, MaxQueueRequests: 4, MaxQueueBytes: 1 << 20, MaxRequestBytes: 1 << 20,
			MaxBatchSize: 1, BatchSizes: []int{1},
			Policy: native.Policy{DecisionID: "old-route", ProfileID: profile.ID, MaxBatchSize: 1, DeadlinePolicy: "keep",
				Batches: []native.BatchProfile{{Size: 1, ServiceSamplesMS: []float64{1}, EnergyPerItemJ: 1}}},
		}
		if err := native.ValidateConfig(&cfg); err != nil {
			t.Fatal(err)
		}
		actorCtx, cancel := context.WithCancel(ctx)
		runner, err := native.StartRunner(actorCtx, &cfg)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		actor := native.NewScheduler(actorCtx, &cfg, runner)
		t.Cleanup(func() {
			cancel()
			select {
			case <-actor.Done():
			case <-time.After(5 * time.Second):
				t.Error("native actor did not reap its runner")
			}
		})
		actors[name] = actor
		service := observerServiceFixture(pod)
		service.Spec.ClusterIP = []string{"10.96.0.10", "10.96.0.11"}[index]
		handlers[service.Spec.ClusterIP+":8080"] = native.NewHandler(actor)
		if err := kube.Tracker().Create(core.SchemeGroupVersion.WithResource("pods"), pod, pod.Namespace); err != nil {
			t.Fatal(err)
		}
		if err := kube.Tracker().Create(core.SchemeGroupVersion.WithResource("services"), service, pod.Namespace); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, pod, &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Name: name, Namespace: pod.Namespace}, Data: map[string]string{"config.json": string(raw)}})
		state, err := actor.State(ctx)
		if err != nil {
			t.Fatal(err)
		}
		id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
		spec := sdk.Spec{Endpoint: "http://" + service.Spec.ClusterIP + ":8080", ModelName: cfg.Model}
		instance := serving.SchedulerInstance{ID: id, Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile,
			WorkloadUID: definition.UID, MaxBatch: 1, Capacity: 4, MaxWait: time.Millisecond}
		target.Instances = append(target.Instances, instance)
		ready = append(ready, serving.ReadyInstance{State: serving.InstanceState{ID: id, Node: instance.Node, Profile: profile.ID, Ready: true, Native: &state},
			Profile: &profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 4,
			Execute: func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
				return nativeClient.Execute(ctx, spec, dispatch)
			},
		})
		pods = append(pods, pod)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler := handlers[r.URL.Host]
		if handler == nil {
			http.Error(w, "unknown native endpoint", http.StatusNotFound)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	nativeClient.HTTPClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}, Timeout: time.Second}
	manager.store.client = fake.NewClientBuilder().WithScheme(manager.store.client.Scheme()).WithObjects(objects...).WithInterceptorFuncs(interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if err := c.Patch(ctx, object, patch, opts...); err != nil {
				return err
			}
			switch object := object.(type) {
			case *core.ConfigMap:
				var cfg native.Config
				if err := json.Unmarshal([]byte(object.Data["config.json"]), &cfg); err != nil {
					return err
				}
				return actors[object.Name].Update(ctx, &cfg)
			case *core.Pod:
				return kube.Tracker().Update(core.SchemeGroupVersion.WithResource("pods"), object.DeepCopy(), object.Namespace)
			}
			return nil
		},
	}).Build()
	manager.observer.kube, manager.observer.native = kube, nativeClient
	if err := manager.runtime.InstallInstances(ready); err != nil {
		t.Fatal(err)
	}
	manager.runtime.EnableSchedulerRouting()
	operations, err := NewOperations(manager.store, kube, manager.runtime, manager.observer, nativeClient, OperationConfig{Namespace: fixture.Namespace, PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	return operations, target, pods
}

func TestRouteRetainsOnlyExactNativeAcknowledgmentsAcrossSequentialFailures(t *testing.T) {
	for _, test := range []struct {
		name          string
		unavailable   int
		loseEndpoint  bool
		wantStatus    string
		wantACKs      int
		wantPersisted int
	}{
		{name: "withdrawal needs no new policy", unavailable: -1, wantStatus: nativeOperationComplete},
		{name: "second unavailable after first ACK", unavailable: 1, wantStatus: nativeOperationPartial, wantACKs: 1, wantPersisted: 1},
		{name: "first unavailable before mutation", unavailable: 0, wantStatus: nativeOperationFailed},
		{name: "first endpoint lost after desired persistence", unavailable: 0, loseEndpoint: true, wantStatus: nativeOperationPartial, wantPersisted: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			operations, target, pods := routeAcknowledgmentFixture(t)
			if test.unavailable >= 0 {
				for _, instance := range target.Instances {
					target.Routing[instance.WorkloadUID][instance.ID] = 1
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			before, _ := operations.runtime.SchedulerRouting()
			if test.unavailable >= 0 {
				pod := pods[test.unavailable]
				var err error
				if test.loseEndpoint {
					err = operations.kube.CoreV1().Services(pod.Namespace).Delete(ctx, instanceServicePrefix+string(pod.UID), meta.DeleteOptions{})
				} else {
					err = operations.kube.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, meta.DeleteOptions{})
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			kind := operationRoute
			if test.unavailable < 0 {
				kind = operationUnroute
			}
			result, err := operations.Execute(ctx, Operation{ID: target.ID, Kind: kind, Target: &target})
			if (err != nil) != (test.unavailable >= 0) || result.Status != test.wantStatus {
				t.Fatalf("route status=%s error=%v, want %s", result.Status, err, test.wantStatus)
			}
			if !reflect.DeepEqual(result.Before.Routing, before) || len(result.Before.Native) != 2 {
				t.Fatalf("observed pre-route evidence lost: %+v", result.Before)
			}
			assertRouteNativeACKs(t, target, result, test.wantACKs)
			wanted := target.Routing
			if err != nil {
				wanted = before
			}
			actual, _ := operations.runtime.SchedulerRouting()
			if !maps.EqualFunc(result.Actual.Routing, wanted, func(left, right map[string]int32) bool { return maps.Equal(left, right) }) || !reflect.DeepEqual(actual, result.Actual.Routing) {
				t.Fatalf("routing evidence differs from actual distribution: result=%v actual=%v wanted=%v", result.Actual.Routing, actual, wanted)
			}
			for index, pod := range pods {
				cm := &core.ConfigMap{}
				key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
				if err := operations.store.client.Get(ctx, key, cm); err != nil {
					t.Fatal(err)
				}
				var config native.Config
				if err := json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
					t.Fatal(err)
				}
				storedPod := &core.Pod{}
				if err := operations.store.client.Get(ctx, key, storedPod); err != nil {
					t.Fatal(err)
				}
				decision := "old-route"
				if index < test.wantPersisted {
					decision = target.ID
				}
				if config.Policy.DecisionID != decision || storedPod.Annotations[ServingPolicyAnnotation] != decision {
					t.Fatalf("%s persisted desired policy/annotation differs: %+v %v", pod.Name, config.Policy, storedPod.Annotations)
				}
			}
			if err != nil && test.wantPersisted == 0 && len(result.Progress) != 0 {
				t.Fatalf("unstarted route fabricated mutation progress: %v", result.Progress)
			}
			if err != nil && !strings.Contains(err.Error(), target.Instances[test.unavailable].ID) {
				t.Fatalf("failed physical instance identity lost: %v", err)
			}
		})
	}
}

// full-E5-7-01, scheduler rows 4558–4560 (08:36:10): lattepanda is
// stopped at weight 0; jetsono is the sole destination at weight 1000.
// SCH-AC-38 keeps the physical fence for positive destinations, not retirees.
func TestRouteRequiresPolicyACKOnlyForPositiveDestinationsAC38(t *testing.T) {
	for _, test := range []struct {
		name       string
		dead       int
		wantStatus string
		wantACKs   int
	}{
		{name: "dead lattepanda zero weight", dead: 0, wantStatus: nativeOperationComplete, wantACKs: 1},
		{name: "positive jetsono ACK endpoint unavailable", dead: 1, wantStatus: nativeOperationPartial},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, prepared := tickReadySubsetController(t, nil)
			prepared.Store(true)
			operations := controller.operations
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			snapshot, err := controller.capture(ctx, time.Now(), controller.config)
			if err != nil {
				t.Fatal(err)
			}
			instances := slices.Clone(snapshot.Instances)
			slices.SortFunc(instances, func(a, b serving.SchedulerInstance) int { return strings.Compare(a.Node.Name, b.Node.Name) })
			uid := instances[0].WorkloadUID
			for i := range instances {
				instances[i].WorkloadUID = uid
			}
			target := serving.SchedulerCandidate{ID: "plan-61f142e72e70", Instances: instances,
				Routing: map[string]map[string]int32{uid: {instances[0].ID: 0, instances[1].ID: 1000}}}
			pods, err := operations.kube.CoreV1().Pods(operations.config.Namespace).List(ctx, meta.ListOptions{})
			if err != nil {
				t.Fatal(err)
			}
			slices.SortFunc(pods.Items, func(a, b core.Pod) int { return strings.Compare(a.Name, b.Name) })
			pod := pods.Items[test.dead].DeepCopy()
			if test.dead == 0 {
				target.Instances[0].Ready, target.Instances[0].Stopped, target.Instances[0].Terminated = false, true, true
				pod.Status.ContainerStatuses[0].State = core.ContainerState{Terminated: &core.ContainerStateTerminated{ExitCode: 1, ContainerID: pod.Status.ContainerStatuses[0].ContainerID}}
				if _, err := operations.kube.CoreV1().Pods(pod.Namespace).UpdateStatus(ctx, pod, meta.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			} else if err := operations.kube.CoreV1().Services(pod.Namespace).Delete(ctx, instanceServicePrefix+string(pod.UID), meta.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			result, err := operations.Execute(ctx, Operation{ID: target.ID, Kind: operationRoute, Target: &target, WorkloadUIDs: []string{uid}})
			if (err != nil) != (test.wantStatus == nativeOperationPartial) || result.Status != test.wantStatus {
				t.Fatalf("route status=%s error=%v, want %s", result.Status, err, test.wantStatus)
			}
			if len(result.Actual.Native) != test.wantACKs {
				t.Fatalf("unexpected destination ACKs: %v", result.Actual.Native)
			}
			if err == nil && result.Actual.Routing[uid][target.Instances[1].ID] != 1000 {
				t.Fatalf("live destination not published: %v", result.Actual.Routing)
			}
			cm := &core.ConfigMap{}
			if err := operations.store.client.Get(ctx, types.NamespacedName{Namespace: pods.Items[0].Namespace, Name: pods.Items[0].Name}, cm); err != nil {
				t.Fatal(err)
			}
			var config native.Config
			if err := json.Unmarshal([]byte(cm.Data["config.json"]), &config); err != nil {
				t.Fatal(err)
			}
			if config.Policy.DecisionID == target.ID {
				t.Fatalf("zero-weight executor received a new policy: %+v", config.Policy)
			}
		})
	}
}

// A route records the applied distribution on every runtime instance's Pod; the
// restarted observer reads it back.
func TestRoutePersistsAppliedDistributionOnPods(t *testing.T) {
	operations, target, pods := routeAcknowledgmentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	routed := `{"` + target.Instances[0].WorkloadUID + `":1}`
	if err := operations.persistRouting(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	if got := podRoutingAnnotations(ctx, t, operations, pods); !slices.Equal(got, []string{routed, routed}) {
		t.Fatalf("persisted routing %v, want the frozen distribution %s", got, routed)
	}
	if _, err := operations.Execute(ctx, Operation{ID: target.ID, Kind: operationUnroute, Target: &target}); err != nil {
		t.Fatal(err)
	}
	if got := podRoutingAnnotations(ctx, t, operations, pods); !slices.Equal(got, []string{`{}`, `{}`}) {
		t.Fatalf("withdrawn routing persisted as %v", got)
	}
}

func TestScopedRouteDoesNotPersistManualPodDefaults(t *testing.T) {
	operations, target, pods := routeAcknowledgmentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	uid := target.Instances[0].WorkloadUID
	if err := operations.persistRouting(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	stopped := target
	stopped.Routing = map[string]map[string]int32{uid: {}}
	if err := operations.runtime.ApplySchedulerUnrouteTarget(stopped, []string{uid}); err != nil {
		t.Fatal(err)
	}
	// An explicitly empty scope is not the legacy all-UID scope.
	result := OperationResult{Operation: Operation{ID: target.ID, Kind: operationRoute, Target: &target, WorkloadUIDs: []string{}}}
	if err := operations.ApplyTarget(ctx, target, &result); err != nil {
		t.Fatal(err)
	}
	routed := `{"` + uid + `":1}`
	if got := podRoutingAnnotations(ctx, t, operations, pods); !slices.Equal(got, []string{routed, routed}) {
		t.Fatalf("autonomous routing persisted a manual unroute over Pod defaults: %v", got)
	}
	if err := operations.persistRouting(ctx, "", []string{"another-autonomous-uid"}); err != nil {
		t.Fatal(err)
	}
	if got := podRoutingAnnotations(ctx, t, operations, pods); !slices.Equal(got, []string{routed, routed}) {
		t.Fatalf("another UID's persistence overwrote manual Pod defaults: %v", got)
	}
	if err := operations.runtime.ReleaseSchedulerRouting(uid); err != nil {
		t.Fatal(err)
	}
	if err := operations.persistRouting(ctx, "", []string{uid}); err != nil {
		t.Fatal(err)
	}
}

// Stop and drain withdraw an instance's routes without a route action; the Pod
// records it so a restart does not resume traffic to a retiring instance.
func TestStopAndDrainPersistWithdrawnRouting(t *testing.T) {
	operations, target, pods := routeAcknowledgmentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	routed := `{"` + target.Instances[0].WorkloadUID + `":1}`
	if err := operations.persistRouting(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := operations.StopInstance(ctx, target.Instances[0].ID); err != nil {
		t.Fatal(err)
	}
	if got := podRoutingAnnotations(ctx, t, operations, pods); !slices.Equal(got, []string{`{}`, routed}) {
		t.Fatalf("stop persisted %v", got)
	}
	if _, err := operations.Execute(ctx, Operation{ID: "drain", Kind: operationDrain, Node: target.Instances[1].Node, Instance: target.Instances[1].ID}); err != nil {
		t.Fatal(err)
	}
	if got := podRoutingAnnotations(ctx, t, operations, pods); !slices.Equal(got, []string{`{}`, `{}`}) {
		t.Fatalf("drain persisted %v", got)
	}
}

// A staticRouting move of an instance from one Workload to another keeps the old
// route in memory for its queued request, with or without autonomous control,
// while the Pod records only the declared Workload, even when a refresh
// reinstalls the instance from its old annotation before the persist.
func TestReassignedInstancePersistsOnlyItsDeclaredWorkload(t *testing.T) {
	for _, controlled := range []bool{true, false} {
		t.Run(map[bool]string{true: "controlled", false: "static"}[controlled], func(t *testing.T) {
			ctx := context.Background()
			m, _ := newManagerHarness(t)
			createManagerCatalog(t, m)
			first, err := m.CreateWorkload(ctx, "first", managerWorkloadSpec())
			if err != nil {
				t.Fatal(err)
			}
			second, err := m.CreateWorkload(ctx, "second", managerWorkloadSpec())
			if err != nil {
				t.Fatal(err)
			}
			profile := managerProfile(first.Applied)
			m.runtime.SetProfiles([]serving.Profile{profile})
			pod, node, _, _ := observerSourceFixture()
			pod.Annotations[ServingRoutingAnnotation] = `{"` + first.Applied.UID + `":1}`
			id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
			ref := serving.NodeReference{Name: node.Name, UID: string(node.UID)}
			started, finish := make(chan serving.Dispatch, 2), make(chan struct{}, 2)
			instance := serving.ReadyInstance{State: serving.InstanceState{ID: id, Node: ref, Profile: profile.ID, Ready: true}, Profile: &profile, Routing: map[string]int32{first.Applied.UID: 1}, Capacity: 1,
				Execute: func(_ context.Context, d serving.Dispatch) (serving.BackendResult, error) {
					started <- d
					<-finish
					return serving.BackendResult{StatusCode: 200, Finished: true}, nil
				}}
			if err = m.runtime.InstallInstances([]serving.ReadyInstance{instance}); err != nil {
				t.Fatal(err)
			}
			if controlled {
				m.runtime.EnableSchedulerRouting()
			}
			receipts, calls := []serving.Receipt{}, []*serving.Call{}
			for range 2 {
				receipt, err := m.runtime.Receive("first", time.Now())
				if err != nil {
					t.Fatal(err)
				}
				call, err := m.runtime.Accept(receipt, managerSample(t))
				if err != nil {
					t.Fatal(err)
				}
				receipts, calls = append(receipts, receipt), append(calls, call)
			}
			<-started // the second request stays queued behind the capacity of one
			moved := serving.SchedulerCandidate{ID: "move", Instances: []serving.SchedulerInstance{{ID: id, Node: ref, Profile: &profile, WorkloadUID: second.Applied.UID}},
				Routing: map[string]map[string]int32{first.Applied.UID: {}, second.Applied.UID: {id: 1}}}
			if err = m.runtime.ApplySchedulerUnrouteTarget(moved, nil); err != nil {
				t.Fatal(err)
			}
			// A refresh that listed the Pod before the patch reinstalls the old annotation.
			if err = m.runtime.InstallInstances([]serving.ReadyInstance{instance}); err != nil {
				t.Fatal(err)
			}
			created := pod.DeepCopy()
			if err = m.store.client.Create(ctx, created); err != nil {
				t.Fatal(err)
			}
			listed := pod.DeepCopy() // the store keeps no status; the listed Pod carries it
			listed.ResourceVersion = created.ResourceVersion
			operations := &Operations{store: m.store, kube: kubefake.NewSimpleClientset(listed), runtime: m.runtime, config: OperationConfig{Namespace: pod.Namespace}}
			if err = operations.persistRouting(ctx, "", nil); err != nil {
				t.Fatal(err)
			}
			stored := &core.Pod{}
			if err = m.store.client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, stored); err != nil {
				t.Fatal(err)
			}
			if got, want := stored.Annotations[ServingRoutingAnnotation], `{"`+second.Applied.UID+`":1}`; got != want {
				t.Fatalf("persisted routing %s, want only the declared Workload %s", got, want)
			}
			// The kept route still runs the queued request of the old Workload.
			finish <- struct{}{}
			select {
			case d := <-started:
				if d.Definition.UID != first.Applied.UID {
					t.Fatalf("queued request ran for %s", d.Definition.UID)
				}
			case <-time.After(time.Second):
				t.Fatal("queued request of the old Workload lost its route")
			}
			finish <- struct{}{}
			completeManagerResponses(t, m, receipts, calls)
		})
	}
}

func podRoutingAnnotations(ctx context.Context, t *testing.T, operations *Operations, pods []*core.Pod) []string {
	t.Helper()
	values := []string{}
	for _, pod := range pods {
		stored := &core.Pod{}
		if err := operations.store.client.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}, stored); err != nil {
			t.Fatal(err)
		}
		values = append(values, stored.Annotations[ServingRoutingAnnotation])
	}
	return values
}

func assertRouteNativeACKs(t *testing.T, target serving.SchedulerCandidate, result OperationResult, count int) {
	t.Helper()
	if len(result.Actual.Native) != count {
		t.Fatalf("lost actual ACK or fabricated failed instance: %+v", result.Actual.Native)
	}
	for id, state := range result.Actual.Native {
		policy := state.Metadata.Policy
		if !state.Ready || state.At.IsZero() ||
			state.Metadata.DecisionID != target.ID || policy.DecisionID != target.ID ||
			policy.ProfileID != target.Instances[0].Profile.ID || policy.DeadlinePolicy != "keep" ||
			policy.MaxBatchSize != 1 || policy.MaxBatchWaitMS != float64(target.Instances[0].MaxWait)/float64(time.Millisecond) {
			t.Fatalf("%s lost full observed native ACK: %+v", id, state)
		}
		if !slices.Contains(result.Progress, id+": native policy acknowledged") {
			t.Fatalf("missing acknowledged instance progress: %v", result.Progress)
		}
	}
}

func TestRouteRejectsImmutableProfileAndDeadlinePolicyBeforePersistence(t *testing.T) {
	operations, target, pods := routeAcknowledgmentFixture(t)
	for _, instance := range target.Instances {
		target.Routing[instance.WorkloadUID][instance.ID] = 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key := types.NamespacedName{Namespace: pods[0].Namespace, Name: pods[0].Name}
	original := &core.ConfigMap{}
	if err := operations.store.client.Get(ctx, key, original); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{profileInitContainerName, "deadline policy"} {
		t.Run(field, func(t *testing.T) {
			profile := *target.Instances[0].Profile
			firstfloor := *profile.Condition.Firstfloor
			profile.Condition.Firstfloor = &firstfloor
			if field == profileInitContainerName {
				profile.ID = "different-profile"
			} else {
				firstfloor.DeadlinePolicy = "drop"
			}
			wanted := target
			wanted.Instances = slices.Clone(target.Instances)
			wanted.Instances[0].Profile = &profile
			wantError := "immutable " + field
			if field == profileInitContainerName {
				wantError = "lacks matching actual readiness"
			}
			result, err := operations.Execute(ctx, Operation{ID: wanted.ID, Kind: operationRoute, Target: &wanted})
			if err == nil || !strings.Contains(err.Error(), wantError) || result.Status != nativeOperationFailed || len(result.Actual.Native) != 0 || len(result.Progress) != 0 {
				t.Fatalf("immutable %s mutation escaped rejection: result=%+v err=%v", field, result, err)
			}
			current := &core.ConfigMap{}
			if err := operations.store.client.Get(ctx, key, current); err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(current.Data, original.Data) {
				t.Fatalf("immutable %s changed desired config", field)
			}
		})
	}
}

func TestCaptureRecordsNodePodsWithWorkloadRole(t *testing.T) {
	manager, _ := newManagerHarness(t)
	node := &core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}}
	pod := func(name string, labels map[string]string) *core.Pod {
		return &core.Pod{ObjectMeta: meta.ObjectMeta{Namespace: "apps", Name: name, UID: types.UID(name), Labels: labels},
			Spec:   core.PodSpec{NodeName: node.Name, Containers: []core.Container{{Name: "main", Image: name + "-image"}}},
			Status: core.PodStatus{Phase: core.PodRunning}}
	}
	external := pod("external", map[string]string{"app": "external"})
	system := pod("system", map[string]string{profilingrun.MeasurementWorkloadLabelKey: profilingrun.SystemRole})
	operations := &Operations{store: manager.store, kube: kubefake.NewSimpleClientset(node, external, system), runtime: manager.runtime}
	state, err := operations.Capture(context.Background(), serving.NodeReference{Name: node.Name, UID: string(node.UID)})
	if err != nil {
		t.Fatal(err)
	}
	want := []workloadobserver.Pod{
		{Namespace: "apps", Name: "external", UID: "external", Phase: "Running", Images: []string{"external-image"}},
		{Namespace: "apps", Name: "system", UID: "system", Phase: "Running", Images: []string{"system-image"},
			Labels: map[string]string{profilingrun.MeasurementWorkloadLabelKey: profilingrun.SystemRole}},
	}
	if !reflect.DeepEqual(state.Pods, want) {
		t.Fatalf("Pods = %+v", state.Pods)
	}
}

func TestRouteAndBridgeRejectEmptyBeforePolicyOrSeedMutation(t *testing.T) {
	for _, kind := range []string{operationRoute, operationResize} {
		for _, allZero := range []bool{false, true} {
			operations, target, pods := routeAcknowledgmentFixture(t)
			uid := target.Instances[0].WorkloadUID
			weights := map[string]int32{}
			if allZero {
				for _, instance := range target.Instances {
					weights[instance.ID] = 0
				}
			}
			target.Routing = map[string]map[string]int32{uid: weights}
			before, _ := operations.runtime.SchedulerRouting()
			seeds := podRoutingAnnotations(context.Background(), t, operations, pods)
			result := OperationResult{Operation: Operation{Kind: kind, Target: &target, WorkloadUIDs: []string{uid}}}
			if err := operations.ApplyTarget(context.Background(), target, &result); err == nil {
				t.Fatal("empty/all-zero route or bridge/resize passed preflight")
			}
			after, _ := operations.runtime.SchedulerRouting()
			if !reflect.DeepEqual(before, after) || !slices.Equal(seeds, podRoutingAnnotations(context.Background(), t, operations, pods)) || len(result.Steps) != 0 {
				t.Fatal("rejected empty distribution mutated runtime, seeds, or policy")
			}
		}
	}
}

type routingPersistenceFailureClient struct {
	client.Client
}

func (c routingPersistenceFailureClient) Patch(ctx context.Context, object client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if pod, ok := object.(*core.Pod); ok {
		before := &core.Pod{}
		if err := c.Get(ctx, client.ObjectKeyFromObject(pod), before); err != nil {
			return err
		}
		if before.Annotations[ServingRoutingAnnotation] != pod.Annotations[ServingRoutingAnnotation] {
			return errors.New("injected routing persistence failure")
		}
	}
	return c.Client.Patch(ctx, object, patch, opts...)
}

func TestUnroutePersistenceFailureIsNotSuccessfulACK(t *testing.T) {
	operations, target, pods := routeAcknowledgmentFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := operations.persistRouting(ctx, "", nil); err != nil {
		t.Fatal(err)
	}
	seeds := podRoutingAnnotations(ctx, t, operations, pods)
	operations.store.client = routingPersistenceFailureClient{operations.store.client}
	result, err := operations.Execute(ctx, Operation{ID: target.ID, Kind: operationUnroute, Target: &target})
	if err == nil || result.Status == nativeOperationComplete || !strings.Contains(err.Error(), "injected routing persistence failure") {
		t.Fatalf("persistence failure promoted to ACK: status=%s err=%v", result.Status, err)
	}
	if !slices.Equal(seeds, podRoutingAnnotations(ctx, t, operations, pods)) {
		t.Fatal("failed persistence changed Pod seeds")
	}
}
