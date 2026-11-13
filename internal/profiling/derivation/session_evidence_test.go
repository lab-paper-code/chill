package profilederivation

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"
)

// fixtureTrial is one stored trial written by hack/evaluation/session_fixture.py.
type fixtureTrial struct {
	Run         string   `json:"run"`
	Batch       int      `json:"batch"`
	OriginNanos int64    `json:"originNanos"`
	MemoryBytes int64    `json:"memoryBytes"`
	MemoryKind  string   `json:"memoryKind"`
	PostLoad    [2]int64 `json:"postLoad"`
	Levels      []struct {
		Phase      string `json:"phase"`
		Start, End int64
		Sent       int `json:"sent"`
		Completed  int `json:"completed"`
		Batches    int `json:"batches"`
	} `json:"levels"`
	Rows struct {
		BatchIDDelta    []uint64 `json:"batchIDDelta"`
		Start           []int64  `json:"start"`
		Duration        []int64  `json:"duration"`
		ServiceResidual []int64  `json:"serviceResidual"`
		Idle            []*int64 `json:"idle"`
	} `json:"rows"`
	PowerTime  []int64   `json:"powerTime"`
	PowerWatts []float64 `json:"powerWatts"`
}

func (t fixtureTrial) at(micros int64) time.Time {
	return time.Unix(0, t.OriginNanos+micros*1000).UTC()
}

func (t fixtureTrial) power() []Sample {
	samples := make([]Sample, len(t.PowerTime))
	var at int64
	for i, delta := range t.PowerTime {
		at += delta
		samples[i] = Sample{ObservedAt: t.at(at), Watts: t.PowerWatts[i]}
	}
	return samples
}

// rows decodes the native rows, grouped by the level that owns them.
func (t fixtureTrial) rows() [][]NativeBatchRow {
	var id uint64
	var finish int64
	grouped := make([][]NativeBatchRow, len(t.Levels))
	index := 0
	for level, owned := range t.Levels {
		for range owned.Batches {
			gap := t.Rows.Start[index]
			if index == 0 {
				gap = 0
			}
			id += t.Rows.BatchIDDelta[index]
			start := finish + t.Rows.Start[index]
			finish = start + t.Rows.Duration[index]
			row := NativeBatchRow{
				BatchID: id, BatchSize: t.Batch, StartedAt: t.at(start), FinishedAt: t.at(finish),
				ServiceDurationNanos: (t.Rows.Duration[index] + t.Rows.ServiceResidual[index]) * 1000,
			}
			if residual := t.Rows.Idle[index]; residual != nil {
				idle := (gap + *residual) * 1000
				row.PrecedingIdleNanos = &idle
			}
			grouped[level] = append(grouped[level], row)
			index++
		}
	}
	return grouped
}

func loadSessionFixture(t *testing.T) map[string][]fixtureTrial {
	t.Helper()
	raw, err := os.ReadFile("testdata/session_trials.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string][]fixtureTrial
	if err := json.NewDecoder(reader).Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func trialSession(run string) string { return run[strings.LastIndex(run, "-")+1:] }

// sessionLevels maps a stored trial's phases to the session's levels:
// saturation, the 25 % utilisation level as the mid level, and the sparse
// level only at batch 1.
var sessionLevels = map[string]string{"saturation": "saturation", "utilization-0": "mid", "sparse": "sparse"}

func storedSession(trials []fixtureTrial, session string, settings SessionSettings) SessionInput {
	input := SessionInput{Settings: settings}
	var last fixtureTrial
	for _, trial := range trials {
		if trialSession(trial.Run) != session {
			continue
		}
		rows := trial.rows()
		batch := SessionBatchInput{Batch: trial.Batch, PeakMemoryBytes: trial.MemoryBytes, MemoryKind: trial.MemoryKind}
		for i, level := range trial.Levels {
			kind, ok := sessionLevels[level.Phase]
			if !ok || (kind == "sparse" && trial.Batch != 1) {
				continue
			}
			batch.Levels = append(batch.Levels, SessionLevelInput{
				Row: DriverLevelRow{
					Kind: kind, ID: fmt.Sprintf("%s/b%d/%s", session, trial.Batch, kind),
					Start: trial.at(level.Start), End: trial.at(level.End), SentInputs: level.Sent,
					CompletedInputs: level.Completed, CompletedBatches: level.Batches, EndReason: "stored phase elapsed",
				},
				Native: rows[i],
			})
		}
		input.Batches = append(input.Batches, batch)
		input.Power = append(input.Power, trial.power()...)
		if trial.Batch > last.Batch {
			last = trial
		}
	}
	input.PostLoadStart, input.PostLoadEnd = last.at(last.PostLoad[0]), last.at(last.PostLoad[1])
	return input
}

// The design's acceptance check on stored evidence: power derived from one
// stored trial per batch size predicts every level of the other trials of the
// same condition, including the utilisation levels a session never runs.
func TestSessionPowerPredictsOtherStoredTrials(t *testing.T) {
	settings := SessionSettings{
		MeterLagSeconds: 3, MinimumPowerWindowSeconds: 15, MidLevelRatio: 0.25, SparseMeanGapSeconds: 1.5,
		LevelDurationLimitSeconds: 600, PostLoadWindowSeconds: 300, MinimumSamples: 59, Percentile: 95,
	}
	// Bounds sit just above the measured maxima: genesis2 2.70-3.13 W (the
	// verification study: 2.8-3.1 W), jetsono 0.56-1.03 W (0.50-0.82 W) and
	// lattepanda 0.12-0.14 W (0.20-0.26 W). The CPU differences come from the
	// awake fraction alone: the study computed it from a Poisson idle formula,
	// the session measures it from native start and finish times; with the
	// formula and everything else unchanged the study's figures reappear.
	bounds := map[string]float64{"genesis2-ic-cuda": 3.2, "jetsono-ic-cpu": 1.1, "lattepanda-ic-cpu": 0.2}
	for condition, trials := range loadSessionFixture(t) {
		for _, session := range []string{"a1", "a2", "a3"} {
			result, err := DeriveSession(storedSession(trials, session, settings))
			if err != nil {
				t.Fatalf("%s %s: %v", condition, session, err)
			}
			power := result.Power
			if power.IdleWatts.Value == nil || power.AwakeStepWatts.Value == nil {
				t.Fatalf("%s %s: power not fitted: %+v", condition, session, power)
			}
			hold := 0.0
			if power.HoldSeconds.Value != nil {
				hold = *power.HoldSeconds.Value
			}
			slope := map[int]float64{}
			slopes := ""
			for _, batch := range result.Batches {
				slope[batch.Batch] = *batch.JoulesPerInput.Value
				slopes += fmt.Sprintf(" b%d %.4f %s", batch.Batch, slope[batch.Batch], batch.JoulesPerInput.Status)
			}
			worst, worstCell, sum, count := 0.0, "", 0.0, 0
			for _, trial := range trials {
				if trialSession(trial.Run) == session {
					continue
				}
				samples, rows := trial.power(), trial.rows()
				var executions []NativeBatchRow
				for _, level := range rows {
					executions = append(executions, level...)
				}
				for _, level := range trial.Levels {
					start, end := trial.at(level.Start).Add(3*time.Second), trial.at(level.End)
					joules, err := measurePower(start, end, samples)
					if err != nil {
						t.Fatal(err)
					}
					rate := float64(level.Completed) / trial.at(level.End).Sub(trial.at(level.Start)).Seconds()
					awake := newAwakeWindow(executions, start, end).fraction(hold)
					predicted := *power.IdleWatts.Value + *power.AwakeStepWatts.Value*awake + slope[trial.Batch]*rate
					errorWatts := math.Abs(predicted - joules/end.Sub(start).Seconds())
					sum, count = sum+errorWatts, count+1
					if errorWatts > worst {
						worst, worstCell = errorWatts, fmt.Sprintf("%s %s", trial.Run[strings.LastIndex(trial.Run, "-b")+1:], level.Phase)
					}
				}
			}
			t.Logf("%s %s: idle %.2f W, step %.2f W %s %v, hold %.4g s %s %v, slopes%s; max error %.2f W (%s), mean %.2f W over %d levels",
				condition, session, *power.IdleWatts.Value, *power.AwakeStepWatts.Value, power.AwakeStepWatts.Status, power.AwakeStepWatts.Range,
				hold, power.HoldSeconds.Status, power.HoldSeconds.Range, slopes, worst, worstCell, sum/float64(count), count)
			if worst > bounds[condition] {
				t.Errorf("%s %s: max error %.2f W exceeds %.2f W", condition, session, worst, bounds[condition])
			}
		}
	}
}
