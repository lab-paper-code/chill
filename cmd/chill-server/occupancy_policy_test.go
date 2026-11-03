package main

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

func syntheticOccupancyPolicy() dispatchOccupancyPolicy {
	return dispatchOccupancyPolicy{WarmupSeconds: 5, BurstSeconds: 15, Workers: 8, MinimumPairs: 100,
		MaximumObservationWindowSeconds: 3600,
		RequestLifetimeSeconds:          2, DeliveryMarginMillis: 10, PreparationSeconds: 1,
		JobSetupSeconds: 1, CollectionSeconds: 1,
		PlannedElapsedCapSeconds: 30, Seed: 7}
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
	for _, field := range []string{
		"supplyMode", "minimumBatches", "maximumIdleFraction", "windowFloorSeconds",
		"loadPreparationFloorSeconds", "timeMargin", "relativeXPlanningPrecision", "planningConfidence",
		"maximumIdleBaseFraction", "idleDErrorSecondsPerRequest", "refillSeconds",
	} {
		block[field] = 1
		raw, _ = json.Marshal(wire)
		if _, err := decodeSchedulerConfiguration(string(raw), 1<<20); err == nil {
			t.Fatal("accepted removed or server-owned config field", field)
		}
		delete(block, field)
	}
	delete(wire, "dispatchOccupancy")
	raw, _ = json.Marshal(wire)
	config, err := decodeSchedulerConfiguration(string(raw), 1<<20)
	if err != nil || config.DispatchOccupancy != nil {
		t.Fatal("missing policy acquired defaults", err)
	}
	for _, change := range []func(*dispatchOccupancyPolicy){
		func(p *dispatchOccupancyPolicy) { p.WarmupSeconds = -1 },
		func(p *dispatchOccupancyPolicy) { p.BurstSeconds = 0 },
		func(p *dispatchOccupancyPolicy) { p.Workers = 0 },
		func(p *dispatchOccupancyPolicy) { p.Workers = 65 },
		func(p *dispatchOccupancyPolicy) { p.MinimumPairs = 0 },
		func(p *dispatchOccupancyPolicy) { p.MaximumObservationWindowSeconds = 0 },
		func(p *dispatchOccupancyPolicy) { p.MaximumObservationWindowSeconds = p.BurstSeconds - 1 },
		func(p *dispatchOccupancyPolicy) { p.RequestLifetimeSeconds = 0 },
		func(p *dispatchOccupancyPolicy) { p.DeliveryMarginMillis = 0 },
		func(p *dispatchOccupancyPolicy) { p.PreparationSeconds = 0 },
		func(p *dispatchOccupancyPolicy) { p.JobSetupSeconds = -1 },
		func(p *dispatchOccupancyPolicy) { p.CollectionSeconds = -1 },
		func(p *dispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = 5 },
		func(p *dispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = math.NaN() },
		func(p *dispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = math.Inf(1) },
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

func TestOccupancyPolicyZeroInputsAreExplicitNotGoFallbacks(t *testing.T) {
	for _, field := range []string{"warmupSeconds", "jobSetupSeconds", "collectionSeconds", "seed"} {
		raw, err := json.Marshal(syntheticOccupancyPolicy())
		if err != nil {
			t.Fatal(err)
		}
		var block map[string]json.RawMessage
		if err = json.Unmarshal(raw, &block); err != nil {
			t.Fatal(err)
		}
		original := block[field]
		for _, value := range []json.RawMessage{nil, json.RawMessage("null")} {
			block[field] = value
			if value == nil {
				delete(block, field)
			}
			raw, err = json.Marshal(block)
			if err != nil {
				t.Fatal(err)
			}
			var policy dispatchOccupancyPolicy
			if err = json.Unmarshal(raw, &policy); err == nil {
				t.Fatalf("omitted/null %s silently became a Go default", field)
			}
			block[field] = original
		}
		block[field] = json.RawMessage("0")
		raw, err = json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		var policy dispatchOccupancyPolicy
		if err = json.Unmarshal(raw, &policy); err != nil {
			t.Fatal(err)
		}
		if err = policy.validate(); err != nil {
			t.Fatal("explicit zero rejected", field, err)
		}
	}
}
