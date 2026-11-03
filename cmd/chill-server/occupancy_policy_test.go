package main

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func syntheticOccupancyPolicy() dispatchOccupancyPolicy {
	return dispatchOccupancyPolicy{SupplyMode: "closed-loop", MinimumBatches: 4, MaximumIdleFraction: .01,
		RequestLifetimeSeconds: 2, DeliveryMarginMillis: 10, PreparationSeconds: 1, JobSetupSeconds: 1, CollectionSeconds: 1,
		WindowFloorSeconds: 1, LoadPreparationFloorSeconds: 1, PlannedElapsedCapSeconds: 30, TimeMargin: 1.25,
		RelativeXPlanningPrecision: .1, PlanningConfidence: .9, MaximumIdleBaseFraction: .01,
		IdleDErrorSecondsPerRequest: .001, RefillSeconds: .02, Seed: 7}
}

func TestDispatchOccupancyPolicyStrictConfiguration(t *testing.T) {
	raw, err := os.ReadFile("../../config/samples/serving-scheduler.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSchedulerConfiguration(string(raw), 1<<20); err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	block := wire["dispatchOccupancy"].(map[string]any)
	block["workers"] = 6
	raw, _ = json.Marshal(wire)
	if _, err := decodeSchedulerConfiguration(string(raw), 1<<20); err == nil {
		t.Fatal("accepted server-derived config field")
	}
	delete(wire, "dispatchOccupancy")
	raw, _ = json.Marshal(wire)
	config, err := decodeSchedulerConfiguration(string(raw), 1<<20)
	if err != nil || config.DispatchOccupancy != nil {
		t.Fatal("missing policy acquired defaults", err)
	}
	for _, change := range []func(*dispatchOccupancyPolicy){
		func(p *dispatchOccupancyPolicy) { p.TimeMargin = math.NaN() },
		func(p *dispatchOccupancyPolicy) { p.PlanningConfidence = 1 },
		func(p *dispatchOccupancyPolicy) { p.MaximumIdleFraction = .02 },
		func(p *dispatchOccupancyPolicy) { p.RequestLifetimeSeconds = 0 },
		func(p *dispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = 5 },
		func(p *dispatchOccupancyPolicy) { p.RefillSeconds = 0 },
		func(p *dispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = math.MaxInt32 + 1 },
	} {
		p := syntheticOccupancyPolicy()
		change(&p)
		if p.validate() == nil {
			t.Fatalf("accepted invalid policy %+v", p)
		}
	}
	if err := syntheticOccupancyPolicy().validate(); err != nil {
		t.Fatal(err)
	}
}
