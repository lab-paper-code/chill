package serving

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// SCH-AC-32: release removes the override, including for future physical Pods.
func TestReleaseSchedulerRoutingFollowsNewReadySeed(t *testing.T) {
	r, original, _ := routingRuntime(t, map[string]int32{"w1": 1, "w2": 3})
	first := r.instances[original.ID].ReadyInstance
	other := original
	other.WorkloadUID = "w2"
	blocked := SchedulerCandidate{Instances: []SchedulerInstance{other}, Routing: map[string]map[string]int32{"w1": {}, "w2": {other.ID: 7}}}
	if err := r.ApplySchedulerTarget(blocked); err != nil {
		t.Fatal(err)
	}
	if r.Instances()[0].Routable["w1"] || !r.Instances()[0].Routable["w2"] {
		t.Fatal("routability did not follow the effective declaration")
	}
	if err := r.ReleaseSchedulerRouting("w1"); err != nil {
		t.Fatal(err)
	}
	if _, present := r.routeTargets["w1"]; present {
		t.Fatal("release retained an empty override")
	}
	if !r.Instances()[0].Routable["w1"] {
		t.Fatal("release did not restore the current Pod seed")
	}
	executed := make(chan string, 1)
	prepared := first
	prepared.State.ID = "new-measurement-physical"
	prepared.OwnerWorkloadUID = "w1"
	prepared.Routing = map[string]int32{"w1": 5}
	prepared.Execute = func(_ context.Context, dispatch Dispatch) (BackendResult, error) {
		executed <- prepared.State.ID
		return BackendResult{StatusCode: 200, Finished: true}, nil
	}
	first.Routing = map[string]int32{"w2": 3}
	if err := r.InstallInstances([]ReadyInstance{first, prepared}); err != nil {
		t.Fatal(err)
	}
	// A later autonomous route for another UID must not recapture the released
	// UID's empty snapshot entry and block future/current Pod defaults.
	delete(blocked.Routing, "w1")
	if err := r.ApplySchedulerWorkloadsTarget(blocked, []string{"w2"}); err != nil {
		t.Fatal(err)
	}
	weights, _ := r.SchedulerRouting()
	if !reflect.DeepEqual(weights["w2"], map[string]int32{original.ID: 7}) || !reflect.DeepEqual(weights["w1"], map[string]int32{prepared.State.ID: 5}) {
		t.Fatalf("release changed another UID or lost the future seed: %v", weights)
	}
	for _, state := range r.Instances() {
		if state.Routable["w1"] != (state.ID == prepared.State.ID) {
			t.Fatalf("incorrect new-request routability: %+v", state)
		}
	}
	receipt, err := r.Receive(r.workloads["w1"].definition.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := r.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err = call.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-executed:
		if id != prepared.State.ID {
			t.Fatalf("dispatched to %q", id)
		}
	default:
		t.Fatal("new seeded instance did not receive a real request")
	}
	if err = r.Response(receipt, "ok", ""); err != nil {
		t.Fatal(err)
	}
}

func TestManualRoutingPreservesReleasedUID(t *testing.T) {
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 2, "w2": 3})
	r.EnableSchedulerRouting()
	if err := r.ReleaseSchedulerRouting("w1"); err != nil {
		t.Fatal(err)
	}
	instance.WorkloadUID = "w2"
	target := SchedulerCandidate{Instances: []SchedulerInstance{instance}, Routing: map[string]map[string]int32{"w2": {instance.ID: 7}}}
	if err := r.ApplySchedulerWorkloadsTarget(target, []string{"w2"}); err != nil {
		t.Fatal(err)
	}
	if _, controlled := r.routeTargets["w1"]; controlled {
		t.Fatal("manual routing recaptured a released UID")
	}
	if got, _ := r.SchedulerRouting(); got["w1"][instance.ID] != 2 || got["w2"][instance.ID] != 7 {
		t.Fatalf("manual routing changed an unrelated distribution: %v", got)
	}
	if err := r.ApplySchedulerWorkloadsTarget(target, []string{""}); err == nil {
		t.Fatal("empty manual routing UID accepted")
	}
}
