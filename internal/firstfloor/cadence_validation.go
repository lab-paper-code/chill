package firstfloor

import (
	"errors"
	"fmt"
	"math"
)

// Validate checks the frozen trace body at its stored/request trust boundary.
// Source gaps are observations; no statistical distance/coverage gate is added.
func (c *CadenceContract) Validate() error {
	if c == nil {
		return errors.New("active measurement requires an actual frozen cadence trace")
	}
	if c.Target.LoadContext == "" || c.Target.Boundary != NativeExchangeGapBoundary ||
		c.Target.ConditionalModel != ConditionalServiceModel || len(c.Target.Schedule) == 0 {
		return errors.New("cadence target context, schedule or native model semantics are invalid")
	}
	selected := make(map[string]bool, len(c.Target.Schedule))
	for _, phase := range c.Target.Schedule {
		if phase.Name == "" || phase.OfferedLoad == "" || phase.SharingContext == "" || selected[phase.Name] {
			return errors.New("cadence target phases must have distinct names and actual load/sharing meanings")
		}
		selected[phase.Name] = true
	}
	if c.Provenance.DrawRule != CadenceDrawRule || len(c.Provenance.Sources) == 0 || len(c.Provenance.Draws) == 0 {
		return errors.New("cadence source records and an explicit supported draw sequence are required")
	}
	for _, source := range c.Provenance.Sources {
		if err := validateCadenceSource(source, c.Target.LoadContext); err != nil {
			return err
		}
	}
	return c.validateCadenceElements(selected)
}

func validateCadenceSource(source CadenceSource, loadContext string) error {
	if source.Object.Store == "" || source.Object.Key == "" || source.Object.Size <= 0 || source.Boundary == "" {
		return errors.New("cadence source requires an actual trace object and declared source boundary")
	}
	if source.LoadContext != loadContext || source.Workload == "" || source.NodeUID == "" ||
		source.ExecutionPath == "" || source.PowerMode == "" || source.PhysicalInstanceID == "" ||
		source.TrialID == "" || source.ObservedBatch <= 0 {
		return errors.New("cadence donor identity or target load context is incomplete")
	}
	if source.WindowStart.IsZero() || !source.WindowEnd.After(source.WindowStart) {
		return errors.New("cadence source collection window is invalid")
	}
	return nil
}

func (c *CadenceContract) validateCadenceElements(selected map[string]bool) error {
	lengths := make(map[string]int, len(c.Provenance.Phases))
	for _, phase := range c.Provenance.Phases {
		if !selected[phase.Name] || lengths[phase.Name] != 0 || len(phase.Elements) == 0 {
			return errors.New("cadence phase elements must match the selected target schedule")
		}
		lengths[phase.Name] = len(phase.Elements)
		for _, element := range phase.Elements {
			if element.GapNanos < 0 || element.SourceIndex < 0 || element.SourceIndex >= len(c.Provenance.Sources) ||
				element.BatchID == 0 {
				return errors.New("cadence phase contains an invalid actual gap record")
			}
		}
	}
	if len(lengths) != len(selected) {
		return errors.New("cadence is missing actual records for a selected phase")
	}
	for _, ref := range c.Provenance.Draws {
		if ref.ElementIndex < 0 || ref.ElementIndex >= lengths[ref.Phase] {
			return fmt.Errorf("cadence draw references an absent phase element %q", ref.Phase)
		}
	}
	return nil
}

// PacingBudget is a delivery allowance, not a predicted successful duration.
// The required known-gap draws plus one possible elapsed-floor overshoot are
// added to the unchanged producer/warmup/final-read failure envelope.
func (c *CadenceContract) PacingBudget(knownSamples int) (int64, error) {
	var cycle, largest int64
	for index := range c.Provenance.Draws {
		gap := c.Draw(uint64(index)).TargetIdleNanos
		if gap > math.MaxInt64-cycle {
			return 0, errors.New("frozen cadence duration exceeds the supported clock range")
		}
		cycle += gap
		largest = max(largest, gap)
	}
	cycles, remainder := knownSamples/len(c.Provenance.Draws), knownSamples%len(c.Provenance.Draws)
	if cycles > 0 && cycle > (math.MaxInt64-largest)/int64(cycles) {
		return 0, errors.New("frozen cadence stop budget exceeds the supported clock range")
	}
	total := int64(cycles)*cycle + largest
	for index := range remainder {
		gap := c.Draw(uint64(index)).TargetIdleNanos
		if gap > math.MaxInt64-total {
			return 0, errors.New("frozen cadence stop budget exceeds the supported clock range")
		}
		total += gap
	}
	return total, nil
}
