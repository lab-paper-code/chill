package kube

import (
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/serving"
	core "k8s.io/api/core/v1"
)

// nodePowerTargets returns, by mode, the maximum actuation duration of each
// mode the Node can be switched into: the non-Fixed modes of its one
// DeviceClass that have a configured actuation policy.
func nodePowerTargets(n *core.Node, classes []edge.DeviceClass, policies []nodeactuator.ActuationPolicySpec) map[string]time.Duration {
	class := servingstate.NodeClass(n, classes)
	if class == nil {
		return nil
	}
	targets := map[string]time.Duration{}
	for _, mode := range class.Spec.PowerModes {
		for _, p := range policies {
			if mode.Actuator.Kind != edge.PowerModeActuatorFixed && p.DeviceClass == class.Name && p.PowerMode == mode.Name &&
				p.ActuatorKind == string(mode.Actuator.Kind) {
				targets[mode.Name] = time.Duration(p.MaxActuationDurationSeconds) * time.Second
			}
		}
	}
	return targets
}

// captureNodeInventory retains GPU identity and quantity checks. CPU online
// observations belong to the node agent, not placement admission.
func captureNodeInventory(sn *serving.SchedulerNode, n *core.Node, profiles []serving.Profile) {
	sn.GPUUUIDs = measuredGPUs(profiles, sn.Node.UID)
	gpuQuantity := n.Status.Allocatable["nvidia.com/gpu"]
	if int64(len(sn.GPUUUIDs)) > gpuQuantity.Value() ||
		sn.Reserved.GPUUUID != "" && !slices.Contains(sn.GPUUUIDs, sn.Reserved.GPUUUID) {
		sn.Reasons = append(sn.Reasons, serving.NodePlacementInventoryMissing)
	}
}

// measuredGPUs is the GPU inventory of one Node: the GPUs its verified
// profiles were measured on.
func measuredGPUs(profiles []serving.Profile, nodeUID string) []string {
	var gpus []string
	for _, p := range profiles {
		if p.Condition.NodeUID == nodeUID && p.RuntimeMetadata != nil && p.RuntimeMetadata.GPUUUID != "" &&
			!slices.Contains(gpus, p.RuntimeMetadata.GPUUUID) {
			gpus = append(gpus, p.RuntimeMetadata.GPUUUID)
		}
	}
	slices.Sort(gpus)
	return gpus
}

func bindProfileMeters(s *serving.SchedulerSnapshot, meters []servingops.MeterDeclaration) {
	for index := range s.Instances {
		bindProfileMeter(&s.Instances[index], meters)
	}
	for index := range s.Options {
		bindProfileMeter(&s.Options[index].Instance, meters)
	}
}

// captureNodePods records CHILL serving instances and the non-serving budget.
// A Pod CHILL did not place leaves the Node in the POOL; when its GPU or CPU use
// cannot be admitted the Node gets PlacementBudgetUnavailable.
func captureNodePods(s *serving.SchedulerSnapshot, sn *serving.SchedulerNode, n *core.Node, pods *core.PodList, physical map[string]serving.InstanceState, profileByID map[string]*serving.Profile) {
	var nonServingMemory uint64
	servingUIDs := map[string]bool{}
	for _, state := range physical {
		if state.Node.UID != sn.Node.UID {
			continue
		}
		uid := state.PodUID
		if uid == "" {
			uid, _, _ = strings.Cut(state.ID, "/")
		}
		servingUIDs[uid] = true
		instance := serving.SchedulerInstance{ID: state.ID, TargetID: state.TargetID, OwnerWorkloadUID: state.OwnerWorkloadUID,
			Node: state.Node, Profile: profileByID[state.Profile], Allocation: state.Allocation, Ready: state.Ready && !state.Terminated,
			Stopped: state.Stopped, Terminated: state.Terminated, ObservedAt: state.ObservedAt, Routable: state.Routable, Weight: 1}
		instance.PodUID = state.PodUID
		instance.StartupFailure = state.StartupFailure
		instance.ActualImageID = state.ActualImageID
		instance.DesiredRuntimeImage = state.DesiredRuntimeImage
		instance.ReplacementReason = state.ReplacementReason
		if state.Reason != "" {
			instance.Reasons = []string{state.Reason}
		}
		if !state.Terminated && (state.Native == nil || !state.Ready) {
			sn.Reasons = appendReason(sn.Reasons, serving.NodeInstanceUnavailable)
			instance.Ready = false
		}
		if state.Native != nil {
			m := state.Native.Metadata
			instance.Allocation.GPUUUID = m.Runtime.GPUUUID
			instance.Ready = instance.Ready && state.Native.Ready
			instance.Capacity, instance.MaxBatch, instance.FixedBatchSize = m.MaxQueueRequests, m.MaxBatchSize, m.Policy.FixedBatchSize
			instance.MaxWait = time.Duration(m.Policy.MaxBatchWaitMS * float64(time.Millisecond))
			if m.Policy.MaxBatchSize > 0 {
				instance.MaxBatch = m.Policy.MaxBatchSize
			}
		}
		for _, pod := range pods.Items {
			if string(pod.UID) != uid {
				continue
			}
			instance.PodName = pod.Name
			for _, container := range pod.Spec.Containers {
				if container.Name == servingstate.FirstfloorContainerName {
					instance.Allocation.MemoryBytes = uint64(container.Resources.Limits.Memory().Value())
				}
			}
			if weight, err := strconv.Atoi(pod.Annotations[servingstate.ServingWeightAnnotation]); err == nil {
				instance.Weight = int32(weight)
			}
		}
		for _, workload := range state.Workloads {
			if instance.WorkloadUID == "" || workload.UID < instance.WorkloadUID {
				instance.WorkloadUID = workload.UID
			}
		}
		s.Instances = append(s.Instances, instance)
	}
	for j := range pods.Items {
		p := &pods.Items[j]
		if p.Spec.NodeName != n.Name || servingUIDs[string(p.UID)] || p.Status.Phase == core.PodSucceeded || p.Status.Phase == core.PodFailed {
			continue
		}
		memory, gpu := podBudget(p)
		if gpu {
			sn.Reasons = appendReason(sn.Reasons, serving.NodePlacementBudgetUnavailable)
		}
		if math.MaxUint64-nonServingMemory < uint64(memory) {
			sn.Reasons = appendReason(sn.Reasons, serving.NodePlacementBudgetUnavailable)
			continue
		}
		nonServingMemory += uint64(memory)
	}
	if nonServingMemory > 0 {
		sn.NonServing = append(sn.NonServing, serving.SchedulerAllocation{MemoryBytes: nonServingMemory})
	}
}

func appendReason(reasons []string, reason string) []string {
	if slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}

func bindProfileMeter(instance *serving.SchedulerInstance, meters []servingops.MeterDeclaration) {
	if instance.Profile == nil {
		return
	}
	for _, meter := range meters {
		if meter.Node == instance.Node && meter.Scope == servingstate.MeasurementWallScope &&
			meter.Source.NodeName == instance.Profile.MetricSource.NodeName && sameWallSeries(meter.Source, instance.Profile.MetricSource) {
			instance.IdleMeter = meter.ID
			instance.IdleScope = meter.Scope
			return
		}
	}
}

func sameWallSeries(a, b metricobserver.SourceIdentity) bool {
	return a.Kind == b.Kind && a.Endpoint == b.Endpoint && a.Metric == b.Metric && reflect.DeepEqual(a.MetricLabels, b.MetricLabels)
}

func schedulerPhysicalState(id string, physical []serving.InstanceState) *serving.InstanceState {
	for index := range physical {
		if physical[index].ID == id {
			return &physical[index]
		}
	}
	var state *serving.InstanceState
	for index := range physical {
		if physical[index].TargetID == id && !physical[index].Terminated {
			if state != nil {
				return nil
			}
			state = &physical[index]
		}
	}
	return state
}

func schedulerTargetReady(wanted serving.SchedulerInstance, snapshot serving.SchedulerSnapshot, physical []serving.InstanceState, requirePolicy bool) bool {
	if wanted.Terminated || wanted.Profile == nil {
		return false
	}
	state := schedulerPhysicalState(wanted.ID, physical)
	if state == nil || !state.Ready || state.Stopped || state.Terminated || state.Native == nil || !state.Native.Ready {
		return false
	}
	captured := slices.ContainsFunc(snapshot.Instances, func(actual serving.SchedulerInstance) bool {
		return actual.ID == state.ID && actual.Ready && !actual.Stopped && !actual.Terminated && actual.Node == wanted.Node &&
			serving.SchedulerInstanceState(actual) == serving.SchedulerInstanceState(wanted)
	})
	if !captured || state.Native.Metadata.ProfileID != wanted.Profile.ID || state.Native.Metadata.MaxQueueRequests != wanted.Capacity {
		return false
	}
	return !requirePolicy || schedulerPolicyAcknowledged(wanted, state.Native.Metadata)
}

func schedulerPolicyAcknowledged(wanted serving.SchedulerInstance, actual native.Metadata) bool {
	if wanted.Profile.Condition.Firstfloor == nil {
		return false
	}
	maxBatch := actual.Policy.MaxBatchSize
	if maxBatch == 0 {
		maxBatch = actual.MaxBatchSize
	}
	return actual.Policy.ProfileID == wanted.Profile.ID && maxBatch == wanted.MaxBatch && actual.Policy.FixedBatchSize == wanted.FixedBatchSize &&
		actual.Policy.MaxBatchWaitMS == float64(wanted.MaxWait)/float64(time.Millisecond) &&
		native.EffectiveDeadlinePolicy(actual.Policy.DeadlinePolicy) == native.EffectiveDeadlinePolicy(wanted.Profile.Condition.Firstfloor.DeadlinePolicy)
}

func podBudget(pod *core.Pod) (int64, bool) {
	var memory, sideMemory, initMemory int64
	gpu := false
	for _, container := range pod.Spec.Containers {
		memory += container.Resources.Requests.Memory().Value()
		q := container.Resources.Requests["nvidia.com/gpu"]
		gpu = gpu || q.Sign() > 0
	}
	for _, container := range pod.Spec.InitContainers {
		m := container.Resources.Requests.Memory().Value()
		q := container.Resources.Requests["nvidia.com/gpu"]
		gpu = gpu || q.Sign() > 0
		if container.RestartPolicy != nil && *container.RestartPolicy == core.ContainerRestartPolicyAlways {
			sideMemory += m
			m = sideMemory
		} else {
			m += sideMemory
		}
		initMemory = max(initMemory, m)
	}
	memory = max(memory+sideMemory, initMemory) + pod.Spec.Overhead.Memory().Value()
	return memory, gpu
}
