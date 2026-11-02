package kube

import (
	"context"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const measuredTwoCoreCPUMax = "max 100000"

func measuredCPUPreparationFixture() PrepareSpec {
	pod, node, profile, _ := observerSourceFixture()
	profile.RuntimeMetadata.CPUAffinity = []int{0, 1, 2, 3}
	profile.RuntimeMetadata.IntraOpThreads, profile.RuntimeMetadata.InterOpThreads = 0, 0
	profile.RuntimeMetadata.CgroupCPUMax = measuredTwoCoreCPUMax
	return PrepareSpec{
		Name: pod.Name, MemoryBytes: 1024,
		Instance: serving.SchedulerInstance{ID: "measured-slot", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile, Weight: 1, Allocation: serving.SchedulerAllocation{MemoryBytes: 1024}},
		Store:    storekube.Binding{Store: "evidence", Endpoint: "http://evidence", Bucket: "bucket", Region: "region", CredentialsSecretName: "credentials"},
		Bundle:   evidencestore.ObjectIdentity{Store: "evidence", Key: "bundle", Size: 1},
		Config:   native.Config{Mode: "serving", Model: "chill_model", ProfilePath: "/profile/bundle.json", Policy: native.Policy{DecisionID: "bootstrap", ProfileID: profile.ID, MaxBatchSize: 1}},
	}
}

func TestNativePodReadbackUsesMemoryAndGPUWithoutCPUAllocation(t *testing.T) {
	pod, node, profile, _ := observerSourceFixture()
	profile.RuntimeMetadata.IntraOpThreads, profile.RuntimeMetadata.InterOpThreads = 0, 0
	profile.RuntimeMetadata.CPUAffinity = []int{7}
	container := &pod.Spec.Containers[0]
	if err := validateSourceResources(container, node, &profile); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(100, 0)
	sn := serving.SchedulerNode{Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, MemoryBytes: 1 << 30, Ready: true, PowerMode: "fixed", ObservedAt: at}
	id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
	state := serving.InstanceState{ID: id, Node: sn.Node, Profile: profile.ID, Ready: true, ObservedAt: at, Native: &native.State{Ready: true, Metadata: native.Metadata{ProfileID: profile.ID, Runtime: *profile.RuntimeMetadata, MaxQueueRequests: 4, MaxBatchSize: 1, Policy: native.Policy{MaxBatchSize: 1}}}}
	snapshot := serving.SchedulerSnapshot{At: at, ObservationEnd: at}
	captureNodePods(&snapshot, &sn, node, &core.PodList{Items: []core.Pod{*pod}}, map[string]serving.InstanceState{id: state}, map[string]*serving.Profile{profile.ID: &profile})
	if len(snapshot.Instances) != 1 || len(sn.NonServing) != 0 || snapshot.Instances[0].Allocation.MemoryBytes != 1024 {
		t.Fatalf("native physical ownership/memory lost: %+v", snapshot)
	}
	for _, resources := range []*core.ResourceList{&container.Resources.Requests, &container.Resources.Limits} {
		(*resources)[core.ResourceCPU] = resource.MustParse("0")
		if err := validateSourceResources(container, node, &profile); err == nil {
			t.Fatal("admission-injected CPU field accepted")
		}
		delete(*resources, core.ResourceCPU)
	}
}

func TestPreparationSharedCPURetainsMemoryAndDeviceAdmission(t *testing.T) {
	if err := ValidatePreparation(measuredCPUPreparationFixture()); err != nil {
		t.Fatal("runtime automatic threads rejected", err)
	}
	for _, change := range []func(*PrepareSpec){
		func(s *PrepareSpec) { s.MemoryBytes-- },
		func(s *PrepareSpec) { s.Instance.Allocation.GPUUUID = "unmeasured" },
		func(s *PrepareSpec) { s.Instance.Node.UID = "wrong-node" },
	} {
		spec := measuredCPUPreparationFixture()
		change(&spec)
		kube := kubefake.NewSimpleClientset()
		operations := &Operations{kube: kube}
		if _, err := operations.PrepareInstance(context.Background(), spec); err == nil {
			t.Fatal("unsafe preparation admitted")
		}
		if len(kube.Actions()) != 0 {
			t.Fatal("invalid preparation reached Kubernetes")
		}
	}
}
