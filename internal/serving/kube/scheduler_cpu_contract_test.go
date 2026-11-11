package kube

import (
	"context"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

const measuredTwoCoreCPUMax = "max 100000"

func measuredCPUPreparationFixture() servingstate.PrepareSpec {
	pod, node, profile, _ := observerSourceFixture()
	profile.RuntimeMetadata.CPUAffinity = []int{0, 1, 2, 3}
	profile.RuntimeMetadata.IntraOpThreads, profile.RuntimeMetadata.InterOpThreads = 0, 0
	profile.RuntimeMetadata.CgroupCPUMax = measuredTwoCoreCPUMax
	return servingstate.PrepareSpec{
		Name: pod.Name, MemoryBytes: 1024,
		Instance: serving.SchedulerInstance{ID: "measured-slot", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Profile: &profile, Weight: 1, Allocation: serving.SchedulerAllocation{MemoryBytes: 1024}},
		Store:    storekube.Binding{Store: "evidence", Endpoint: "http://evidence", Bucket: "bucket", Region: "region", CredentialsSecretName: "credentials"},
		Bundle:   evidencestore.ObjectIdentity{Store: "evidence", Key: "bundle", Size: 1},
		Config: native.Config{
			Mode: "serving", Model: profile.Condition.Actor.Model, ProfilePath: "/profile/bundle.json",
			RuntimeImage: profile.Condition.RuntimeImage, MemoryRequestBytes: 1024, MemoryLimitBytes: 1024,
			ArtifactImage: profile.Condition.Artifact.Image, ArtifactFiles: profile.Condition.Artifact.Files,
			PreprocessorImage: profile.Condition.Preprocessor.Image, PreprocessorPath: profile.Condition.Preprocessor.Path,
			Policy: native.Policy{DecisionID: "bootstrap", ProfileID: profile.ID, MaxBatchSize: 1},
		},
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
	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
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
	if err := servingops.ValidatePreparation(measuredCPUPreparationFixture()); err != nil {
		t.Fatal("runtime automatic threads rejected", err)
	}
	for _, change := range []func(*servingstate.PrepareSpec){
		func(s *servingstate.PrepareSpec) { s.MemoryBytes-- },
		func(s *servingstate.PrepareSpec) { s.Instance.Allocation.GPUUUID = "unmeasured" },
		func(s *servingstate.PrepareSpec) { s.Instance.Node.UID = "wrong-node" },
	} {
		spec := measuredCPUPreparationFixture()
		change(&spec)
		kube := kubefake.NewSimpleClientset()
		operations := &servingops.Operations{Kube: kube}
		if _, err := operations.PrepareInstance(context.Background(), spec); err == nil {
			t.Fatal("unsafe preparation admitted")
		}
		if len(kube.Actions()) != 0 {
			t.Fatal("invalid preparation reached Kubernetes")
		}
	}
}

func TestPreparationIgnoresMeasuredDeadlinePolicy(t *testing.T) {
	for _, mode := range []string{native.DeadlineKeep, native.DeadlineDrop, native.DeadlineDefer} {
		t.Run(mode, func(t *testing.T) {
			spec := measuredCPUPreparationFixture()
			spec.Instance.Profile.Condition.Firstfloor.DeadlinePolicy = mode
			if err := servingops.ValidatePreparation(spec); err != nil {
				t.Fatal("measured deadline provenance constrained serving preparation", err)
			}
		})
	}
}

func TestSchedulerPolicyAcknowledgmentIgnoresMeasuredDeadlinePolicy(t *testing.T) {
	wanted := measuredCPUPreparationFixture().Instance
	wanted.MaxBatch, wanted.MaxWait = 1, time.Millisecond
	wanted.Profile.Condition.Firstfloor.DeadlinePolicy = native.DeadlineDrop
	for _, mode := range []string{"", native.DeadlineKeep, native.DeadlineDrop, native.DeadlineDefer} {
		t.Run(mode, func(t *testing.T) {
			actual := native.Metadata{Policy: native.Policy{ProfileID: wanted.Profile.ID, MaxBatchSize: 1, MaxBatchWaitMS: 1, DeadlinePolicy: mode}}
			if !schedulerPolicyAcknowledged(wanted, actual) {
				t.Fatal("measured deadline provenance constrained serving acknowledgment")
			}
			actual.Policy.FixedBatchSize = 1
			if schedulerPolicyAcknowledged(wanted, actual) {
				t.Fatal("fixed batch mismatch escaped serving acknowledgment")
			}
		})
	}
}

func TestCaptureNativeStateFreezesBoundedArrivalHistory(t *testing.T) {
	state := &native.State{ArrivalAgesMS: map[string][]float64{"workload": {3, 7}}}
	instance := serving.SchedulerInstance{}
	captureNativeState(&instance, state)
	state.ArrivalAgesMS["workload"][0] = 99
	state.ArrivalAgesMS["other"] = []float64{1}
	if len(instance.ArrivalAgesMS) != 1 || len(instance.ArrivalAgesMS["workload"]) != 2 || instance.ArrivalAgesMS["workload"][0] != 3 || instance.ArrivalAgesMS["workload"][1] != 7 {
		t.Fatalf("snapshot history aliases mutable native state: %+v", instance.ArrivalAgesMS)
	}
	empty := serving.SchedulerInstance{}
	captureNativeState(&empty, &native.State{ArrivalAgesMS: map[string][]float64{}})
	missing := serving.SchedulerInstance{}
	captureNativeState(&missing, &native.State{})
	if empty.ArrivalAgesMS == nil || missing.ArrivalAgesMS != nil {
		t.Fatal("known-empty native history and missing evidence collapsed")
	}
}
