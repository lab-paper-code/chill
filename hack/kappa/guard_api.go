package main

import "github.com/lab-paper-code/chill/internal/serving"

// The committed shim matches a tree without the reduction guard. replay-gate.py
// replaces this file with the guard-backed version when the source under test
// defines serving.SchedulerReductionCommit, so the instrument never fails to
// build against either generation.
const guardAvailable = false

func reductionWorkloads(serving.SchedulerSettings, serving.SchedulerSnapshot, []serving.CapacityRow, serving.SchedulerCandidate) []string {
	return nil
}

func reductionCommit(serving.SchedulerSettings, serving.SchedulerSnapshot, []serving.CapacityRow, serving.SchedulerCandidate, []string) string {
	return "GuardNotPresentInSource"
}
