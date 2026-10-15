package serving

import (
	"fmt"
	"math"
	"slices"
)

// StaticRoutingTarget selects only current physical, measured, ready instances.
// It declares a manual baseline; it does not enumerate a scheduling decision.
func StaticRoutingTarget(snapshot SchedulerSnapshot, routing map[string]map[string]int, id string) (SchedulerCandidate, error) {
	target := SchedulerCandidate{ID: id, Nodes: snapshot.Nodes, Routing: map[string]map[string]int32{}}
	if len(routing) != len(snapshot.Workloads) {
		return target, fmt.Errorf("staticRouting requires the complete current workload UID map")
	}
	instances := map[string]SchedulerInstance{}
	for _, definition := range snapshot.Workloads {
		weights, ok := routing[definition.UID]
		if !ok || len(weights) == 0 {
			return target, fmt.Errorf("staticRouting lacks positive distribution for current workload %q", definition.UID)
		}
		target.Routing[definition.UID] = map[string]int32{}
		positive := false
		for physical, weight := range weights {
			if weight < 0 || int64(weight) > math.MaxInt32 {
				return target, fmt.Errorf("staticRouting weight for %q must be a nonnegative int32", physical)
			}
			var selected *SchedulerInstance
			for index := range snapshot.Instances {
				actual := &snapshot.Instances[index]
				if actual.ID == physical {
					selected = actual
					break
				}
			}
			if selected == nil || !selected.Ready || selected.Stopped || selected.Profile == nil || !MatchesProfile(definition, selected.Profile) {
				return target, fmt.Errorf("staticRouting physical target %q is not currently ready and compatible for UID %q", physical, definition.UID)
			}
			if weight == 0 {
				continue
			}
			positive = true
			instance, exists := instances[physical]
			if !exists {
				instance = *selected
				instance.WorkloadUIDs = nil
			}
			instance.WorkloadUIDs = append(instance.WorkloadUIDs, definition.UID)
			instances[physical] = instance
			target.Routing[definition.UID][physical] = int32(weight)
		}
		if !positive {
			return target, fmt.Errorf("staticRouting lacks positive distribution for UID %q", definition.UID)
		}
	}
	for _, instance := range instances {
		target.Instances = append(target.Instances, instance)
	}
	slices.SortFunc(target.Instances, func(a, b SchedulerInstance) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	return target, nil
}
