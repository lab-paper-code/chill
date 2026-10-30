package profilederivation

import (
	"encoding/json"
	"math"
	"reflect"
	"testing"
)

func TestStudentT95UsesExactCriticalValueBeyondLegacyTable(t *testing.T) {
	const want = 2.0395134463964077
	if got := studentTCritical(0.95, 31); math.Abs(got-want) > 1e-12 {
		t.Fatalf("studentTCritical(0.95, 31) = %.15f, want %.15f", got, want)
	}
}

func TestPercentileUsesEmpiricalNearestRank(t *testing.T) {
	values := []float64{4, 1, 3, 2}
	for _, test := range []struct {
		quantile float64
		want     float64
	}{
		{quantile: 0, want: 1},
		{quantile: 0.50, want: 2},
		{quantile: 0.75, want: 3},
		{quantile: 1, want: 4},
	} {
		if got := percentile(values, test.quantile); got != test.want {
			t.Errorf("percentile(%v, %.2f) = %v, want %v", values, test.quantile, got, test.want)
		}
	}
}

func TestCurveStatisticsPoolsClientCallsWithinEachBatch(t *testing.T) {
	_, plan := testVerifiedCurvePlan(t, 2)
	pass := testPass(t, plan, []float64{1, 2}, "statistics")
	vectors := [][]float64{make([]float64, 100), {10}, {20}}
	for index := range vectors[0] {
		vectors[0][index] = 1
	}
	for index := range pass.Batches[1].Trials {
		trial := &pass.Batches[1].Trials[index]
		trial.LatencyMilliseconds = vectors[index]
		trial.CompletedCalls = len(vectors[index])
		trial.CompletedItems = trial.Batch * trial.CompletedCalls
	}
	// Baseline map keys are lookup labels, not an additional identity gate.
	baselines := testCurveBaselines(t, plan)
	baseline := baselines[plan.state.StateIdentity]
	fourthIdle := baseline.Trials[0]
	fourthIdle.TrialID = "idle-fourth"
	fourthIdle.RawEvidence = rawEvidence("idle-fourth")
	baseline, err := DeriveIdleBaseline(append(baseline.Trials, fourthIdle), pointPolicy("statistics"))
	if err != nil {
		t.Fatal(err)
	}
	curve, err := DeriveCurve([]Pass{pass}, plan, map[string]IdleBaseline{
		"shared-baseline": baseline,
	}, pointPolicy("statistics"))
	if err != nil {
		t.Fatal(err)
	}
	before := curve.Points()
	rows := curve.Statistics()
	if len(rows) != 2 || rows[0].Batch != 1 || rows[1].Batch != 2 {
		t.Fatalf("batch rows = %+v", rows)
	}
	if rows[0].LatencySampleCount != 3 || rows[0].LatencyP95Milliseconds != 1 {
		t.Fatalf("another batch's samples leaked into batch 1: %+v", rows[0])
	}
	got := rows[1]
	if got.ActiveTrialCount != 3 || got.IdleTrialCount != 4 ||
		got.LatencySampleCount != 102 || got.LatencyP95Milliseconds != 1 {
		t.Fatalf("expected 3 trials, 102 client calls (204 items), pooled p95 1ms: %+v", got)
	}
	trialQuantileMean := (percentile(vectors[0], .95) +
		percentile(vectors[1], .95) + percentile(vectors[2], .95)) / 3
	if got.LatencyP95Milliseconds == trialQuantileMean ||
		got.LatencyP95Milliseconds == before[1].LatencyP99Milliseconds {
		t.Fatalf("pooled p95 was replaced by a trial summary: %+v", got)
	}
	if got.IncrementalWorkJoulesPerItem != before[1].MeanIncrementalWorkJoulesPerItem ||
		!reflect.DeepEqual(got.Confidence, before[1].Confidence) {
		t.Fatal("summary changed the existing mean or confidence")
	}
	if !reflect.DeepEqual(curve.Points(), before) {
		t.Fatal("summary mutated the derived curve")
	}
}

func TestCurveStatisticsCopiesConfidenceAndSerializes(t *testing.T) {
	for _, nonzero := range []bool{false, true} {
		name := "zero variance"
		if nonzero {
			name = "shared idle variance with nondefault confidence"
		}
		t.Run(name, func(t *testing.T) {
			_, plan := testVerifiedCurvePlan(t, 1)
			policy := pointPolicy("statistics-confidence")
			policy.ConfidenceLevel = .8
			idleTrials := testCurveBaselines(t, plan)[plan.state.StateIdentity].Trials
			if nonzero {
				idleTrials[0].ModelLoadedIdleMeanWallWatts = .4
				idleTrials[2].ModelLoadedIdleMeanWallWatts = 1.6
			}
			baseline, err := DeriveIdleBaseline(idleTrials, policy)
			if err != nil {
				t.Fatal(err)
			}
			curve, err := DeriveCurve(
				[]Pass{testPass(t, plan, []float64{.01}, "confidence")},
				plan, map[string]IdleBaseline{plan.state.StateIdentity: baseline}, policy,
			)
			if err != nil {
				t.Fatal(err)
			}
			point := curve.Points()[0]
			rows := curve.Statistics()
			got := rows[0]
			if got.IncrementalWorkJoulesPerItem != point.MeanIncrementalWorkJoulesPerItem ||
				!reflect.DeepEqual(got.Confidence, point.Confidence) || got.Confidence.Level != .8 {
				t.Fatalf("existing estimator was changed: %+v, point %+v", got, point)
			}
			if got.Confidence.VarianceOfMean == nil {
				t.Fatal("variance, including observed zero, must be retained")
			}
			if nonzero {
				if point.IncrementalWorkSampleVariance != 0 ||
					*got.Confidence.VarianceOfMean <= 0 ||
					got.Confidence.EffectiveDegreesOfFreedom == nil ||
					got.IncrementalWorkJoulesPerItem-got.Confidence.HalfWidth >= 0 {
					t.Fatalf("shared idle variance or signed lower bound was lost: %+v", got)
				}
			} else if *got.Confidence.VarianceOfMean != 0 ||
				got.Confidence.HalfWidth != 0 || got.Confidence.EffectiveDegreesOfFreedom != nil {
				t.Fatalf("zero-width confidence changed: %+v", got.Confidence)
			}
			encoded, err := json.Marshal(rows)
			if err != nil {
				t.Fatal(err)
			}
			var decoded []BatchStatistics
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, rows) {
				t.Fatalf("statistics JSON round trip changed values: %s", encoded)
			}
			var wire []map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &wire); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{
				"batch", "incrementalWorkJoulesPerItem", "activeTrialCount", "idleTrialCount",
				"confidence", "latencySampleCount", "latencyP95Milliseconds",
			} {
				if _, exists := wire[0][field]; !exists {
					t.Fatalf("missing JSON field %q: %s", field, encoded)
				}
			}
			if len(wire[0]) != 7 {
				t.Fatalf("summary exposed fields beyond its read contract: %s", encoded)
			}
			var confidence map[string]json.RawMessage
			if err := json.Unmarshal(wire[0]["confidence"], &confidence); err != nil {
				t.Fatal(err)
			}
			_, hasDegreesOfFreedom := confidence["effectiveDegreesOfFreedom"]
			if hasDegreesOfFreedom != nonzero {
				t.Fatalf("confidence omitempty semantics changed: %s", encoded)
			}
			*rows[0].Confidence.VarianceOfMean = 999
			if rows[0].Confidence.EffectiveDegreesOfFreedom != nil {
				*rows[0].Confidence.EffectiveDegreesOfFreedom = 999
			}
			if !reflect.DeepEqual(curve.Points()[0].Confidence, point.Confidence) {
				t.Fatal("summary confidence pointers mutate the curve")
			}
		})
	}
}
