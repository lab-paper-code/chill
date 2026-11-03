package state

import (
	serving "github.com/lab-paper-code/chill/internal/serving"
	slices "slices"
)

func PositiveWorkloadScope(target serving.SchedulerCandidate, allowed []string) []string {
	active := []string{}
	for uid, weights := range target.Routing {
		if allowed != nil && !slices.Contains(allowed, uid) {
			continue
		}
		for _, weight := range weights {
			if weight > 0 {
				active = append(active, uid)
				break
			}
		}
	}
	return active
}
