// Package fixtures holds captured-shape inputs shared by contract consumers.
// It is not a runtime fallback or a source of production cadence defaults.
package fixtures

import (
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

func Cadence() *firstfloor.CadenceContract {
	start := time.Date(2026, time.November, 5, 0, 0, 0, 0, time.UTC)
	return &firstfloor.CadenceContract{
		Target: firstfloor.CadenceTarget{LoadContext: "captured-shared-load", Boundary: firstfloor.NativeExchangeGapBoundary,
			ConditionalModel: firstfloor.ConditionalServiceModel,
			Schedule: []firstfloor.CadenceTargetPhase{{
				Name: "active", OfferedLoad: "captured-requests", SharingContext: "shared-gateway",
			}}},
		Provenance: firstfloor.CadenceProvenance{Seed: 7, DrawRule: firstfloor.CadenceDrawRule,
			Sources: []firstfloor.CadenceSource{{
				Object:   firstfloor.CadenceObject{Store: "raw", Key: "captured-native-exchanges", Size: 1024},
				Boundary: firstfloor.NativeExchangeGapBoundary, SourceRevision: "captured-fixture",
				LoadContext: "captured-shared-load", Workload: "fixture", NodeUID: "donor-node",
				ExecutionPath: "ort-cpu", PowerMode: "default", PhysicalInstanceID: "donor-instance", TrialID: "donor-trial",
				ObservedBatch: 1, WindowStart: start, WindowEnd: start.Add(time.Minute), Borrowed: true}},
			Phases: []firstfloor.CadencePhase{{Name: "active", Elements: []firstfloor.CadenceGapElement{
				{GapNanos: 1_000_000, SourceIndex: 0, BatchID: 2}, {GapNanos: 100_000_000, SourceIndex: 0, BatchID: 3},
			}}},
			Draws: []firstfloor.CadenceElementRef{{Phase: "active", ElementIndex: 0}, {Phase: "active", ElementIndex: 1}},
		},
	}
}

func Cadences(batches ...int) map[int]*firstfloor.CadenceContract {
	body := Cadence()
	cadences := make(map[int]*firstfloor.CadenceContract, len(batches))
	for _, batch := range batches {
		cadences[batch] = body.Clone()
	}
	return cadences
}

func ServiceSample(
	cadence *firstfloor.CadenceContract, trialID string, callIndex uint64, service int64,
) firstfloor.ServiceSample {
	sample := firstfloor.ServiceSample{ServiceDurationNanos: service, TrialID: trialID}
	if callIndex > 0 {
		draw := cadence.Draw(callIndex - 1)
		actual := draw.TargetIdleNanos + 1000
		sample.PrecedingIdleNanos, sample.TargetIdleNanos, sample.TraceDrawRef = &actual, &draw.TargetIdleNanos, &draw.Ref
	}
	return sample
}
