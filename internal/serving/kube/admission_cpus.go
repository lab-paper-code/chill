package kube

import (
	"fmt"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

// readBackMode is the mode the Node reads back now: the target of its latest
// Ready power change, or the only mode of a Fixed class.
func readBackMode(n *core.Node, class *edge.DeviceClass, acts []edge.NodeActuation) string {
	var latest *edge.NodeActuation
	for i := range acts {
		a := &acts[i]
		if a.Spec.Node.UID == string(n.UID) && (latest == nil || a.Spec.StageRequestedAt.After(latest.Spec.StageRequestedAt.Time)) {
			latest = a
		}
	}
	if latest != nil && latest.Status.Phase == edge.NodeActuationPhaseReady && latest.Status.TargetObservation != nil {
		return latest.Status.TargetObservation.PowerMode
	}
	if class != nil && len(class.Spec.PowerModes) == 1 && class.Spec.PowerModes[0].Actuator.Kind == edge.PowerModeActuatorFixed {
		return class.Spec.PowerModes[0].Name
	}
	return ""
}

// modeOnlineCPUs is the CPU set online in mode: the DeviceClass declaration,
// otherwise the Node's published online CPUs when mode is the mode it reads
// back now. The published list is the current mode's, so it says nothing of
// another mode.
func modeOnlineCPUs(n *core.Node, class *edge.DeviceClass, acts []edge.NodeActuation, mode string) (cpuset.CPUSet, bool) {
	if class != nil {
		for _, m := range class.Spec.PowerModes {
			if m.Name == mode && m.OnlineCPUs != "" {
				cpus, err := cpuset.Parse(m.OnlineCPUs)
				return cpus, err == nil
			}
		}
	}
	if mode == "" || mode != readBackMode(n, class, acts) {
		return cpuset.CPUSet{}, false
	}
	cpus, err := cpuset.Parse(n.Annotations[chillmeta.OnlineCPUs])
	return cpus, err == nil && cpus.Size() > 0
}

// cpuRefusal refuses a profiling measurement whose CPU allocation does not fit
// the CPUs its mode leaves outside the serving reservation: the option it
// measures could never be placed. A measurement without a CPU allocation is
// not checked.
func cpuRefusal(root client.Object, n *core.Node, classes []edge.DeviceClass, acts []edge.NodeActuation, reserved []int) (string, string) {
	var mode string
	var affinity []int32
	var quota *resource.Quantity
	switch o := root.(type) {
	case *edge.BatchEnergyOptimization:
		mode, affinity, quota = o.Spec.State.Candidate.PowerMode, o.Spec.CPUAffinity, o.Spec.CPUQuota
	case *edge.ModelLoadedIdleBaseline:
		mode, affinity, quota = o.Spec.State.Candidate.PowerMode, o.Spec.CPUAffinity, &o.Spec.CPUQuota
	default:
		return "", ""
	}
	if len(affinity) == 0 && (quota == nil || quota.IsZero()) {
		return "", ""
	}
	online, known := modeOnlineCPUs(n, nodeClass(n, classes), acts, mode)
	if !known {
		return refusedOnlineCPUsUnknown, mode
	}
	available := online.Difference(cpuset.New(reserved...))
	if len(affinity) > 0 {
		wanted := make([]int, len(affinity))
		for i, cpu := range affinity {
			wanted[i] = int(cpu)
		}
		if set := cpuset.New(wanted...); !set.IsSubsetOf(available) {
			return refusedCPUsUnavailable, fmt.Sprintf("%s %s available %s", set, mode, available)
		}
		return "", ""
	}
	if quota.Value() > int64(available.Size()) {
		return refusedCPUsUnavailable, fmt.Sprintf("quota %s %s available %s", quota.String(), mode, available)
	}
	return "", ""
}
