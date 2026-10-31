package main

import "github.com/lab-paper-code/chill/internal/serving"

const guardAvailable = true

func reductionWorkloads(s serving.SchedulerSettings, snap serving.SchedulerSnapshot, rows []serving.CapacityRow, target serving.SchedulerCandidate) []string {
	return serving.SchedulerReductionWorkloads(s, snap, rows, target)
}

func reductionCommit(s serving.SchedulerSettings, snap serving.SchedulerSnapshot, rows []serving.CapacityRow, target serving.SchedulerCandidate, uids []string) string {
	return serving.SchedulerReductionCommit(s, snap, rows, target, uids)
}
