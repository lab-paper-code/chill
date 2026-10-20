package serving

import (
	"context"
	"testing"
	"time"
)

func TestStaticExclusiveBaselineThenSwappedTargetDrainsPhysicalBinding(t *testing.T) {
	_, snapshot := schedulerFixture()
	first := snapshot.Workloads[0]
	second := *first
	second.Name, second.UID = "second", "w2"
	snapshot.Workloads = append(snapshot.Workloads, &second)
	other := snapshot.Instances[0]
	other.ID = "i2"
	snapshot.Instances = append(snapshot.Instances, other)
	observationLog, err := NewObservationLog(ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Writer: &observationArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = observationLog.Close(context.Background()) }()
	runtime, err := NewRuntime(context.Background(), RuntimeConfig{QueueLimit: 4, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.cancel()
	runtime.SetProfiles([]Profile{*snapshot.Instances[0].Profile})
	for _, definition := range snapshot.Workloads {
		if err = runtime.Publish(definition); err != nil {
			t.Fatal(err)
		}
	}
	started, finish := make(chan struct{}), make(chan struct{})
	instances := make([]ReadyInstance, 0, 2)
	for _, instance := range snapshot.Instances {
		physical := instance.ID
		instances = append(instances, ReadyInstance{
			State:   InstanceState{ID: physical, Node: instance.Node, Profile: instance.Profile.ID, Ready: true},
			Profile: instance.Profile, Weight: 1, Capacity: 1,
			Execute: func(context.Context, Dispatch) (BackendResult, error) {
				if physical == "i1" {
					close(started)
					<-finish
				}
				return BackendResult{Body: []byte(physical), Finished: true}, nil
			},
		})
	}
	if err = runtime.InstallInstances(instances); err != nil {
		t.Fatal(err)
	}
	baseline := map[string]map[string]int{first.UID: {"i1": 1}, second.UID: {"i2": 1}}
	target, err := StaticRoutingTarget(snapshot, baseline, nil, "manual-static-routing:42")
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.ApplySchedulerTarget(target); err != nil {
		t.Fatal(err)
	}
	if runtime.instances["i1"].bindings[second.UID] || runtime.instances["i2"].bindings[first.UID] {
		t.Fatal("exclusive baseline retained zero-route bindings")
	}
	receipt, err := runtime.Receive(first.Name, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	call, err := runtime.Accept(receipt, &InferenceInput{Inputs: []RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// The swap gives each Workload the other instance; the accepted call stays on i1.
	swapped, err := StaticRoutingTarget(snapshot, map[string]map[string]int{first.UID: {"i2": 1}, second.UID: {"i1": 1}}, nil, "manual-static-routing:43")
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.ApplySchedulerTarget(swapped); err != nil {
		t.Fatal(err)
	}
	weights, _ := runtime.SchedulerRouting()
	if weights[first.UID]["i2"] != 1 || weights[second.UID]["i1"] != 1 || len(weights[first.UID]) != 1 || len(weights[second.UID]) != 1 {
		t.Fatalf("physical swapped readback: %v", weights)
	}
	if !runtime.instances["i1"].bindings[first.UID] || !runtime.instances["i1"].bindings[second.UID] {
		t.Fatal("route change lost accepted identity or the new Workload")
	}
	close(finish)
	result, err := call.Wait(ctx)
	if err != nil || string(result.Body) != "i1" {
		t.Fatalf("accepted execution moved: %s %v", result.Body, err)
	}
	runtime.mu.Lock()
	retained := runtime.instances["i1"].bindings[first.UID]
	runtime.mu.Unlock()
	if retained {
		t.Fatal("drained zero-route binding retained")
	}
	for _, invalid := range []map[string]map[string]int{
		{first.UID: {"i1": 1}, "stale": {"i2": 1}},
		{first.UID: {"logical-alias": 1}, second.UID: {"i2": 1}},
		{first.UID: {"i2": 1}, second.UID: {"i2": 1}},
		{first.UID: {"i1": -1}, second.UID: {"i2": 1}},
	} {
		if _, err = StaticRoutingTarget(snapshot, invalid, nil, "manual-static-routing:44"); err == nil {
			t.Fatalf("invalid declaration accepted: %v", invalid)
		}
	}
}

// A partial map keeps unlisted Workloads' routing; autonomous entries and a
// final distribution that routes two Workloads to one instance are rejected.
func TestStaticRoutingPartialKeepsUnlistedAndRejectsAutonomousAndShared(t *testing.T) {
	_, snapshot := schedulerFixture()
	second := *snapshot.Workloads[0]
	second.Name, second.UID = "partial", "w2"
	snapshot.Workloads = append(snapshot.Workloads, &second)
	for _, id := range []string{"i2", "i3"} {
		other := snapshot.Instances[0]
		other.ID, other.WorkloadUID = id, ""
		snapshot.Instances = append(snapshot.Instances, other)
	}
	snapshot.Instances[1].WorkloadUID = "w2"
	snapshot.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"i2": 3}}
	target, err := StaticRoutingTarget(snapshot, map[string]map[string]int{"w1": {"i3": 1}}, nil, "partial")
	if err != nil {
		t.Fatal(err)
	}
	if target.Routing["w1"]["i3"] != 1 || len(target.Routing["w1"]) != 1 || target.Routing["w2"]["i2"] != 3 || len(target.Routing["w2"]) != 1 {
		t.Fatalf("partial staticRouting changed an unlisted Workload: %v", target.Routing)
	}
	for _, instance := range target.Instances {
		if want := map[string]string{"i2": "w2", "i3": "w1"}[instance.ID]; instance.WorkloadUID != want {
			t.Fatalf("instance %s serves %q, want %q", instance.ID, instance.WorkloadUID, want)
		}
	}
	if _, err = StaticRoutingTarget(snapshot, map[string]map[string]int{"w1": {"i3": 1}}, []string{"w1"}, "autonomous"); err == nil {
		t.Fatal("staticRouting entry for an autonomous Workload accepted")
	}
	// w2 keeps i2, so routing w1 to i2 would put one instance in two Workloads.
	if _, err = StaticRoutingTarget(snapshot, map[string]map[string]int{"w1": {"i2": 1}}, nil, "shared"); err == nil {
		t.Fatal("staticRouting routed two Workloads to one instance")
	}
}

// An empty or all-zero entry removes a disabled Workload's routes and frees its instances.
func TestStaticRoutingEmptyEntryUnroutesWorkload(t *testing.T) {
	_, snapshot := schedulerFixture()
	second := *snapshot.Workloads[0]
	second.Name, second.UID = "unrouted", "w2"
	snapshot.Workloads = append(snapshot.Workloads, &second)
	snapshot.Instances[0].WorkloadUID = "w1"
	snapshot.Routing = map[string]map[string]int32{"w1": {"i1": 1}, "w2": {"i1": 1}}
	for _, entry := range []map[string]int{{}, {"i1": 0}} {
		target, err := StaticRoutingTarget(snapshot, map[string]map[string]int{"w2": entry}, nil, "unroute")
		if err != nil {
			t.Fatal(err)
		}
		if len(target.Routing["w2"]) != 0 || target.Routing["w1"]["i1"] != 1 || len(target.Instances) != 1 || target.Instances[0].WorkloadUID != "w1" {
			t.Fatalf("entry %v left w2 routed: %v %+v", entry, target.Routing, target.Instances)
		}
	}
}
