package state

import (
	reflect "reflect"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	core "k8s.io/api/core/v1"
)

// NodeClass returns the one DeviceClass of the Node, or nil when none or
// several match.
func NodeClass(n *core.Node, classes []edge.DeviceClass) *edge.DeviceClass {
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

// ReadBackMode is the mode the Node is in now: the mode whose actuator the
// actuation agent read back from the host, or the Fixed mode of a Node without
// a read-back. A requested mode still pending, or several matching modes,
// leave it unknown.
func ReadBackMode(n *core.Node, class *edge.DeviceClass) string {
	if class == nil || n.Labels[chillmeta.PowerModeState] == chillmeta.PowerModePending {
		return ""
	}
	var actuator edge.PowerModeActuator
	readsBack := chillmeta.NodeReadBack(n, &actuator) == nil
	// With no mode request, a discrete GPU remains under the installation's
	// default policy. Its instantaneous MHz is still published, but is not a
	// declared locked-mode identity.
	if readsBack && actuator.Kind == edge.PowerModeActuatorNvidiaLockedGPUClock &&
		n.Labels[chillmeta.PowerMode] == "" {
		readsBack = false
	}
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

func nodeClassMatches(node *core.Node, class *edge.DeviceClass) bool {
	if len(class.Spec.NodeSelector) == 0 {
		return false
	}
	for key, value := range class.Spec.NodeSelector {
		if node.Labels[key] != value {
			return false
		}
	}
	return true
}
