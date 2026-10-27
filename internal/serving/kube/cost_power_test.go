package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestPowerTrialProjectsSettlingExcessWithoutExtendingAvailability(t *testing.T) {
	for _, tc := range []struct {
		settling int32
		watts    float64
		eta      float64
		wall     float64
	}{{0, 10, 0, 20}, {60, 10, 0, 20}, {60, 11, 62, 82}, {60, 9.9, -6.2, 13.8}} {
		t.Run(fmt.Sprintf("settling-%d-watts-%g", tc.settling, tc.watts), func(t *testing.T) {
			contract, raw, policy, profile := powerTrialInputs(tc.settling, tc.watts)
			derived, err := transitioncost.Derive(contract, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			accepted, ok := derived.AcceptedObservation()
			if !ok {
				t.Fatalf("fixture not accepted: %+v", derived.Record())
			}
			if want := tc.watts * (2 + float64(tc.settling)); math.Abs(accepted.TransitionWallJoules()-want) > 1e-9 {
				t.Fatalf("retained wall integral = %g, want %g", accepted.TransitionWallJoules(), want)
			}
			rawBytes, err := transitioncost.EncodeRawRecord(raw)
			if err != nil {
				t.Fatal(err)
			}
			rawRef := evidencestore.ObjectIdentity{Store: "store", Key: "power/raw.json", Size: int64(len(rawBytes))}
			finalBytes, err := json.Marshal(struct {
				SchemaVersion string                                    `json:"schemaVersion"`
				RawEvidence   evidencestore.ObjectIdentity              `json:"rawEvidence"`
				Observation   transitioncost.FinalizedObservationRecord `json:"observation"`
			}{transitioncost.FinalizedRecordSchemaVersion, rawRef, derived.Record()})
			if err != nil {
				t.Fatal(err)
			}
			finalRef := evidencestore.ObjectIdentity{Store: "store", Key: "power/final.json", Size: int64(len(finalBytes))}
			evidence := CostEvidence{objects: &costTestObjects{objects: map[string][]byte{rawRef.Key: rawBytes, finalRef.Key: finalBytes}, opens: map[string]int{}}, maxBytes: 1 << 20}
			cost := serving.OperationCost{Node: costTestMeter.Node}
			if err := evidence.appendPowerTrial(context.Background(), finalRef, contract, policy, raw.PowerSource, []serving.Profile{profile}, &cost); err != nil {
				t.Fatal(err)
			}
			if len(cost.Trials) != 1 || cost.EnergyScope != "target-baseline-delta" || cost.SubtractedWatts == nil || *cost.SubtractedWatts != 10 {
				t.Fatalf("lost target baseline delta contract: %+v", cost)
			}
			trial := cost.Trials[0]
			if trial.Duration != 2*time.Second || math.Abs(trial.Joules-tc.eta) > 1e-9 {
				t.Fatalf("projected trial = %+v, want tau=2s eta=%g", trial, tc.eta)
			}
			// Both scheduler consumers restore target baseline over Duration only.
			wall := trial.Joules + *cost.SubtractedWatts*trial.Duration.Seconds()
			if math.Abs(wall-tc.wall) > 1e-9 {
				t.Fatalf("scheduler transition energy = %g, want %g", wall, tc.wall)
			}
		})
	}
}

func powerTrialInputs(settling int32, watts float64) (transitioncost.FrozenContract, transitioncost.RawObservation, transitioncost.Policy, serving.Profile) {
	at := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
	source := transitioncost.StateIdentity{DeviceType: "cpu", Model: "model", PowerMode: "high"}
	target := source
	target.PowerMode = "low"
	contract := transitioncost.FrozenContract{SourceState: source, TargetState: target, SettlingSeconds: settling,
		TargetIdleBaseline: transitioncost.IdleBaseline{EvidenceReference: "idle/low.json", State: target, MeanWallWatts: 10}}
	process := transitioncost.ProcessIdentity{ContainerName: "firstfloor", ContainerID: "container", StartedAt: at.Add(-time.Hour)}
	runtime := native.RuntimeMetadata{Runtime: "ort-cpu", RuntimeVersion: "fixture", ModelFiles: []string{"/model/model.onnx"}, CPUAffinity: []int{0}, IntraOpThreads: 1}
	before := transitioncost.NativeNodeObservation{NodeName: costTestMeter.Node.Name, NodeUID: costTestMeter.Node.UID, ReceivedAt: at,
		Instances: []transitioncost.NativeInstanceObservation{{Namespace: "measure", PodName: "serving", PodUID: "pod", ContainerID: process.ContainerID, StartedAt: process.StartedAt,
			Image: "fixture-image", ImageID: "fixture-image-id", CPURequestMilli: 1000, CPULimitMilli: 1000, MemoryRequestBytes: 128, MemoryLimitBytes: 128,
			State: native.State{At: at, Ready: true, Metadata: native.Metadata{Mode: "serving", ProfileID: "profile", Runtime: runtime}}}}}
	after := before
	after.ReceivedAt = at.Add(time.Duration(3+settling) * time.Second)
	bracket := func(seconds int) transitioncost.ReceiptTimeBracket {
		end := at.Add(time.Duration(seconds) * time.Second)
		return transitioncost.ReceiptTimeBracket{LastAbsentReceivedAt: end.Add(-10 * time.Millisecond), FirstPresentReceivedAt: end}
	}
	raw := transitioncost.RawObservation{ObservationStartedAt: at.Add(-time.Second), ObservationEndedAt: after.ReceivedAt.Add(time.Second),
		SourceReadBack:       transitioncost.StateReadBack{Fact: transitioncost.MappedFact{OccurredAt: at, MappedAt: at}, State: source},
		TransitionStart:      &transitioncost.OccurredFact{OccurredAt: at.Add(time.Second)},
		TargetReadBack:       &transitioncost.TargetStateReadBack{Fact: transitioncost.OccurredFact{OccurredAt: at.Add(2 * time.Second)}, State: target},
		MarkerBrackets:       transitioncost.TransitionMarkerBrackets{ApplyStartedAt: bracket(1), TargetObservation: bracket(2), ReadyProcessWitness: bracket(3)},
		PreTransitionProcess: &transitioncost.ProcessObservation{ObservedAt: at, Process: process}, PostTransitionProcess: &process,
		BeforeNative: &before, AfterNative: &after, PowerSource: costTestMeter.Source}
	for second := 1; second <= 3+int(settling); second++ {
		raw.PowerSamples = append(raw.PowerSamples, transitioncost.PowerSample{ObservedAt: at.Add(time.Duration(second) * time.Second), Watts: watts})
	}
	policy := transitioncost.Policy{MaximumBoundaryDistance: time.Second, MinimumCoverage: .95, MaximumSampleGap: 2 * time.Second,
		MaximumBoundaryUncertainty: 50 * time.Millisecond, MaximumReadyObservationDelay: 3 * time.Second}
	profile := serving.Profile{ID: "profile", RuntimeMetadata: &runtime, Condition: derivation.CurvePlanSpec{NodeUID: before.NodeUID, RuntimeImage: "fixture-image",
		CPU: derivation.CurveCPUContract{Request: "1", Limit: "1"}, Artifact: derivation.CurveArtifactContract{Files: []string{"model.onnx"}}}}
	return contract, raw, policy, profile
}
