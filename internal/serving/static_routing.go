package serving

import (
	"fmt"
	"maps"
	"math"
	"slices"
)

// StaticRoutingTarget selects only current physical, measured, ready instances.
// It declares a manual distribution for disabled Workloads; every Workload the
// map omits keeps its current distribution, and an entry without a positive
// weight removes that Workload's routes. It does not enumerate a decision.
func StaticRoutingTarget(snapshot SchedulerSnapshot, routing map[string]map[string]int, autonomous []string, id string) (SchedulerCandidate, error) {
	target := SchedulerCandidate{ID: id, Nodes: snapshot.Nodes, Routing: map[string]map[string]int32{}}
	definitions := map[string]*Definition{}
	for _, definition := range snapshot.Workloads {
		definitions[definition.UID] = definition
	}
	for uid := range routing {
		if definitions[uid] == nil {
			return target, fmt.Errorf("staticRouting names Workload %q that is not current", uid)
		}
		if slices.Contains(autonomous, uid) {
			return target, fmt.Errorf("staticRouting names autonomous Workload %q; it applies to disabled Workloads only", uid)
		}
	}
	instances := map[string]SchedulerInstance{}
	bind := func(physical, uid string) {
		instance, exists := instances[physical]
		if !exists {
			for _, actual := range snapshot.Instances {
				if actual.ID == physical {
					instance = actual
					instance.WorkloadUIDs = nil
				}
			}
		}
		instance.WorkloadUIDs = append(instance.WorkloadUIDs, uid)
		instances[physical] = instance
	}
	for _, definition := range snapshot.Workloads {
		weights, declared := routing[definition.UID]
		if !declared {
			target.Routing[definition.UID] = maps.Clone(snapshot.Routing[definition.UID])
			if target.Routing[definition.UID] == nil {
				target.Routing[definition.UID] = map[string]int32{}
			}
			for physical, weight := range target.Routing[definition.UID] {
				if weight > 0 {
					bind(physical, definition.UID)
				}
			}
			continue
		}
		target.Routing[definition.UID] = map[string]int32{}
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
			bind(physical, definition.UID)
			target.Routing[definition.UID][physical] = int32(weight)
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
