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
	Run string `json:"run"`
	// Source is the raw evidence file the trial was extracted from.
	Source      string   `json:"source"`
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
var sessionLevels = map[string]string{"saturation": LevelSaturation, "utilization-0": LevelMid, "sparse": LevelSparse}

var storedSettings = SessionSettings{
	MeterLagSeconds: 3, MinimumPowerWindowSeconds: 15, MidLevelRatio: 0.25, SparseMeanGapSeconds: 1.5,
	LevelDurationLimitSeconds: 600, PostLoadWindowSeconds: 300, MinimumSamples: 59, ConfidenceLevel: 0.95,
}

func storedSession(trials []fixtureTrial, session string) SessionInput {
	input := SessionInput{
		Settings: storedSettings, MaxResultBytes: 64 << 20,
		MeterSource: json.RawMessage(`{"kind":"edgemetrics","endpoint":"stored","metric":"node_watts"}`),
	}
	var last fixtureTrial
	for _, trial := range trials {
		if trialSession(trial.Run) != session {
			continue
		}
		rows := trial.rows()
		batch := SessionBatchInput{Batch: trial.Batch, PeakMemoryBytes: trial.MemoryBytes, MemoryKind: trial.MemoryKind}
		for i, level := range trial.Levels {
			kind, ok := sessionLevels[level.Phase]
			if !ok || (kind == LevelSparse && trial.Batch != 1) {
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

func describe(p SessionParameter) string {
	value, ok := p.Value()
	bounds, _ := p.Range()
	if !ok {
		return fmt.Sprintf("%s %.4g..%.4g", p.Status(), bounds[0], bounds[1])
	}
	return fmt.Sprintf("%.4g %s %.4g..%.4g", value, p.Status(), bounds[0], bounds[1])
}

// genesis2's stored post-load series all settle: its twelve settled idles,
// from about 22 s to the end, are 66.08 W to 67.07 W; the highest, b8-a3,
// carries two idle spikes just after its settle point. The other conditions
// are logged.
func TestSessionIdleSettlesOnStoredPostLoad(t *testing.T) {
	tests := newSessionTests(storedSettings.ConfidenceLevel)
	for condition, trials := range loadSessionFixture(t) {
		for _, trial := range trials {
			observation, err := analysePostLoad(trial.power(), trial.at(trial.PostLoad[0]), trial.at(trial.PostLoad[1]), 3*time.Second, 15*time.Second, tests)
			if err != nil {
				t.Fatal(err)
			}
			idle, ok := observation.idle.Value()
			t.Logf("%s %s: idle %s from %.0f s, meter interval %.3g s, decay hold %v", condition, trial.Run[strings.LastIndex(trial.Run, "-b")+1:],
				describe(observation.idle), observation.settledAt, observation.interval, observation.hold != nil)
			if condition == "genesis2-ic-cuda" && (!ok || idle < 65.9 || idle > 67.1) {
				t.Errorf("%s: idle %s %s", trial.Run, describe(observation.idle), observation.idle.Reason())
			}
		}
	}
}

// The design's acceptance check on stored evidence: power derived from one
// stored trial per batch size predicts every level of the other trials of the
// same condition, including the utilisation levels a session never runs.
func TestSessionPowerPredictsOtherStoredTrials(t *testing.T) {
	// Bounds sit just above the recomputed maxima over sessions a1-a3:
	// genesis2 2.73-3.13 W, jetsono 0.55-1.01 W and lattepanda 0.12-0.15 W.
	// jetsono's batch 8 slope is a value near zero (0.021, 0.014, 0.020 J
	// with ranges from 0 to about 0.10 J), so every session predicts all 40
	// levels of the other trials.
	bounds := map[string]float64{"genesis2-ic-cuda": 3.2, "jetsono-ic-cpu": 1.1, "lattepanda-ic-cpu": 0.2}
	for condition, trials := range loadSessionFixture(t) {
		for _, session := range []string{"a1", "a2", "a3"} {
			result, err := DeriveSession(storedSession(trials, session))
			if err != nil {
				t.Fatalf("%s %s: %v", condition, session, err)
			}
			power := result.Power
			line := fmt.Sprintf("%s %s: idle %s, step %s, hold %s;", condition, session, describe(power.IdleWatts), describe(power.AwakeStepWatts), describe(power.HoldSeconds))
			idle, idleOK := power.IdleWatts.Value()
			step, stepOK := power.AwakeStepWatts.Value()
			hold, holdOK := power.HoldSeconds.Value()
			slope := map[int]float64{}
			for _, batch := range result.Batches {
				line += fmt.Sprintf(" b%d %s;", batch.Batch, describe(batch.JoulesPerInput))
				if value, ok := batch.JoulesPerInput.Value(); ok {
					slope[batch.Batch] = value
				}
			}
			worst, sum, count, skipped := 0.0, 0.0, 0, 0
			for _, trial := range trials {
				if trialSession(trial.Run) == session {
					continue
				}
				samples, rows := trial.power(), trial.rows()
				for i, level := range trial.Levels {
					e, slopeOK := slope[trial.Batch]
					if !idleOK || !slopeOK || !stepOK || (step > 0 && !holdOK) {
						skipped++
						continue
					}
					start, end := trial.at(level.Start).Add(3*time.Second), trial.at(level.End)
					joules, err := measurePower(start, end, samples)
					if err != nil {
						t.Fatal(err)
					}
					awake := 0.0
					if window, _, ok := levelAwakeWindow(rows[i], 3*time.Second); ok {
						awake = window.fraction(hold)
					}
					rate := float64(level.Completed) / trial.at(level.End).Sub(trial.at(level.Start)).Seconds()
					errorWatts := math.Abs(idle + step*awake + e*rate - joules/end.Sub(start).Seconds())
					worst, sum, count = math.Max(worst, errorWatts), sum+errorWatts, count+1
				}
			}
			t.Logf("%s max error %.2f W, mean %.2f W over %d levels, %d levels not predicted", line, worst, sum/float64(max(count, 1)), count, skipped)
			if worst > bounds[condition] || skipped > 0 {
				t.Errorf("%s %s: max error %.2f W exceeds %.2f W or %d levels not predicted", condition, session, worst, bounds[condition], skipped)
			}
		}
	}
}
