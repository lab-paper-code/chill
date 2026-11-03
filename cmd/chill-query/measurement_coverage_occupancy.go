package main

import (
	"fmt"
	"slices"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"github.com/lab-paper-code/chill/internal/serving"
)

const (
	occupancyStatePresent    = "present"
	occupancyStateMeasuring  = "measuring"
	occupancyStateFailed     = "failed"
	occupancyEvidenceInvalid = "EvidenceInvalid"
)

// Occupancy is indexed by physical node, frozen profile and batch; a newer
// failed attempt never hides an accepted value for the same current tuple.
func (p *coverageProjection) addOccupancy(cell *queryapi.MeasurementCoverageCell) {
	cell.Occupancy = []queryapi.MeasurementOccupancy{}
	current := map[string]bool{}
	for _, variant := range cell.Variants {
		for _, id := range variant.ProfileIDs {
			current[id] = true
		}
	}
	nodes := p.occupancyNodes(cell)
	key := occupancyEntryKey
	entries := map[string]queryapi.MeasurementOccupancy{}
	selectedAt := map[string]time.Time{}
	for _, task := range p.occupancy {
		node, exists := nodes[task.Spec.Node]
		if !exists {
			continue
		}
		matched := p.occupancyRequestMatches(task, cell)
		result, projected := p.base.occupancyResults[string(task.UID)]
		if !projected {
			result.DispatchOccupancyProgress = occupancyProgress(task)
		}
		if record := result.record; record != nil {
			// Evidence identity is authoritative, including replaced physical nodes.
			node = queryapi.MeasurementIdentity{Name: record.Node.Name, UID: record.Node.UID}
			matched = matched || record.Model == cell.Model.Name &&
				record.PowerMode == cell.PowerMode && current[record.ProfileID]
		}
		if record := result.burst; record != nil {
			node = queryapi.MeasurementIdentity{Name: record.Node.Name, UID: record.Node.UID}
			matched = record.Model == cell.Model.Name && record.PowerMode == cell.PowerMode &&
				(matched || current[record.ProfileID]) &&
				(record.Estimate == nil || record.Estimate.ExecutionPath == cell.ExecutionPath)
		}
		if !matched {
			continue
		}
		id := key(node, result.ProfileID, result.Batch)
		entry := queryapi.MeasurementOccupancy{
			Node: node, ProfileID: result.ProfileID, Batch: result.Batch, State: "none",
			Request: &queryapi.MeasurementIdentity{Name: task.Name, UID: string(task.UID)},
			Phase:   result.Phase, Reason: result.Reason, D: result.D, Evidence: result.Evidence,
		}
		setCoverageOccupancyState(&entry, task.Status.Phase, result, current, nodes)
		if previous, exists := entries[id]; exists {
			priority := occupancyPriority(entry.State) - occupancyPriority(previous.State)
			if priority < 0 || priority == 0 && !task.CreationTimestamp.Time.After(selectedAt[id]) {
				continue
			}
		}
		entries[id] = entry
		selectedAt[id] = task.CreationTimestamp.Time
	}
	p.addCatalogOccupancy(cell, nodes, current, entries)
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b string) int {
		left, right := entries[a], entries[b]
		if delta := strings.Compare(left.Node.Name, right.Node.Name); delta != 0 {
			return delta
		}
		if delta := strings.Compare(left.Node.UID, right.Node.UID); delta != 0 {
			return delta
		}
		if delta := left.Batch - right.Batch; delta != 0 {
			return delta
		}
		if delta := occupancyPriority(right.State) - occupancyPriority(left.State); delta != 0 {
			return delta
		}
		if delta := selectedAt[b].Compare(selectedAt[a]); delta != 0 {
			return delta
		}
		return strings.Compare(a, b)
	})
	for _, id := range ids {
		entry := entries[id]
		entry.Adopted = p.occupancyAdopted(&entry, cell, nodes, current)
		cell.Occupancy = append(cell.Occupancy, entry)
	}
}

func (p *coverageProjection) occupancyAdopted(entry *queryapi.MeasurementOccupancy,
	cell *queryapi.MeasurementCoverageCell, nodes map[string]queryapi.MeasurementIdentity, current map[string]bool,
) *bool {
	if p.base.acceptedOccupancy == nil || p.base.failures[coverageCatalogSource] != nil {
		return nil
	}
	for _, selected := range p.base.acceptedOccupancy {
		if selected.Provenance == nil && selected.Batch == entry.Batch &&
			selected.Node.Name == entry.Node.Name && selected.Node.UID == entry.Node.UID &&
			selected.ProfileID == entry.ProfileID &&
			occupancyTupleMatches(selected.Node, selected.Model, selected.PowerMode,
				selected.ExecutionPath, selected.ProfileID, cell, nodes, current) {
			return nil
		}
	}
	adopted := entry.SchedulerD != nil &&
		(entry.SchedulerD.Source == "measured" || entry.SchedulerD.Source == "dom")
	return &adopted
}

func (p *coverageProjection) occupancyRequestMatches(
	task api.DispatchOccupancyMeasurement, cell *queryapi.MeasurementCoverageCell,
) bool {
	for _, beo := range p.base.requests.Items {
		c := beo.Spec.State.Candidate
		if beo.Name == task.Spec.BatchEnergyOptimization &&
			c.ModelSpec == cell.Model.Name && c.DeviceClass == cell.DeviceClass.Name &&
			c.ExecutionPath == cell.ExecutionPath && c.PowerMode == cell.PowerMode {
			return true
		}
	}
	return false
}

func setCoverageOccupancyState(
	entry *queryapi.MeasurementOccupancy, phase api.MeasurementPhase, result dispatchOccupancyProjection,
	current map[string]bool, nodes map[string]queryapi.MeasurementIdentity,
) {
	switch phase {
	case "", api.MeasurementPhaseMeasuring:
		entry.State = occupancyStateMeasuring
	case api.MeasurementPhaseFailed, api.MeasurementPhaseInsufficient:
		entry.State = occupancyStateFailed
	case api.MeasurementPhaseComplete:
		entry.State, entry.Reason = occupancyStateFailed, occupancyEvidenceInvalid
	}
	if result.record != nil && result.record.Verdict == measurementVerdictAccepted && current[result.ProfileID] &&
		nodes[entry.Node.Name].UID == entry.Node.UID {
		entry.State, entry.Reason = occupancyStatePresent, result.Reason
	}
	if result.burst != nil && result.burst.Verdict == measurementVerdictObserved && current[result.ProfileID] &&
		nodes[entry.Node.Name].UID == entry.Node.UID {
		entry.State, entry.Reason = occupancyStatePresent, result.Reason
	}
	if result.ProfileID != "" && !current[result.ProfileID] {
		entry.State, entry.Reason = "none", "ProfileSuperseded"
	}
}

func occupancyPriority(state string) int {
	switch state {
	case occupancyStatePresent:
		return 3
	case occupancyStateMeasuring:
		return 2
	case occupancyStateFailed:
		return 1
	default:
		return 0
	}
}

func occupancyTupleMatches(
	node serving.NodeReference, model, powerMode, executionPath, profileID string,
	cell *queryapi.MeasurementCoverageCell, nodes map[string]queryapi.MeasurementIdentity, current map[string]bool,
) bool {
	return node.UID != "" && nodes[node.Name].UID == node.UID && current[profileID] &&
		model == cell.Model.Name && powerMode == cell.PowerMode && executionPath == cell.ExecutionPath
}

func occupancyEntryKey(node queryapi.MeasurementIdentity, profile string, batch int) string {
	return fmt.Sprintf("%s/%s/%s/%d", node.Name, node.UID, profile, batch)
}

func (p *coverageProjection) addCatalogOccupancy(
	cell *queryapi.MeasurementCoverageCell, nodes map[string]queryapi.MeasurementIdentity,
	current map[string]bool, entries map[string]queryapi.MeasurementOccupancy,
) {
	if p.base.failures[coverageCatalogSource] != nil {
		return
	}
	for i := range p.base.observedOccupancy {
		observed := &p.base.observedOccupancy[i]
		if !occupancyTupleMatches(observed.Node, observed.Model, observed.PowerMode, observed.ExecutionPath,
			observed.Profile.ProfileID, cell, nodes, current) {
			continue
		}
		node := queryapi.MeasurementIdentity{Name: observed.Node.Name, UID: observed.Node.UID}
		id := occupancyEntryKey(node, observed.Profile.ProfileID, observed.Batch)
		entry, exists := entries[id]
		if !exists {
			entry = queryapi.MeasurementOccupancy{
				Node: node, ProfileID: observed.Profile.ProfileID, Batch: observed.Batch, State: "none",
			}
		}
		entry.ObservedD = observed
		entries[id] = entry
	}
	for i := range p.base.acceptedOccupancy {
		selected := &p.base.acceptedOccupancy[i]
		if selected.Provenance == nil || !occupancyTupleMatches(selected.Node, selected.Model, selected.PowerMode,
			selected.ExecutionPath, selected.ProfileID, cell, nodes, current) {
			continue
		}
		node := queryapi.MeasurementIdentity{Name: selected.Node.Name, UID: selected.Node.UID}
		id := occupancyEntryKey(node, selected.ProfileID, selected.Batch)
		entry, exists := entries[id]
		if !exists {
			entry = queryapi.MeasurementOccupancy{
				Node: node, ProfileID: selected.ProfileID, Batch: selected.Batch, State: "none",
			}
		}
		provenance := selected.Provenance
		entry.SchedulerD = &queryapi.SchedulerOccupancy{
			Source: provenance.Source, Value: provenance.Value, Pairs: provenance.Pairs,
			Window: provenance.Window, UpdatedAt: provenance.UpdatedAt,
		}
		entries[id] = entry
	}
}

func (p *coverageProjection) occupancyNodes(
	cell *queryapi.MeasurementCoverageCell,
) map[string]queryapi.MeasurementIdentity {
	nodes := map[string]queryapi.MeasurementIdentity{}
	for _, class := range p.base.inventory.Classes {
		if class.Name != cell.DeviceClass.Name {
			continue
		}
		for _, node := range class.Nodes {
			if p.query.Node == "" || p.query.Node == node.Name {
				nodes[node.Name] = queryapi.MeasurementIdentity{Name: node.Name, UID: node.UID}
			}
		}
	}
	return nodes
}
