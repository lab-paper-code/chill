package application

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving/measurement"

	"github.com/lab-paper-code/chill/internal/serving"
)

func syntheticOccupancyPolicy() measurement.DispatchOccupancyPolicy {
	return measurement.DispatchOccupancyPolicy{WarmupSeconds: 5, BurstSeconds: 15, Workers: 8, MinimumPairs: 100,
		AggregationWindowSeconds: 120, RefreshIntervalSeconds: 60, ScanTimeoutSeconds: 30,
		MaximumRequests:        250000,
		RequestLifetimeSeconds: 2, DeliveryMarginMillis: 10, PreparationSeconds: 1,
		JobSetupSeconds: 1, CollectionSeconds: 1,
		PlannedElapsedCapSeconds: 30, Seed: 7}
}

func TestDispatchOccupancyPolicyStrictConfiguration(t *testing.T) {
	raw, err := os.ReadFile("../../../config/samples/serving-scheduler.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSchedulerConfiguration(string(raw), 1<<20); err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	block := wire["dispatchOccupancy"].(map[string]any)
	configured, err := DecodeSchedulerConfiguration(string(raw), 1<<20)
	if err != nil || configured.Settings.DefaultDispatchOccupancyNanos != 2000000 {
		t.Fatal("authoritative settings defaults were not retained", err)
	}
	for _, field := range []string{
		"aggregationWindowSeconds", "refreshIntervalSeconds", "scanTimeoutSeconds", "maximumRequests",
	} {
		original := block[field]
		delete(block, field)
		raw, _ = json.Marshal(wire)
		if _, err := DecodeSchedulerConfiguration(string(raw), 1<<20); err == nil {
			t.Fatalf("omitted %s acquired a Go policy default", field)
		}
		block[field] = original
	}
	for _, field := range []string{
		"supplyMode", "minimumBatches", "maximumIdleFraction", "windowFloorSeconds",
		"defaultPerRequestNanos", "defaultPerRequestNanosByBatch",
		"loadPreparationFloorSeconds", "timeMargin", "relativeXPlanningPrecision", "planningConfidence",
		"maximumIdleBaseFraction", "idleDErrorSecondsPerRequest", "refillSeconds",
	} {
		block[field] = 1
		raw, _ = json.Marshal(wire)
		if _, err := DecodeSchedulerConfiguration(string(raw), 1<<20); err == nil {
			t.Fatal("accepted removed or server-owned config field", field)
		}
		delete(block, field)
	}
	settings := wire["settings"].(map[string]any)
	delete(settings, "defaultDispatchOccupancyNanos")
	raw, _ = json.Marshal(wire)
	if _, err := DecodeSchedulerConfiguration(string(raw), 1<<20); err == nil {
		t.Fatal("aggregation policy supplied an implicit scheduler default")
	}
	settings["defaultDispatchOccupancyNanos"] = float64(2000000)
	delete(wire, "dispatchOccupancy")
	delete(wire["settings"].(map[string]any), "defaultDispatchOccupancyNanos")
	raw, _ = json.Marshal(wire)
	if _, err := DecodeSchedulerConfiguration(string(raw), 1<<20); err == nil {
		t.Fatal("missing policy and missing settings fallback acquired an implicit zero")
	}
	wire["settings"].(map[string]any)["defaultDispatchOccupancyNanos"] = float64(0)
	raw, _ = json.Marshal(wire)
	config, err := DecodeSchedulerConfiguration(string(raw), 1<<20)
	if err != nil || config.DispatchOccupancy != nil {
		t.Fatal("missing policy acquired defaults", err)
	}
	for _, change := range []func(*measurement.DispatchOccupancyPolicy){
		func(p *measurement.DispatchOccupancyPolicy) { p.AggregationWindowSeconds = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.RefreshIntervalSeconds = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.ScanTimeoutSeconds = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.ScanTimeoutSeconds = p.RefreshIntervalSeconds/2 + 1 },
		func(p *measurement.DispatchOccupancyPolicy) { p.MaximumRequests = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.WarmupSeconds = -1 },
		func(p *measurement.DispatchOccupancyPolicy) { p.BurstSeconds = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.Workers = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.Workers = 65 },
		func(p *measurement.DispatchOccupancyPolicy) { p.MinimumPairs = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.AggregationWindowSeconds = p.BurstSeconds - 1 },
		func(p *measurement.DispatchOccupancyPolicy) { p.RequestLifetimeSeconds = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.DeliveryMarginMillis = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.PreparationSeconds = 0 },
		func(p *measurement.DispatchOccupancyPolicy) { p.JobSetupSeconds = -1 },
		func(p *measurement.DispatchOccupancyPolicy) { p.CollectionSeconds = -1 },
		func(p *measurement.DispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = 5 },
		func(p *measurement.DispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = math.NaN() },
		func(p *measurement.DispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = math.Inf(1) },
		func(p *measurement.DispatchOccupancyPolicy) { p.PlannedElapsedCapSeconds = math.MaxInt32 + 1 },
	} {
		p := syntheticOccupancyPolicy()
		change(&p)
		if p.Validate() == nil {
			t.Fatalf("accepted invalid policy %+v", p)
		}
	}
	if err := syntheticOccupancyPolicy().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOccupancyPolicyZeroInputsAreExplicitNotGoFallbacks(t *testing.T) {
	for _, field := range []string{
		"warmupSeconds", "jobSetupSeconds", "collectionSeconds", "seed",
	} {
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
			var policy measurement.DispatchOccupancyPolicy
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
		var policy measurement.DispatchOccupancyPolicy
		if err = json.Unmarshal(raw, &policy); err != nil {
			t.Fatal(err)
		}
		if err = policy.Validate(); err != nil {
			t.Fatal("explicit zero rejected", field, err)
		}
	}
}

func TestSchedulerSettingsPUTAndPATCHControlOccupancyDefaults(t *testing.T) {
	for _, tc := range []struct {
		name, method string
		scalar       int64
	}{
		{"PUT/scalar", "PUT", 5000000},
		{"PUT/zero", "PUT", 0},
		{"PATCH/scalar", "PATCH", 5000000},
		{"PATCH/zero", "PATCH", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile("../../../config/samples/serving-scheduler.json")
			if err != nil {
				t.Fatal(err)
			}
			var document map[string]json.RawMessage
			if err := json.Unmarshal(raw, &document); err != nil {
				t.Fatal(err)
			}
			var input serving.SchedulerSettingsInput
			if err := json.Unmarshal(document["settings"], &input); err != nil {
				t.Fatal(err)
			}
			current, err := input.Settings() // same settings boundary as public GET
			if err != nil {
				t.Fatal(err)
			}
			scalar := tc.scalar
			if tc.method == "PATCH" {
				patch, marshalErr := json.Marshal(map[string]int64{"defaultDispatchOccupancyNanos": scalar})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				current, err = serving.ValidateSchedulerSettingsPatch(current, patch)
			} else {
				input.DefaultDispatchOccupancyNanos = scalar
				current, err = input.Settings() // public PUT uses this exact decode boundary
			}
			if err != nil {
				t.Fatal(err)
			}
			// Public PUT replaces only settings in the otherwise unchanged ConfigMap.
			if document["settings"], err = json.Marshal(current); err != nil {
				t.Fatal(err)
			}
			if raw, err = json.Marshal(document); err != nil {
				t.Fatal(err)
			}
			configuration, err := DecodeSchedulerConfiguration(string(raw), 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			accepted := rollingOccupancyFixture()
			accepted.configuration, accepted.legacy = &configuration, nil
			var cache rollingOccupancyCache
			cache.configure(accepted)
			for _, selected := range cache.selected() {
				expected := scalar
				if int64(selected.PerRequest) != expected || selected.Provenance.Value != expected ||
					selected.Provenance.Source != "default" {
					t.Fatalf("public settings overwritten by aggregation block: %+v want %d", selected, expected)
				}
			}
		})
	}
}
