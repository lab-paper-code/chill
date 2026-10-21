package kube

import (
	"fmt"
	"reflect"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	core "k8s.io/api/core/v1"
	"k8s.io/utils/cpuset"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	refusedCPUsUnavailable   = "CPUsUnavailable"
	refusedOnlineCPUsUnknown = "OnlineCPUsUnknown"
)

// nodeClass returns the one DeviceClass of the Node, or nil when none or
// several match.
func nodeClass(n *core.Node, classes []edge.DeviceClass) *edge.DeviceClass {
	var class *edge.DeviceClass
	for i := range classes {
		if classes[i].Spec.Architecture == n.Status.NodeInfo.Architecture && nodeClassMatches(n, &classes[i]) {
			if class != nil {
				return nil
			}
			class = &classes[i]
		}
	}
	return class
}

// readBackMode is the mode the Node is in now: the mode whose actuator the
// actuation agent read back from the host, or the Fixed mode of a Node without
// a read-back. A requested mode still pending, or several matching modes,
// leave it unknown.
func readBackMode(n *core.Node, class *edge.DeviceClass) string {
	if class == nil || n.Labels[chillmeta.PowerModeState] == chillmeta.PowerModePending {
		return ""
	}
	var actuator edge.PowerModeActuator
	readsBack := chillmeta.NodeReadBack(n, &actuator) == nil
	found := ""
	for _, m := range class.Spec.PowerModes {
		if !readsBack && m.Actuator.Kind == edge.PowerModeActuatorFixed || readsBack && reflect.DeepEqual(m.Actuator, actuator) {
			if found != "" {
				return ""
			}
			found = m.Name
		}
	}
	return found
}

// modeOnlineCPUs is the CPU set online in mode: the DeviceClass declaration,
// otherwise the Node's published online CPUs when mode is the mode it reads
// back now. The published list is the current mode's, so it says nothing of
// another mode.
func modeOnlineCPUs(n *core.Node, class *edge.DeviceClass, mode string) (cpuset.CPUSet, bool) {
	if class != nil {
		for _, m := range class.Spec.PowerModes {
			if m.Name == mode && m.OnlineCPUs != "" {
				cpus, err := cpuset.Parse(m.OnlineCPUs)
				return cpus, err == nil
			}
		}
	}
	if mode == "" || mode != readBackMode(n, class) {
		return cpuset.CPUSet{}, false
	}
	cpus, err := cpuset.Parse(n.Annotations[chillmeta.OnlineCPUs])
	return cpus, err == nil && cpus.Size() > 0
}

// cpuRefusal refuses a profiling measurement whose CPU allocation does not fit
// the CPUs its mode leaves outside the serving reservation: the option it
// measures could never be placed. A measurement without a CPU allocation is
// not checked.
func cpuRefusal(root client.Object, n *core.Node, classes []edge.DeviceClass, reserved []int) (string, string) {
	var mode string
	var affinity []int32
	if o, ok := root.(*edge.BatchEnergyOptimization); ok {
		mode, affinity = o.Spec.State.Candidate.PowerMode, o.Spec.CPUAffinity
	}
	if len(affinity) == 0 {
		return "", ""
	}
	online, known := modeOnlineCPUs(n, nodeClass(n, classes), mode)
	if !known {
		return refusedOnlineCPUsUnknown, mode
	}
	available := online.Difference(cpuset.New(reserved...))
	wanted := make([]int, len(affinity))
	for i, cpu := range affinity {
		wanted[i] = int(cpu)
	}
	if set := cpuset.New(wanted...); !set.IsSubsetOf(available) {
		return refusedCPUsUnavailable, fmt.Sprintf("%s %s available %s", set, mode, available)
	}
	return "", ""
}
