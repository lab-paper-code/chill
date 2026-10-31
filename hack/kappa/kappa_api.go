package main

import "github.com/lab-paper-code/chill/internal/serving"

// replay-gate.py replaces this binding with policy κ for archived sources that
// predate the planning envelope, preserving each source's own supply semantics.
func planningKappa(in serving.CapacityInput, rows map[string]serving.CapacityRow) float64 {
	kappa, _, _ := serving.CapacityPlanningKappa(in, rows)
	return kappa
}
