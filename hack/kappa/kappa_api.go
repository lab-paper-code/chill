package main

import "github.com/lab-paper-code/chill/internal/serving"

// replay-gate.py replaces this binding with policy κ for archived sources that
// predate the planning envelope, preserving each source's own supply semantics.
func planningKappa(in serving.CapacityInput, rows map[string]serving.CapacityRow) float64 {
	row := rows[in.Key()]
	if row.Kappa == nil || row.Status == serving.CapacityPending {
		return 0
	}
	return *row.Kappa
}
