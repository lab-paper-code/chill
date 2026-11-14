package v1alpha1_test

import (
	"strings"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
)

func burstSpec() edge.DispatchOccupancyMeasurementSpec {
	return edge.DispatchOccupancyMeasurementSpec{
		Batch: 2, Workers: 8, SupplyMode: "closed-loop", MinimumPairs: 100,
		WarmupSeconds: 5, DurationSeconds: 15, RequestLifetimeSeconds: 2, DeliveryMarginMillis: 10,
		DrainSeconds: 3, PreparationSeconds: 10, JobSetupSeconds: 2, CollectionSeconds: 2, PlannedElapsedSeconds: 40,
		Conditions: edge.DispatchOccupancyConditions{ProfileID: "profile", WaitingBatches: 3,
			InputsJSON: `{"coefficients":{"warmupSeconds":5,"burstSeconds":15,"workers":8,"minimumPairs":100,"plannedElapsedCapSeconds":60},"computed":{"tailBatchSeconds":0.1,"framedDecodedRequestUpperBytes":32,"queueRequestCap":256,"queueBytesCap":67108864}}`},
	}
}

func TestBurstFrozenConfigurationAndSafety(t *testing.T) {
	spec := burstSpec()
	if err := spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*edge.DispatchOccupancyMeasurementSpec){
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.Workers++ },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.MinimumPairs++ },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.DurationSeconds++ },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.WarmupSeconds++ },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.Conditions.WaitingBatches++ },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.RequestLifetimeSeconds = 0 },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.DrainSeconds = 1 },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.PlannedElapsedSeconds = 39 },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.SupplyMode = "poisson" },
		func(s *edge.DispatchOccupancyMeasurementSpec) { s.Conditions.InputsJSON = `[]` },
		func(s *edge.DispatchOccupancyMeasurementSpec) {
			s.Conditions.InputsJSON = strings.Replace(s.Conditions.InputsJSON, `"queueRequestCap":256`, `"queueRequestCap":5`, 1)
		},
		func(s *edge.DispatchOccupancyMeasurementSpec) {
			s.Conditions.InputsJSON = strings.Replace(s.Conditions.InputsJSON, `"tailBatchSeconds":0.1`, `"tailBatchSeconds":1`, 1)
		},
	} {
		bad := spec
		change(&bad)
		if bad.ValidateConditions() == nil {
			t.Fatalf("accepted infeasible burst: %+v", bad)
		}
	}
}

func TestLegacyIdleFieldsDoNotGateBurst(t *testing.T) {
	spec := burstSpec()
	spec.MaximumIdleFraction = "not-a-number"
	spec.MinimumBatches = 99999
	spec.Conditions.MaximumIdleNanosPerRequest = 1
	spec.Conditions.MinimumCycles = 99999
	if err := spec.ValidateConditions(); err != nil {
		t.Fatalf("legacy evidence field gated a burst: %v", err)
	}
}
