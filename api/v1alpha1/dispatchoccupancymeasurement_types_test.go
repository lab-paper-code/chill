package v1alpha1_test

import (
	"strings"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestOccupancyFrozenSupplySizing(t *testing.T) {
	legacy := edge.DispatchOccupancyMeasurementSpec{
		Batch: 2, Workers: 12, SupplyMode: "closed-loop", MinimumBatches: 2,
		MaximumIdleFraction: "0.01", WarmupSeconds: 60, DurationSeconds: 360,
		RequestLifetimeSeconds: 5, DeliveryMarginMillis: 10, DrainSeconds: 6,
		PreparationSeconds: 60, JobSetupSeconds: 30, CollectionSeconds: 30, PlannedElapsedSeconds: 600,
		Conditions: edge.DispatchOccupancyConditions{
			ProfileID: "profile", WaitingBatches: 5, MinimumCycles: 1, MaximumIdleNanosPerRequest: 1000000,
			InputsJSON: `{"coefficients":{"windowFloorSeconds":360,"loadPreparationFloorSeconds":60,"plannedElapsedCapSeconds":3600},"computed":{"tailBatchSeconds":0.1,"fastBatchSeconds":0.02,"refillSeconds":0.1,"prefixSeconds":0.1,"framedDecodedRequestUpperBytes":32,"queueRequestCap":256,"queueBytesCap":67108864,"qUpper":40}}`,
		},
	}
	if err := legacy.ValidateConditions(); err != nil {
		t.Fatalf("legacy frozen inputs: %v", err)
	}
	larger := legacy
	larger.Workers, larger.Conditions.WaitingBatches = 16, 7
	if larger.ValidateConditions() == nil {
		t.Fatal("accepted increased supply without frozen sizing")
	}
	const sizing = `"supplySizing":{"previousWorkers":12,"profileThroughputRequestsPerSecond":100,"batch":2,"producerOutsideTime":{"p99Seconds":0.1,"sources":[],"fallback":true,"fallbackReason":"explicit fixture outside-time fallback"},"margin":1.25,"minimumWorkers":15,"wholeBatchWorkers":16},`
	larger.Conditions.InputsJSON = strings.Replace(legacy.Conditions.InputsJSON, `"tailBatchSeconds":`, sizing+`"tailBatchSeconds":`, 1)
	if err := larger.ValidateConditions(); err != nil {
		t.Fatalf("whole-batch ceil(rate*outside*1.25)+B supply: %v", err)
	}
	for _, tc := range []struct {
		name string
		old  string
		new  string
	}{
		{"previous supply", `"previousWorkers":12`, `"previousWorkers":10`},
		{"minimum supply missing batch reserve", `"minimumWorkers":15`, `"minimumWorkers":13`},
		{"whole batch supply", `"wholeBatchWorkers":16`, `"wholeBatchWorkers":15`},
		{"throughput mismatch", `"profileThroughputRequestsPerSecond":100`, `"profileThroughputRequestsPerSecond":200`},
		{"zero throughput", `"profileThroughputRequestsPerSecond":100`, `"profileThroughputRequestsPerSecond":0`},
		{"outside mismatch", `"p99Seconds":0.1`, `"p99Seconds":0.2`},
		{"negative outside", `"p99Seconds":0.1`, `"p99Seconds":-0.1`},
		{"wrong margin", `"margin":1.25`, `"margin":1`},
		{"wrong batch", `"batch":2`, `"batch":1`},
		{"no provenance", `"fallback":true`, `"fallback":false`},
		{"no fallback reason", `"fallbackReason":"explicit fixture outside-time fallback"`, `"fallbackReason":""`},
		{"queue cap", `"queueRequestCap":256`, `"queueRequestCap":13`},
		{"byte cap", `"queueBytesCap":67108864`, `"queueBytesCap":447`},
		{"q upper", `"qUpper":40`, `"qUpper":6`},
		{"lifetime bound", `"tailBatchSeconds":0.1`, `"tailBatchSeconds":1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := larger
			bad.Conditions.InputsJSON = strings.Replace(bad.Conditions.InputsJSON, tc.old, tc.new, 1)
			if bad.ValidateConditions() == nil {
				t.Fatal("accepted inconsistent frozen sizing")
			}
		})
	}
	t.Run("actual workers must match", func(t *testing.T) {
		bad := larger
		bad.Workers, bad.Conditions.WaitingBatches = 18, 8
		if bad.ValidateConditions() == nil {
			t.Fatal("accepted workers above the frozen formula")
		}
	})
	t.Run("raw source provenance", func(t *testing.T) {
		sourced := larger
		sourced.Conditions.InputsJSON = strings.Replace(sourced.Conditions.InputsJSON, `"sources":[],"fallback":true,"fallbackReason":"explicit fixture outside-time fallback"`, `"sources":[{"path":"producer.jsonl","sha256":"fixture"}],"fallback":false`, 1)
		if err := sourced.ValidateConditions(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("legacy refill remains lower bound", func(t *testing.T) {
		low := legacy
		low.Conditions.InputsJSON = strings.NewReplacer(
			`"profileThroughputRequestsPerSecond":100`, `"profileThroughputRequestsPerSecond":1`,
			`"minimumWorkers":15`, `"minimumWorkers":3`,
			`"wholeBatchWorkers":16`, `"wholeBatchWorkers":12`,
		).Replace(larger.Conditions.InputsJSON)
		if err := low.ValidateConditions(); err != nil {
			t.Fatal(err)
		}
		low.Workers, low.Conditions.WaitingBatches = 4, 1
		low.Conditions.InputsJSON = strings.Replace(low.Conditions.InputsJSON, `"wholeBatchWorkers":12`, `"wholeBatchWorkers":4`, 1)
		if low.ValidateConditions() == nil {
			t.Fatal("accepted supply below the legacy refill floor")
		}
	})
}
