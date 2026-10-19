package kube

import (
	"context"
	"slices"
	"strconv"
	"strings"
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

const measuredTwoCoreCPUMax = "200000 100000"

func TestNativePodReadbackKeepsMeasuredWideAffinityResourceEvaluable(t *testing.T) {
	for _, request := range []string{"500m", "1500m", "2"} {
		t.Run(request, func(t *testing.T) {
			pod, node, profile, _ := observerSourceFixture()
			profile.Condition.CPU.Request, profile.Condition.CPU.Limit = request, "2"
			profile.Condition.CPU.ORTIntraOpThreads = 2
			profile.Condition.CPU.AffinityCPUs = []int{0, 1, 2, 3}
			profile.RuntimeMetadata.CPUAffinity = []int{0, 1, 2, 3}
			profile.RuntimeMetadata.IntraOpThreads = 2
			profile.RuntimeMetadata.CgroupCPUMax = measuredTwoCoreCPUMax
			container := &pod.Spec.Containers[0]
			container.Resources.Requests[core.ResourceCPU] = resource.MustParse(request)
			container.Resources.Limits[core.ResourceCPU] = resource.MustParse("2")
			container.Resources.Requests[core.ResourceMemory] = resource.MustParse("1024")
			container.Resources.Limits[core.ResourceMemory] = resource.MustParse("1024")
			if err := validateSourceResources(container, node, &profile); err != nil {
				t.Fatalf("actual measured Pod quota rejected: %v", err)
			}
			at := time.Unix(100, 0)
			sn := serving.SchedulerNode{
				Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: "pool",
				CPUs: []int{0, 1, 2, 3}, MemoryBytes: 1 << 30, Ready: true, PowerMode: "fixed", ObservedAt: at,
			}
			id := physicalID(pod.UID, firstfloorStatus(pod).ContainerID)
			state := serving.InstanceState{
				ID: id, Node: sn.Node, Pool: sn.Pool, Profile: profile.ID, Ready: true, ObservedAt: at,
				Native: &native.State{Ready: true, Metadata: native.Metadata{
					ProfileID: profile.ID, Runtime: *profile.RuntimeMetadata, MaxQueueRequests: 4, MaxBatchSize: 1,
					Policy: native.Policy{MaxBatchSize: 1},
				}},
			}
			snapshot := serving.SchedulerSnapshot{At: at, ObservationEnd: at}
			captureNodePods(&snapshot, &sn, node, &core.PodList{Items: []core.Pod{*pod}},
				map[string]serving.InstanceState{id: state}, map[string]*serving.Profile{profile.ID: &profile})
			if len(snapshot.Instances) != 1 || len(snapshot.Reasons) != 0 || len(sn.NonServing) != 0 {
				t.Fatalf("actual native ownership lost: %+v", snapshot)
			}
			allocation := snapshot.Instances[0].Allocation
			quantity := resource.MustParse(request)
			if allocation.CPUCount != int(quantity.Value()) || allocation.Threads != 2 ||
				!slices.Equal(allocation.CPUs, []int{0, 1, 2, 3}) {
				t.Fatalf("readback conflated request, affinity and threads: %+v", allocation)
			}
			snapshot.Nodes = []serving.SchedulerNode{sn}
			settings := serving.SchedulerSettings{
				Algorithm: serving.SchedulerAlgorithmFixedHorizon, ObservationWindow: time.Second,
				ControlInterval: time.Second, Horizon: time.Second, MaxObservationAge: time.Second,
				Replays: 1, MaxCandidates: 1, MaxReplicas: 1, MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 4,
			}
			decision, err := serving.EvaluateScheduler(context.Background(), settings, snapshot)
			if err != nil {
				t.Fatal(err)
			}
			for _, reason := range decision.Keep.Reasons {
				for _, resourceReason := range []string{
					"CPUThreadAllocationMismatch:", "MeasuredAffinityMismatch:", "UnknownExclusiveCPUAffinity:",
					"ExclusiveCPUInfeasible:", "CPUBudgetInfeasible:", "MeasuredMemoryInfeasible:",
					"MemoryBudgetInfeasible:", "ExclusiveGPUInfeasible:",
				} {
					if strings.HasPrefix(reason, resourceReason) {
						t.Fatalf("actual native readback became resource-infeasible: %v", decision.Keep.Reasons)
					}
				}
			}
		})
	}
}

func measuredCPUPreparationFixture() PrepareSpec {
	pod, node, profile, _ := observerSourceFixture()
	profile.Condition.CPU.Request, profile.Condition.CPU.Limit = "1500m", "2"
	profile.Condition.CPU.ORTIntraOpThreads = 2
	profile.Condition.CPU.AffinityCPUs = []int{0, 1, 2, 3}
	profile.RuntimeMetadata.CPUAffinity = []int{0, 1, 2, 3}
	profile.RuntimeMetadata.IntraOpThreads = 2
	profile.RuntimeMetadata.CgroupCPUMax = measuredTwoCoreCPUMax
	return PrepareSpec{
		Name: pod.Name, MemoryBytes: 1024,
		Instance: serving.SchedulerInstance{
			ID: "measured-slot", Node: serving.NodeReference{Name: node.Name, UID: string(node.UID)}, Pool: "pool",
			Profile: &profile, Weight: 1,
			Allocation: serving.SchedulerAllocation{CPUs: []int{0, 1, 2, 3}, CPUCount: 2, Threads: 2, MemoryBytes: 1024},
		},
		Store:  storekube.Binding{Store: "evidence", Endpoint: "http://evidence", Bucket: "bucket", Region: "region", CredentialsSecretName: "credentials"},
		Bundle: evidencestore.ObjectIdentity{Store: "evidence", Key: "bundle", Size: 1},
		Config: native.Config{Mode: "serving", Model: "chill_model", ProfilePath: "/profile/bundle.json",
			Policy: native.Policy{DecisionID: "bootstrap", ProfileID: profile.ID, MaxBatchSize: 1}},
	}
}

func TestSchedulerConfigurationBindsCPUCountToMeasuredRequest(t *testing.T) {
	settings := serving.SchedulerSettings{
		Algorithm: serving.SchedulerAlgorithmFixedHorizon, ObservationWindow: time.Second,
		ControlInterval: time.Second, Horizon: time.Second, MaxObservationAge: time.Second,
		Replays: 1, MaxCandidates: 1, MaxReplicas: 1, MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 4,
	}
	controller := &SchedulerController{}
	spec := measuredCPUPreparationFixture()
	config := SchedulerControllerConfig{Settings: settings, Preparations: map[string]PrepareSpec{spec.Instance.Profile.ID: spec}}
	if err := controller.SetConfiguration(config); err != nil {
		t.Fatalf("consistent wide-affinity preparation rejected: %v", err)
	}
	for _, count := range []int{-1, 0, 1, 3, 4} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			inconsistent := measuredCPUPreparationFixture()
			inconsistent.Instance.Allocation.CPUCount = count
			changed := SchedulerControllerConfig{Settings: settings, Preparations: map[string]PrepareSpec{inconsistent.Instance.Profile.ID: inconsistent}}
			changed.Settings.ControlInterval = 2 * time.Second
			if err := controller.SetConfiguration(changed); err == nil || !strings.Contains(err.Error(), "CPU request count differs") {
				t.Fatalf("inconsistent profile-bound count %d entered planning: %v", count, err)
			}
			if controller.Settings() != settings {
				t.Fatal("rejected preparation replaced the admitted configuration")
			}
		})
	}
	// Existing zero-count semantics use affinity width, but only when that
	// effective count equals the measured request rounded to a CPU budget.
	spec = measuredCPUPreparationFixture()
	spec.Instance.Allocation.CPUCount = 0
	spec.Instance.Allocation.CPUs = []int{0, 1}
	spec.Instance.Profile.Condition.CPU.AffinityCPUs = []int{0, 1}
	spec.Instance.Profile.RuntimeMetadata.CPUAffinity = []int{0, 1}
	config.Preparations = map[string]PrepareSpec{spec.Instance.Profile.ID: spec}
	if err := controller.SetConfiguration(config); err != nil {
		t.Fatalf("consistent implicit request budget rejected: %v", err)
	}
}

func TestPreparationRejectsUndercountedMeasuredRequestBeforeKubernetesAccess(t *testing.T) {
	spec := measuredCPUPreparationFixture()
	spec.Instance.Allocation.CPUCount = 1
	kube := kubefake.NewSimpleClientset()
	operations := &Operations{kube: kube}
	err := operations.PrepareInstance(context.Background(), spec)
	if err == nil || !strings.Contains(err.Error(), "CPU request count differs") {
		t.Fatalf("undercounted preparation was not rejected at admission: %v", err)
	}
	if len(kube.Actions()) != 0 {
		t.Fatal("undercounted measured preparation reached Kubernetes before rejection")
	}
}
