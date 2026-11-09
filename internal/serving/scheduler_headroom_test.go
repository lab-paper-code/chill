package serving

import (
	"math"
	"testing"
)

func TestTempoHeadroomSettingsBoundsSCH55(t *testing.T) {
	for _, h := range []float64{0, 1, .25, -.01, 1.01, math.NaN(), math.Inf(1), math.Inf(-1)} {
		s, _ := schedulerFixture()
		s.ScaleDownHeadroomFraction = h
		valid := schedulerFinite(h) && h >= 0 && h <= 1
		if (s.Validate() == nil) != valid {
			t.Fatalf("h=%g validation mismatch", h)
		}
	}
}

func TestTempoRecoverySurvivesNodeLocalTimingAndNotReadySCH56(t *testing.T) {
	s, snap := schedulerFixture()
	s.ScaleDownHeadroomFraction = .25
	old := snap.Instances[0]
	next := poolsInstance(old, "cuda", old.Node, snap.Nodes[0].PowerMode, "w1")
	profile := *next.Profile
	profile.ID = "cuda"
	metadata := *profile.RuntimeMetadata
	metadata.CPUAffinity = []int{1}
	profile.RuntimeMetadata = &metadata
	next.Profile = &profile
	snap.Options = append(snap.Options, poolsOption(next))
	snap.Costs = []OperationCost{
		poolsCost(schedulerActionPrepare, old.Node, "unloaded", schedulerState(next), snap.Nodes[0].PowerMode),
		poolsCost(schedulerActionRemove, old.Node, schedulerState(old), "unloaded", snap.Nodes[0].PowerMode),
	}
	lost := poolsAddNode(&snap, "rasp5", map[string]float64{"fixed": 10})
	for k := range snap.Nodes {
		if snap.Nodes[k].Node == lost {
			snap.Nodes[k].Ready = false
		}
	}
	capacityLoad(s, &snap, snap.Workloads[0], 72)
	capacityServiceRatesForTest(&snap, map[string]float64{old.Profile.ID: 64.28502, "cuda": 100})
	p := capacityPlannerForTest(t, s, snap)
	for n := range p.plans {
		p.plans[n] = p.nodePlans(n, p.keep.Nodes)
	}
	found := false
	for n, plans := range p.plans {
		for j, plan := range plans {
			if !plan.record.Eligible || len(plan.instances) != 1 || plan.instances[0].Profile.ID != "cuda" {
				continue
			}
			choice := make([]int, len(p.plans))
			choice[n] = j
			result := p.evaluate(choice, plan.preMax)
			if p.admits(&result) && result.feasible {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("safe CUDA replacement failed after rasp5 NotReady")
	}
}
