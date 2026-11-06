package kube

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestPowerTrialProjectsSettlingExcessWithoutExtendingAvailability(t *testing.T) {
	for _, tc := range []struct {
		settling int32
		watts    float64
		eta      float64
		wall     float64
	}{{0, 10, 0, 20}, {60, 10, 0, 20}, {60, 11, 62, 82}, {60, 9.9, -6.2, 13.8}} {
		t.Run(fmt.Sprintf("settling-%d-watts-%g", tc.settling, tc.watts), func(t *testing.T) {
			contract, raw, policy := powerTrialInputs(tc.settling, tc.watts)
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
			stored := derived.Record()
			stored.FrozenContract.TargetIdleBaseline.EvidenceReference = "archive/idle.json"
			stored.AcceptedObservation.EtaJoules += 0.125
			finalBytes, err := json.Marshal(struct {
				SchemaVersion string                                    `json:"schemaVersion"`
				RawEvidence   evidencestore.ObjectIdentity              `json:"rawEvidence"`
				Observation   transitioncost.FinalizedObservationRecord `json:"observation"`
			}{transitioncost.FinalizedRecordSchemaVersion, rawRef, stored})
			if err != nil {
				t.Fatal(err)
			}
			finalRef := evidencestore.ObjectIdentity{Store: "store", Key: "power/final.json", Size: int64(len(finalBytes))}
			evidence := CostEvidence{objects: &costTestObjects{objects: map[string][]byte{rawRef.Key: append(rawBytes, '\n'), finalRef.Key: append(finalBytes, '\n')}, opens: map[string]int{}}, maxBytes: 1 << 20}
			cost := serving.OperationCost{Node: costTestMeter.Node}
			if err := evidence.appendPowerTrial(context.Background(), finalRef, contract, policy, &cost); err != nil {
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

func powerTrialInputs(settling int32, watts float64) (transitioncost.FrozenContract, transitioncost.RawObservation, transitioncost.Policy) {
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
			Image: "fixture-image", ImageID: "fixture-image-id", MemoryRequestBytes: 128, MemoryLimitBytes: 128,
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
	return contract, raw, policy
}

func TestCostEvidenceLoadsPowerWithRetainedMeterAndNoCurrentProfile(t *testing.T) {
	objects := &costTestObjects{objects: map[string][]byte{}, opens: map[string]int{}}
	put := func(key string, value any) evidencestore.ObjectIdentity {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		objects.objects[key] = payload
		return evidencestore.ObjectIdentity{Store: "store", Key: key, Size: int64(len(payload))}
	}
	var contracts []transitioncost.FrozenContract
	var directions []transitioncost.DirectedStatePair
	var observations []transitioncost.FinalizedObservation
	var finalDirections []map[string]any
	for direction := range 2 {
		var finals []evidencestore.ObjectIdentity
		for trial := range 2 {
			contract, raw, policy := powerTrialInputs(0, 11)
			if direction == 1 {
				contract.SourceState, contract.TargetState = contract.TargetState, contract.SourceState
				contract.TargetIdleBaseline.State = contract.TargetState
				raw.SourceReadBack.State, raw.TargetReadBack.State = contract.SourceState, contract.TargetState
			}
			raw.BeforeNative.Instances[0].PodUID = fmt.Sprintf("physical-%d-%d", direction, trial)
			raw.AfterNative.Instances[0].PodUID = raw.BeforeNative.Instances[0].PodUID
			observation, err := transitioncost.Derive(contract, raw, policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, accepted := observation.AcceptedObservation(); !accepted {
				t.Fatalf("physical power fixture failed: %+v", observation.Record())
			}
			observations = append(observations, observation)
			payload, err := transitioncost.EncodeRawRecord(raw)
			if err != nil {
				t.Fatal(err)
			}
			key := fmt.Sprintf("power/%d/%d/raw.json", direction, trial)
			objects.objects[key] = payload
			rawRef := evidencestore.ObjectIdentity{Store: "store", Key: key, Size: int64(len(payload))}
			finals = append(finals, put(fmt.Sprintf("power/%d/%d/final.json", direction, trial), map[string]any{
				"schemaVersion": transitioncost.FinalizedRecordSchemaVersion, "rawEvidence": rawRef, "observation": observation.Record(),
			}))
			if trial == 0 {
				contracts = append(contracts, contract)
				directions = append(directions, transitioncost.DirectedStatePair{Source: contract.SourceState, Target: contract.TargetState})
			}
		}
		finalDirections = append(finalDirections, map[string]any{"acceptedCount": 2, "finalEvidence": finals})
	}
	table, err := transitioncost.Summarize(directions, 2, observations)
	if err != nil {
		t.Fatal(err)
	}
	result := put("power/result.json", map[string]any{
		"schemaVersion": transitioncost.MeasurementResultSchemaVersion, "outcome": "Complete",
		"directions": finalDirections, "directedTable": table.Record(),
	})
	_, _, policy := powerTrialInputs(0, 11)
	preparation, err := json.Marshal(map[string]any{
		"schemaVersion": transitioncost.PreparationRecordSchemaVersion,
		"prepared": map[string]any{"targetNode": costTestMeter.Node, "powerSource": costTestMeter.Source, "policy": policy,
			"directions": []map[string]any{{"frozenContract": contracts[0]}, {"frozenContract": contracts[1]}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	measurement := &edge.PowerModeTransitionCost{
		ObjectMeta: meta.ObjectMeta{Namespace: "measurements", Name: "power", UID: "power-uid", Generation: 1},
		Spec:       edge.PowerModeTransitionCostSpec{TargetNode: costTestMeter.Node.Name, AcceptedCountTarget: 2},
		Status: edge.PowerModeTransitionCostStatus{ObservedGeneration: 1, Phase: edge.PowerModeTransitionCostPhaseComplete,
			Result: &edge.ProfilingRunEvidenceReference{Store: result.Store, Key: result.Key, Size: result.Size}},
	}
	immutable := true
	prepared := &core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: measurement.Namespace, Name: measurement.Name,
		OwnerReferences: []meta.OwnerReference{{UID: measurement.UID, Kind: "PowerModeTransitionCost"}}},
		Immutable: &immutable, BinaryData: map[string][]byte{"preparation.json": preparation}}
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader, err := NewCostEvidence(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(measurement, prepared).Build()}, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	view, err := reader.Refresh(context.Background(), []string{measurement.Namespace}, nil)
	if err != nil || len(view.Issues) != 0 || len(view.Costs) != 2 {
		t.Fatalf("retained physical power trials required current metadata: %+v %v", view, err)
	}
	for _, cost := range view.Costs {
		if cost.Meter != costTestMeter.Source.Endpoint || cost.Scope != servingstate.MeasurementWallScope ||
			len(cost.Trials) != 2 || cost.Trials[0].Duration != 2*time.Second || cost.Trials[0].Joules != 2 {
			t.Fatalf("retained provenance or directed numeric cost lost: %+v", cost)
		}
	}
}
