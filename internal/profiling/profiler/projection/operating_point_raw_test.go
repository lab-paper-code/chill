package projection

import (
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"math"
	"testing"
	"time"
)

func TestSyntheticRawOperatingPointsKeepAllServiceAndLadderEnergy(t *testing.T) {
	config := projectionRunConfig()
	start := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	raw := profilerevidence.RawMeasurement{RunConfig: config, Runtime: profilingrun.RuntimeResult{InferenceCount: 3, MeasurementStartedAt: start.Format(time.RFC3339Nano), MeasurementEndedAt: start.Add(32 * time.Second).Format(time.RFC3339Nano)}}
	for index, phase := range []struct {
		kind    string
		seconds int
		watts   float64
	}{{"sparse", 0, 200}, {"saturation", 11, 100}, {"utilization", 22, 30}} {
		from := start.Add(time.Duration(phase.seconds) * time.Second)
		to := from.Add(10 * time.Second)
		gap := int64(2000)
		pair := firstfloor.ServiceSample{TrialID: "physical-pod", ServiceDurationNanos: 1000000, PrecedingIdleNanos: &gap}
		if index == 0 {
			pair.PrecedingIdleNanos = nil
		}
		raw.Runtime.Phases = append(raw.Runtime.Phases, profilingrun.Phase{ID: phase.kind, Kind: phase.kind, MeasurementStartedAt: from.Format(time.RFC3339Nano), MeasurementEndedAt: to.Format(time.RFC3339Nano), CompletedInputs: 2, LatencyMilliseconds: []float64{2}, NativeSamples: []profilingrun.NativeServiceSample{{BatchID: uint64(index + 1), BatchSize: 2, RecordIDs: []string{"r1", "r2"}, Units: []int{1, 1}, ServiceSample: pair, RunnerServiceMilliseconds: 1, BackendCallMilliseconds: .5, NativeRequestMilliseconds: 1.5}}})
		raw.Metrics.Samples = append(raw.Metrics.Samples, metricobserver.Sample{ObservedAt: from, Watts: phase.watts}, metricobserver.Sample{ObservedAt: to, Watts: phase.watts})
	}
	execution := profilerevidence.ExecutionEvidence{RunConfig: config, Terminal: profilerevidence.TerminalFacts{Pod: profilerevidence.PodTerminalFacts{UID: "physical-pod"}, Node: profilerevidence.NodeTerminalFacts{UID: config.TargetNode.UID}}}
	trial, err := projectTrial(execution, raw)
	if err != nil {
		t.Fatal(err)
	}
	energy, err := profilederivation.IncrementalWorkTrial(trial, profilederivation.IdleBaseline{ModelLoadedIdleMeanWallWatts: 10})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(energy.IncrementalWorkJoulesPerItem-100) > 1e-9 {
		t.Fatalf("EPI=%g, want ladder-only 100", energy.IncrementalWorkJoulesPerItem)
	}
	if len(energy.NativeSamples) != 3 || len(energy.PhasePower) != 3 {
		t.Fatalf("lost phase service/power: %+v", energy)
	}
	facts := profilederivation.MeasurementPointFacts(profilederivation.Point{Batch: 2, Trials: []profilederivation.EnergyTrial{energy}}, profilederivation.IdleBaseline{}, []float64{2, 2, 2}, 6, 3)
	if len(facts.PhasePower) != 3 {
		t.Fatalf("catalog lost phase power: %+v", facts)
	}
	for index, want := range []float64{200, 100, 30} {
		if facts.PhasePower[index].MeanWallWatts != want || facts.PhasePower[index].TrialID != "physical-pod" || energy.NativeSamples[index].BatchID != uint64(index+1) {
			t.Fatalf("phase %d lost raw population: %+v", index, facts)
		}
	}
	if energy.NativeSamples[0].PrecedingIdleNanos != nil || *energy.NativeSamples[2].PrecedingIdleNanos != 2000 {
		t.Fatal("preceding idle pairs changed")
	}
}
