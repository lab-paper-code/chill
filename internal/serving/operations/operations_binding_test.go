package operations

import (
	"context"
	"testing"

	serving "github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

type bindingObservation struct {
	serving.RuntimeObservation
}

func (bindingObservation) RuntimeFacts() (map[string]*serving.Definition, []serving.RequestRow) {
	return map[string]*serving.Definition{}, nil
}
func (bindingObservation) Err() error               { return nil }
func (bindingObservation) Failure() <-chan struct{} { return nil }
func (bindingObservation) Recovery() serving.ObservationRecovery {
	return serving.ObservationRecovery{}
}
func (bindingObservation) RecordRecovery(serving.InstanceState, bool) error {
	return nil
}

func TestRemoveBindingMatchesPodOrExactExecutorIdentity(t *testing.T) {
	nodeA := serving.NodeReference{Name: "node-a", UID: "node-a-uid"}
	nodeB := serving.NodeReference{Name: "node-b", UID: "node-b-uid"}
	const podUID = "target-pod"
	const executor = podUID + "/containerd://target"
	for _, tc := range []struct {
		name      string
		instance  string
		inventory []serving.InstanceState
		wantErr   bool
	}{
		{
			name: "unrelated Pod-only inventory on another Node",
			inventory: []serving.InstanceState{
				{PodUID: podUID, Node: nodeA, Terminated: true},
				{PodUID: "other-pod", Node: nodeB, Terminated: true},
			},
		},
		{
			name:      "same Pod-only identity observed on another Node",
			inventory: []serving.InstanceState{{PodUID: podUID, Node: nodeB, Terminated: true}},
			wantErr:   true,
		},
		{
			name:      "same Pod executor observed on another Node",
			inventory: []serving.InstanceState{{ID: executor, PodUID: podUID, Node: nodeB, Terminated: true}},
			wantErr:   true,
		},
		{
			name:     "executor comparison ignores another executor of the same Pod",
			instance: executor,
			inventory: []serving.InstanceState{
				{ID: executor, PodUID: podUID, Node: nodeA, Terminated: true},
				{ID: podUID + "/containerd://other", PodUID: podUID, Node: nodeB, Terminated: true},
			},
		},
		{
			name:      "exact executor observed on another Node",
			instance:  executor,
			inventory: []serving.InstanceState{{ID: executor, PodUID: podUID, Node: nodeB, Terminated: true}},
			wantErr:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			runtime, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: bindingObservation{}})
			if err != nil {
				t.Fatal(err)
			}
			var instances []serving.ReadyInstance
			for _, state := range tc.inventory {
				instances = append(instances, serving.ReadyInstance{State: state})
			}
			if err := runtime.InstallInstances(instances); err != nil {
				t.Fatal(err)
			}
			pod := &core.Pod{
				ObjectMeta: meta.ObjectMeta{Name: "target", Namespace: "chill", UID: podUID,
					Labels: map[string]string{"app.kubernetes.io/component": servingstate.ServingInstanceComponent}},
				Spec: core.PodSpec{NodeName: nodeA.Name},
				Status: core.PodStatus{Phase: core.PodSucceeded, ContainerStatuses: []core.ContainerStatus{{
					Name: servingstate.FirstfloorContainerName, ContainerID: "containerd://target",
					State: core.ContainerState{Terminated: &core.ContainerStateTerminated{}},
				}}},
			}
			node := &core.Node{ObjectMeta: meta.ObjectMeta{Name: nodeA.Name, UID: "node-a-uid"}}
			operations := &Operations{Runtime: runtime, Kube: kubefake.NewSimpleClientset(node, pod), Config: OperationConfig{Namespace: pod.Namespace}}
			err = operations.ValidateOperationBinding(ctx, servingstate.Operation{
				Kind: servingstate.OperationRemove, Node: nodeA, PodUID: podUID, Instance: tc.instance,
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("remove binding error = %v, want error %v", err, tc.wantErr)
			}
		})
	}
}
