package kube

import (
	"testing"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

func TestGatewayTargetsRequireExactObservedPolicyAck(t *testing.T) {
	profile := &serving.Profile{ID: "measured"}
	source := &nativeSource{uid: "pod", containerID: "container", ownerUID: "workload",
		spec:     sdk.Spec{InstanceID: "pod/container", DecisionID: "policy-1", Capacity: 4, Profile: profile, Endpoint: "http://pod-only:8080", ModelName: "chill_model"},
		instance: serving.ReadyInstance{State: serving.InstanceState{ID: "pod/container", Ready: true, Native: &native.State{Metadata: native.Metadata{DecisionID: "old-policy"}}}},
	}
	observer := &Observer{sources: map[string]*nativeSource{"pod": source}}
	if len(observer.GatewayTargets()) != 0 {
		t.Fatal("desired policy annotation became an observed ACK")
	}
	source.instance.State.Native.Metadata.DecisionID = "policy-1"
	got := observer.GatewayTargets()
	if len(got) != 1 || got[0].PodUID != "pod" || got[0].ContainerID != "container" || got[0].DecisionID != "policy-1" {
		t.Fatalf("lost physical policy identity: %+v", got)
	}
	source.containerID = restartedEvidence
	if len(observer.GatewayTargets()) != 0 {
		t.Fatal("new container inherited previous native policy ACK")
	}
	source.containerID = "container"
	source.stopped = true
	if len(observer.GatewayTargets()) != 0 {
		t.Fatal("stopped source reentered table")
	}
}
