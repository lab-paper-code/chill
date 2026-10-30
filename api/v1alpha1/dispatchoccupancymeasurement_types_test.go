package v1alpha1_test

import (
	"strings"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestMeasurementPointConfidenceDefaultAndRange(t *testing.T) {
	for _, value := range []string{"", "0.95", "0.8"} {
		got, err := (edge.BatchEnergyOptimizationPointPolicySpec{ConfidenceLevel: value}).Confidence()
		want := .95
		if value == "0.8" {
			want = .8
		}
		if err != nil || got != want {
			t.Fatalf("confidence %q = %g, %v", value, got, err)
		}
	}
	for _, value := range []string{"0", "1", "NaN", "Inf", " 0.95"} {
		if _, err := (edge.BatchEnergyOptimizationPointPolicySpec{ConfidenceLevel: value}).Confidence(); err == nil {
			t.Fatalf("invalid confidence accepted: %q", value)
		}
	}
}

func TestOpenLoopOccupancyUsesConfiguredWindowAndRate(t *testing.T) {
	spec := edge.DispatchOccupancyMeasurementSpec{Batch: 1, Workers: 1, MinimumBatches: 2,
		MaximumIdleFraction: "0.01", DurationSeconds: 30, RateFactor: "1.01"}
	if err := spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"1", "NaN", "Inf"} {
		spec.RateFactor = value
		if spec.ValidateConditions() == nil {
			t.Fatalf("invalid open-loop rate accepted: %s", value)
		}
	}
}

func TestOccupancyFrozenSupplySizing(t *testing.T) {
	legacy := edge.DispatchOccupancyMeasurementSpec{
		Batch: 2, Workers: 12, SupplyMode: "closed-loop", MinimumBatches: 2,
		MaximumIdleFraction: "0.01", WarmupSeconds: 60, DurationSeconds: 360,
		RequestLifetimeSeconds: 5, DeliveryMarginMillis: 10, DrainSeconds: 6,
		PreparationSeconds: 60, JobSetupSeconds: 30, CollectionSeconds: 30, PlannedElapsedSeconds: 600,
		Conditions: edge.DispatchOccupancyConditions{
			ProfileID: "profile", WaitingBatches: 5, MinimumCycles: 1, MaximumIdleNanosPerRequest: 1000000,
			InputsJSON: `{"coefficients":{"windowFloorSeconds":360,"loadPreparationFloorSeconds":60,"plannedElapsedCapSeconds":3600},"profile":{"batch":2,"meanSeconds":0.05,"maxObservedSeconds":0.1},"computed":{"tailBatchSeconds":0.1,"fastBatchSeconds":0.02,"refillSeconds":0.1,"prefixSeconds":0.1,"framedDecodedRequestUpperBytes":32,"queueRequestCap":256,"queueBytesCap":67108864,"qUpper":40}}`,
		},
	}
	if err := legacy.ValidateConditions(); err != nil {
		t.Fatalf("legacy frozen inputs: %v", err)
	}
	capped := legacy
	capped.Workers, capped.Conditions.WaitingBatches = 24, 11
	if capped.ValidateConditions() == nil {
		t.Fatal("accepted changed supply without frozen sizing")
	}
	const sizing = `"supplySizing":{"batch":2,"profileMaxBatchSeconds":0.1,"serviceTimeSource":"profile.maxObservedSeconds","requestLifetimeSeconds":5,"marginSeconds":0.01,"workerCap":64,"queueBytesCap":67108864,"framedDecodedRequestUpperBytes":32,"limitingBound":"lifetime"},`
	capped.Conditions.InputsJSON = strings.Replace(legacy.Conditions.InputsJSON, `"tailBatchSeconds":`, sizing+`"tailBatchSeconds":`, 1)
	if err := capped.ValidateConditions(); err != nil {
		t.Fatalf("lifetime-capped supply: %v", err)
	}
	for _, tc := range []struct{ name, old, new string }{
		{"batch", `"batch":2`, `"batch":3`},
		{"service equality", `"profileMaxBatchSeconds":0.1`, `"profileMaxBatchSeconds":0.11`},
		{"zero service", `"profileMaxBatchSeconds":0.1`, `"profileMaxBatchSeconds":0`},
		{"missing maximum", `"maxObservedSeconds":0.1`, `"unused":0.1`},
		{"missing sizing maximum", `"profileMaxBatchSeconds":0.1`, `"profileMeanBatchSeconds":0.1`},
		{"missing source", `"serviceTimeSource":"profile.maxObservedSeconds"`, `"serviceTimeSource":""`},
		{"mean is not a tail source", `"serviceTimeSource":"profile.maxObservedSeconds"`, `"serviceTimeSource":"profile.meanSeconds"`},
		{"lifetime equality", `"requestLifetimeSeconds":5`, `"requestLifetimeSeconds":6`},
		{"margin equality", `"marginSeconds":0.01`, `"marginSeconds":0.02`},
		{"worker cap", `"workerCap":64`, `"workerCap":65`},
		{"bytes cap", `"queueBytesCap":67108864`, `"queueBytesCap":67108863`},
		{"request bytes equality", `"framedDecodedRequestUpperBytes":32`, `"framedDecodedRequestUpperBytes":33`},
		{"limiting bound", `"limitingBound":"lifetime"`, `"limitingBound":"queue-cap"`},
		{"queue count", `"queueRequestCap":256`, `"queueRequestCap":21`},
		{"q upper", `"qUpper":40`, `"qUpper":10`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := capped
			bad.Conditions.InputsJSON = strings.Replace(bad.Conditions.InputsJSON, tc.old, tc.new, 1)
			if bad.ValidateConditions() == nil {
				t.Fatal("accepted inconsistent frozen sizing")
			}
		})
	}
	for _, tc := range []struct {
		name             string
		mean             string
		bytes            string
		bound            string
		workers, waiting int32
	}{
		{"non batch multiple", "0.13", "32", "lifetime", 19, 9},
		{"one full batch", "1", "32", "lifetime", 2, 0},
		{"fixed cap", "0.01", "32", "queue-cap", 64, 31},
		{"bytes cap", "0.01", "3000000", "bytes-cap", 22, 10},
		{"queue bytes tie", "0.01", "1048576", "queue-cap", 64, 31},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := capped
			spec.Workers, spec.Conditions.WaitingBatches = tc.workers, tc.waiting
			spec.Conditions.InputsJSON = strings.NewReplacer(
				`"maxObservedSeconds":0.1`, `"maxObservedSeconds":`+tc.mean,
				`"profileMaxBatchSeconds":0.1`, `"profileMaxBatchSeconds":`+tc.mean,
				`"framedDecodedRequestUpperBytes":32`, `"framedDecodedRequestUpperBytes":`+tc.bytes,
				`"limitingBound":"lifetime"`, `"limitingBound":"`+tc.bound+`"`,
			).Replace(spec.Conditions.InputsJSON)
			if err := spec.ValidateConditions(); err != nil {
				t.Fatal(err)
			}
			spec.Workers++
			if spec.ValidateConditions() == nil {
				t.Fatal("accepted workers above formula")
			}
			spec.Workers--
			spec.Conditions.WaitingBatches++
			if spec.ValidateConditions() == nil {
				t.Fatal("accepted incorrect waiting batches")
			}
		})
	}
	t.Run("less than one batch is infeasible", func(t *testing.T) {
		bad := capped
		bad.Workers, bad.Conditions.WaitingBatches = 1, 0
		bad.Conditions.InputsJSON = strings.NewReplacer(`"maxObservedSeconds":0.1`, `"maxObservedSeconds":2`, `"profileMaxBatchSeconds":0.1`, `"profileMaxBatchSeconds":2`).Replace(bad.Conditions.InputsJSON)
		if bad.ValidateConditions() == nil {
			t.Fatal("accepted fewer workers than one batch")
		}
	})
	t.Run("new sizing retains lifetime despite legacy tail bound", func(t *testing.T) {
		spec := capped
		spec.DrainSeconds = 7
		spec.Conditions.InputsJSON = strings.Replace(spec.Conditions.InputsJSON, `"tailBatchSeconds":0.1`, `"tailBatchSeconds":1`, 1)
		if err := spec.ValidateConditions(); err != nil {
			t.Fatal(err)
		}
	})
}
