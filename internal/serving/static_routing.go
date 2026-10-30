package serving

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

// StaticRoutingTarget selects only current physical, measured, ready instances.
// It declares a manual distribution for disabled Workloads; every Workload the
// map omits keeps its current distribution, and an entry without a positive
// weight removes that Workload's routes. An instance serves one Workload, so a
// final distribution that routes two Workloads to one instance is rejected.
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
	bind := func(physical, uid string) error {
		instance, exists := instances[physical]
		if exists && instance.WorkloadUID != uid {
			return fmt.Errorf("staticRouting routes Workloads %q and %q to instance %q; an instance serves one Workload", instance.WorkloadUID, uid, physical)
		}
		for _, actual := range snapshot.Instances {
			if actual.ID == physical {
				instance = actual
			}
		}
		instance.WorkloadUID = uid
		instances[physical] = instance
		return nil
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
					if err := bind(physical, definition.UID); err != nil {
						return target, err
					}
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
			if err := bind(physical, definition.UID); err != nil {
				return target, err
			}
			target.Routing[definition.UID][physical] = int32(weight)
		}
	}
	for _, instance := range instances {
		target.Instances = append(target.Instances, instance)
	}
	slices.SortFunc(target.Instances, func(a, b SchedulerInstance) int {
		return strings.Compare(a.ID, b.ID)
	})
	return target, nil
}
