package serving

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

const (
	operationSourceExact       = "exact"
	operationSourceNode        = "node"
	operationSourceDeviceClass = "deviceClass"
	operationSourceDefault     = occupancySourceDefault
)

// OperationCostValue is a configured whole-wall estimate, not measured evidence.
type OperationCostValue struct {
	Seconds float64 `json:"seconds"`
	Joules  float64 `json:"joules"`
}

func (s SchedulerSettings) validateEstimateDefaults() error {
	for _, kind := range []string{schedulerActionPrepare, schedulerActionRemove, schedulerActionPower} {
		if _, ok := s.DefaultOperationCost[kind]; !ok {
			return fmt.Errorf("defaultOperationCost.%s required", kind)
		}
	}
	if s.DefaultIdlePower == nil {
		return fmt.Errorf("defaultIdlePower required")
	}
	for kind, value := range s.DefaultOperationCost {
		if kind != schedulerActionPrepare && kind != schedulerActionRemove && kind != schedulerActionPower {
			return fmt.Errorf("defaultOperationCost has unsupported kind %q", kind)
		}
		if !schedulerFinite(value.Seconds) || value.Seconds*float64(time.Second) < 1 || value.Seconds >= float64(time.Duration(1<<63-1))/float64(time.Second) ||
			!schedulerFinite(value.Joules) || value.Joules < 0 {
			return fmt.Errorf("defaultOperationCost.%s needs positive finite seconds and nonnegative finite joules", kind)
		}
	}
	if !schedulerFinite(s.DefaultIdlePower.NodeWatts) || s.DefaultIdlePower.NodeWatts < 0 ||
		!schedulerFinite(s.DefaultIdlePower.PerInstanceWatts) || s.DefaultIdlePower.PerInstanceWatts < 0 {
		return fmt.Errorf("defaultIdlePower needs finite nonnegative nodeWatts and perInstanceWatts")
	}
	return nil
}

func measurementRank(node, class, targetNode, targetClass string, exact bool) int {
	if node == targetNode {
		if exact {
			return 0
		}
		return 1
	}
	if class != "" && class == targetClass {
		return 2
	}
	return 3
}

func measurementSource(rank int) string {
	return [...]string{operationSourceExact, operationSourceNode, operationSourceDeviceClass, operationSourceDefault}[rank]
}

func measurementPreferred(rank int, at time.Time, id string, oldRank int, oldAt time.Time, oldID string) bool {
	return rank < oldRank || rank == oldRank && (at.After(oldAt) || at.Equal(oldAt) && id < oldID)
}

func measurementDeviceClasses(snapshot SchedulerSnapshot, target *SchedulerCandidate) map[string]string {
	classes := map[string]string{}
	add := func(i SchedulerInstance) {
		if i.Profile != nil {
			classes[i.Node.UID] = i.Profile.Condition.DeviceClass
		}
	}
	for _, o := range snapshot.Options {
		add(o.Instance)
	}
	for _, i := range snapshot.Instances {
		add(i)
	}
	if target != nil {
		for _, i := range target.Instances {
			add(i)
		}
	}
	return classes
}

type IdlePowerDefault struct {
	NodeWatts        float64 `json:"nodeWatts"`
	PerInstanceWatts float64 `json:"perInstanceWatts"`
}

type IdlePowerProvenance struct {
	Source            string   `json:"source"`
	Watts             float64  `json:"watts"`
	References        []string `json:"references,omitempty"`
	PowerMode         string   `json:"powerMode,omitempty"`
	State             []string `json:"state,omitempty"`
	RequestWatts      *float64 `json:"requestWatts,omitempty"`
	RequestReferences []string `json:"requestReferences,omitempty"`
	// Reconstructed distinguishes unloaded + per-instance increments from one
	// directly used measured row, including a row transferred to another state.
	Reconstructed bool `json:"reconstructed,omitempty"`
}

// OperationCostProvenance is additive decision evidence for the selected cost.
type OperationCostProvenance struct {
	Source  string              `json:"source"`
	CostRef string              `json:"costRef,omitempty"`
	Value   *OperationCostValue `json:"value,omitempty"`
}

type SchedulerActionCost struct {
	Action     string                   `json:"action"`
	Kind       string                   `json:"kind"`
	Seconds    *float64                 `json:"seconds,omitempty"`
	Joules     *float64                 `json:"joules,omitempty"`
	Provenance *OperationCostProvenance `json:"provenance,omitempty"`
}

// MeasurementProfileIdentity retains the measured runtime image and version.
// Each scheduler instance is one runtime/model instance; repeated loaded states
// retain the physical replica count in the multiset.
func MeasurementProfileIdentity(p *Profile) string {
	if p == nil {
		return ""
	}
	c := p.Condition
	raw, _ := json.Marshal([]string{c.LogicalModel, c.Artifact.ExecutionPath, c.RuntimeFamily, c.Backend, c.RuntimeImage, c.RuntimeVersion})
	return string(raw)
}

// MeasurementStateIdentity resolves both historical CPU-bound and current states.
// Unknown profile IDs remain opaque: they cannot establish measurement validity.
func MeasurementStateIdentity(state string, profiles map[string]string) string {
	id, rest, found := strings.Cut(state, ":")
	if identity := profiles[id]; identity != "" {
		count := "1"
		if found && strings.Count(rest, ":") >= 2 {
			// The legacy state ends in memory:model-instance-count.
			if last := strings.LastIndexByte(rest, ':'); last >= 0 {
				if n, err := strconv.Atoi(rest[last+1:]); err == nil && n > 0 {
					count = strconv.Itoa(n)
				}
			}
		}
		return identity + ":" + count
	}
	return schedulerCostState(state)
}

func measurementStates(states []string, identities map[string]string) []string {
	out := make([]string, len(states))
	for j, state := range states {
		out[j] = MeasurementStateIdentity(state, identities)
	}
	slices.Sort(out)
	return out
}

func measurementEqualState(a, b []string, identities map[string]string) bool {
	for _, states := range [][]string{a, b} {
		for _, state := range states {
			id, _, _ := strings.Cut(state, ":")
			if identities[id] == "" {
				return false
			}
		}
	}
	return slices.Equal(measurementStates(a, identities), measurementStates(b, identities))
}

func measurementOperationMatches(cost OperationCost, action Action, identities map[string]string) bool {
	if cost.Kind == schedulerActionPower {
		return cost.From == action.From && cost.To == action.To
	}
	measured, target := cost.To, action.To
	if cost.Kind == schedulerActionRemove {
		measured, target = cost.From, action.From
	}
	return measurementEqualState([]string{measured}, []string{target}, identities)
}

func schedulerMeasurementIdentities(snapshot SchedulerSnapshot, target *SchedulerCandidate) map[string]string {
	identities := map[string]string{}
	for _, cost := range snapshot.Costs {
		for id, cell := range cost.StateIdentities {
			identities[id] = cell
		}
	}
	for _, baseline := range snapshot.Baselines {
		for id, cell := range baseline.StateIdentities {
			identities[id] = cell
		}
	}
	add := func(i SchedulerInstance) {
		if i.Profile != nil {
			identities[i.Profile.ID] = MeasurementProfileIdentity(i.Profile)
		}
	}
	for _, i := range snapshot.Instances {
		add(i)
	}
	for _, o := range snapshot.Options {
		add(o.Instance)
	}
	if target != nil {
		for _, i := range target.Instances {
			add(i)
		}
	}
	return identities
}

func actionDefaultCost(a Action) *OperationCost {
	if a.CostProvenance == nil || a.CostProvenance.Source != operationSourceDefault || a.CostProvenance.Value == nil {
		return nil
	}
	v := a.CostProvenance.Value
	duration := time.Duration(v.Seconds * float64(time.Second))
	joules := v.Joules
	if a.Status == phaseRunning && a.Elapsed >= duration {
		// An already-running operation outlived the estimate: keep one configured
		// duration of remaining work at the configured whole-wall rate.
		joules *= 1 + float64(a.Elapsed)/float64(duration)
		duration += a.Elapsed
	}
	return &OperationCost{ID: "settings.defaultOperationCost." + a.Kind, Node: a.Node, Kind: a.Kind, From: a.From, To: a.To,
		EnergyScope: schedulerWholeWall, Scope: schedulerNodeWallScope,
		Trials: []OperationTrial{{Duration: duration, Joules: joules}}}
}

func actionOperationCost(settings SchedulerSettings, costs []OperationCost, a Action) (float64, float64, float64) {
	if cost := actionDefaultCost(a); cost != nil {
		t := cost.Trials[0]
		return t.Duration.Seconds(), t.Duration.Seconds(), t.Joules
	}
	if mean, longest, joules, ok := capacityOperationCost(schedulerCost(costs, a.CostID)); ok {
		return mean, longest, joules
	}
	value := settings.DefaultOperationCost[a.Kind]
	return value.Seconds, value.Seconds, value.Joules
}
