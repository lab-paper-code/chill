package kube

import (
	"slices"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving"
)

// GatewayTargets exports only a verified physical Service/executor pair with
// the exact policy ACK observed by the native SDK. Desired Pod metadata alone
// is never sufficient to publish a router target.
func (o *Observer) GatewayTargets() []serving.GatewayTarget {
	o.mu.Lock()
	defer o.mu.Unlock()
	result := make([]serving.GatewayTarget, 0, len(o.sources))
	for _, source := range o.sources {
		spec, state := source.spec, source.instance.State
		if source.stopped || source.deleted || !state.Ready || state.Stopped || state.Native == nil ||
			spec.InstanceID != physicalID(source.uid, source.containerID) || state.ID != spec.InstanceID ||
			spec.DecisionID == "" || state.Native.Metadata.DecisionID != spec.DecisionID || spec.Profile == nil {
			continue
		}
		result = append(result, serving.GatewayTarget{
			InstanceID: spec.InstanceID, PodUID: string(source.uid), ContainerID: source.containerID,
			Node: spec.Node, Endpoint: spec.Endpoint, ModelName: spec.ModelName, Profile: spec.Profile,
			ExpectedRuntime: spec.ExpectedRuntime, DecisionID: spec.DecisionID,
			OwnerWorkloadUID: string(source.ownerUID), Capacity: spec.Capacity,
		})
	}
	slices.SortFunc(result, func(a, b serving.GatewayTarget) int { return strings.Compare(a.InstanceID, b.InstanceID) })
	return result
}
