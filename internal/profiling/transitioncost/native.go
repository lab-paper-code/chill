package transitioncost

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// NativeInstanceObservation is actual Pod allocation joined to one physical
// firstfloor actor. The profile identity is the immutable bundle really loaded
// by that actor; a host mode change never renames it into another profile.
type NativeInstanceObservation struct {
	Namespace          string       `json:"namespace"`
	PodName            string       `json:"podName"`
	PodUID             string       `json:"podUID"`
	ContainerID        string       `json:"containerID"`
	StartedAt          time.Time    `json:"startedAt"`
	RestartCount       int32        `json:"restartCount"`
	Image              string       `json:"image"`
	ImageID            string       `json:"imageID"`
	CPURequestMilli    int64        `json:"cpuRequestMilli"`
	CPULimitMilli      int64        `json:"cpuLimitMilli"`
	MemoryRequestBytes int64        `json:"memoryRequestBytes"`
	MemoryLimitBytes   int64        `json:"memoryLimitBytes"`
	GPURequest         int64        `json:"gpuRequest"`
	GPULimit           int64        `json:"gpuLimit"`
	State              native.State `json:"state"`
}
type NativeNodeObservation struct {
	NodeName   string                      `json:"nodeName"`
	NodeUID    string                      `json:"nodeUID"`
	ReceivedAt time.Time                   `json:"receivedAt"` // observer clock after actual HTTP responses
	Pods       []workloadobserver.Pod      `json:"pods"`       // all nonterminal Node Pods
	Instances  []NativeInstanceObservation `json:"instances"`
}
type NativeReadFailure struct {
	ReceivedAt time.Time `json:"receivedAt"`
	Detail     string    `json:"detail"`
}

func validNativeNode(node *NativeNodeObservation) error {
	if node == nil || node.NodeName == "" || node.NodeUID == "" || node.ReceivedAt.IsZero() || len(node.Instances) == 0 {
		return errors.New("actual model-loaded native Node witness unavailable")
	}
	seen := map[string]bool{}
	for _, i := range node.Instances {
		id := i.PodUID + "/" + i.ContainerID
		if i.PodUID == "" || i.PodName == "" || i.Namespace == "" || i.ContainerID == "" ||
			i.StartedAt.IsZero() || i.RestartCount < 0 || seen[id] || i.Image == "" || i.ImageID == "" ||
			i.CPURequestMilli <= 0 || i.CPULimitMilli < i.CPURequestMilli || i.MemoryRequestBytes <= 0 ||
			i.MemoryLimitBytes < i.MemoryRequestBytes || !validNativeState(&i.State) {
			return fmt.Errorf("native physical allocation/readiness/zero-queue proof incomplete for %s", id)
		}
		if i.State.Metadata.Runtime.GPUUUID != "" && (i.GPURequest != 1 || i.GPULimit != 1) {
			return errors.New("native GPU lacks actual whole-device allocation")
		}
		seen[id] = true
	}
	return nil
}

func validNativeState(state *native.State) bool {
	m := &state.Metadata
	return state.Ready && !state.At.IsZero() && m.Mode == "serving" && m.ProfileID != "" &&
		!m.Runtime.Synthetic && m.Runtime.RuntimeVersion != "" && len(m.Runtime.ModelFiles) != 0 &&
		len(m.Runtime.CPUAffinity) > 0 && m.Runtime.IntraOpThreads >= 1 &&
		len(m.Runtime.CPUAffinity) >= m.Runtime.IntraOpThreads && len(state.Requests) == 0
}

// VerifyNativeTransition proves the wall trial excludes Serving incremental
// executions and retains exactly the measured loaded allocation. Lost/replaced
// actors without their final counters are insufficient, not assumed idle.
func VerifyNativeTransition(before, after *NativeNodeObservation) error {
	if err := validNativeNode(before); err != nil {
		return err
	}
	if err := validNativeNode(after); err != nil {
		return err
	}
	if before.NodeName != after.NodeName || before.NodeUID != after.NodeUID || !slices.EqualFunc(before.Pods, after.Pods, workloadobserver.SamePod) || len(before.Instances) != len(after.Instances) {
		return errors.New("native Node identity/membership changed during transition")
	}
	for _, a := range before.Instances {
		found := false
		for _, b := range after.Instances {
			if a.PodUID != b.PodUID || a.ContainerID != b.ContainerID {
				continue
			}
			found = true
			if !a.StartedAt.Equal(b.StartedAt) || a.RestartCount != b.RestartCount || a.Image != b.Image || a.ImageID != b.ImageID || a.CPURequestMilli != b.CPURequestMilli || a.CPULimitMilli != b.CPULimitMilli || a.MemoryRequestBytes != b.MemoryRequestBytes || a.MemoryLimitBytes != b.MemoryLimitBytes || a.GPURequest != b.GPURequest || a.GPULimit != b.GPULimit || a.State.AcceptedRequests != b.State.AcceptedRequests || a.State.CompletedRequests != b.State.CompletedRequests || a.State.Executions != b.State.Executions || !reflect.DeepEqual(a.State.Metadata, b.State.Metadata) {
				return errors.New("native loaded condition changed or Serving executed during power window")
			}
			break
		}
		if !found {
			return errors.New("native process was replaced without final zero-inference counters")
		}
	}
	return nil
}
func cloneNativeObservation(source *NativeNodeObservation) *NativeNodeObservation {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Pods = slices.Clone(source.Pods)
	copy.Instances = slices.Clone(source.Instances)
	for index := range copy.Instances {
		state := &copy.Instances[index].State
		state.Requests = slices.Clone(state.Requests)
		state.Metadata.Runtime.CPUAffinity = slices.Clone(state.Metadata.Runtime.CPUAffinity)
		state.Metadata.Runtime.Providers = slices.Clone(state.Metadata.Runtime.Providers)
		state.Metadata.Runtime.InitializedBatches = slices.Clone(state.Metadata.Runtime.InitializedBatches)
		state.Metadata.Runtime.ModelFiles = slices.Clone(state.Metadata.Runtime.ModelFiles)
		state.Metadata.Runtime.Inputs = slices.Clone(state.Metadata.Runtime.Inputs)
		state.Metadata.Runtime.Outputs = slices.Clone(state.Metadata.Runtime.Outputs)
		if state.ArrivalAgesMS != nil {
			ages := make(map[string][]float64, len(state.ArrivalAgesMS))
			for uid, v := range state.ArrivalAgesMS {
				ages[uid] = slices.Clone(v)
			}
			state.ArrivalAgesMS = ages
		}
	}
	return &copy
}
func nativeProcessObserved(node *NativeNodeObservation, process *ProcessIdentity) bool {
	if node == nil || process == nil || process.ContainerName != "firstfloor" {
		return false
	}
	for _, i := range node.Instances {
		if i.ContainerID == process.ContainerID && i.StartedAt.Equal(process.StartedAt) && i.RestartCount == process.RestartCount {
			return true
		}
	}
	return false
}
