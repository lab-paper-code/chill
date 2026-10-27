package gateway

import (
	"context"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestClosedWorkloadDrainAndDisconnectedScope(t *testing.T) {
	control, _ := NewControl(2)
	defer control.Close()
	plan := testPlan("closed")
	plan.Participants = 2
	plan.Admission = map[string]bool{"workload": false}
	plan.Targets = []serving.GatewayTarget{{InstanceID: "pod/container", PodUID: "pod", ContainerID: "container", Node: serving.NodeReference{UID: "node-a"}, OwnerWorkloadUID: "workload", DecisionID: "ack", Capacity: 8}}
	if err := control.Publish(plan); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	channel := newChannel(ctx, nil)
	participant, err := control.join("router-one", channel)
	if err != nil {
		t.Fatal(err)
	}
	report := serving.GatewayReport{RouterID: "router-one", PlanVersion: plan.PlanVersion, ObservedAt: time.Now(), AppliedAt: time.Now()}
	if err = control.accept(participant, channel, message{Type: "applied", Generation: participant.Generation, Report: &report}); err != nil {
		t.Fatal(err)
	}
	if !control.Drained("workload", "", "") {
		t.Fatal("closed workload with no accepted work could not drain")
	}
	if control.CanRetire("pod/container") {
		t.Fatal("workload admission closure retired a still-routable physical source")
	}
	control.members["old"] = &member{RouterState: RouterState{RouterID: "old", Exposed: map[string]bool{"other/container": true}, Sources: map[string]Source{"other/container": {NodeUID: "node-b", Workloads: map[string]bool{"other-workload": true}}}}}
	next := testPlan("retired")
	next.Participants = 2
	if err = control.Publish(next); err != nil {
		t.Fatal(err)
	}
	report.PlanVersion = next.PlanVersion
	if err = control.accept(participant, channel, message{Type: "applied", Generation: participant.Generation, Report: &report}); err != nil {
		t.Fatal(err)
	}
	if !control.Drained("", "", "node-a") {
		t.Fatal("unrelated disconnected source blocked safe node scope")
	}
	if control.Drained("", "", "node-b") {
		t.Fatal("unknown old generation stopped blocking its own node")
	}
	if !control.PodDrained("pod") {
		t.Fatal("unrelated disconnected generation blocked exact Pod drain")
	}
	if control.PodDrained("other") {
		t.Fatal("Pod drain forgot old generation's exposure")
	}
	participant.LastSeen = time.Now().Add(-FreshnessTimeout - time.Second)
	if control.Drained("", "", "node-a") {
		t.Fatal("stale participant's applied report remained authoritative")
	}
}
