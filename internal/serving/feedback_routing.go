package serving

import (
	"fmt"
	"math"
	"sort"
)

// FeedbackRouteInput supplies profile routing support, never a capacity witness.
type FeedbackRouteInput struct {
	InstanceID     string
	Profile        *Profile
	MaxBatch       int
	ExistingWeight int32
}

// BuildFeedbackRouting allocates existing integer route units from observed
// support. Returned weights follow input order; ties use input energy then ID.
func BuildFeedbackRouting(instances []FeedbackRouteInput, controlRouting bool) ([]int32, error) {
	weights := make([]int32, len(instances))
	if !controlRouting {
		for k, instance := range instances {
			weights[k] = instance.ExistingWeight
		}
		return weights, nil
	}
	if len(instances) == 0 {
		return weights, nil
	}
	rates := make([]float64, len(instances))
	order := make([]int, len(instances))
	total := 0.0
	for k, instance := range instances {
		if instance.Profile == nil {
			return nil, fmt.Errorf("missing routing profile: %s", instance.InstanceID)
		}
		rates[k] = capacityThroughput(instance.Profile, instance.MaxBatch)
		if rates[k] <= 0 || !schedulerFinite(rates[k]) {
			return nil, fmt.Errorf("missing routing support: %s", instance.InstanceID)
		}
		total += rates[k]
		order[k] = k
	}
	sort.Slice(order, func(a, b int) bool {
		left, right := instances[order[a]], instances[order[b]]
		le, re := capacityEnergyOrder(SchedulerInstance{Profile: left.Profile}), capacityEnergyOrder(SchedulerInstance{Profile: right.Profile})
		if le != re {
			return le < re
		}
		return left.InstanceID < right.InstanceID
	})
	sum := int32(0)
	for k, rate := range rates {
		weights[k] = int32(math.Floor(rate / total * schedulerRouteUnits))
		sum += weights[k]
	}
	for ; sum < schedulerRouteUnits; sum++ {
		to, next := -1, 0.0
		for _, k := range order {
			if ratio := float64(weights[k]+1) / rates[k]; to < 0 || ratio < next {
				to, next = k, ratio
			}
		}
		weights[to]++
	}
	return weights, nil
}
