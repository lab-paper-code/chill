package kube

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
)

const timeoutOutcome = "timeout"

func TestBurstPairThresholdRetainsStatistics(t *testing.T) {
	for _, pairs := range []int{0, 3, 4} {
		statistics := &serving.OccupancyStatistics{Mean: -12, Median: -10}
		if pairs == 0 {
			statistics = nil
		}
		record := BurstOccupancyRecord{SchemaVersion: BurstOccupancySchema, Estimate: &serving.OccupancyEstimate{Pairs: pairs, Statistics: statistics}}
		judgeOccupancy(&record, edge.DispatchOccupancyMeasurementSpec{MinimumPairs: 4})
		if record.Estimate.Statistics != statistics {
			t.Fatal("threshold changed observed statistics")
		}
		if pairs < 4 {
			if record.Verdict != measurementInsufficient || record.Reason != "TooFewPairs" {
				t.Fatalf("missing pair verdict: %+v", record)
			}
		} else if record.Verdict != "Observed" {
			t.Fatalf("observation must not imply scheduler admission: %+v", record)
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "perRequestNanos") || strings.Contains(string(raw), "idleFraction") {
			t.Fatalf("legacy scalar leaked into burst: %s", raw)
		}
		if pairs == 0 && !strings.Contains(string(raw), `"statistics":null`) {
			t.Fatalf("missing statistics must be null: %s", raw)
		}
	}
	if BurstOccupancyPrefix == DispatchOccupancyPrefix || BurstOccupancySchema == DispatchOccupancySchema {
		t.Fatal("burst overwrites scheduler evidence contract")
	}
}

func TestBurstOperationalFailuresRetainEstimate(t *testing.T) {
	record := BurstOccupancyRecord{Estimate: &serving.OccupancyEstimate{Pairs: 20, Statistics: &serving.OccupancyStatistics{Mean: 42}}}
	occupancyReason(&record, "PhysicalDrainUnconfirmed")
	occupancyReason(&record, "PhysicalDrainUnconfirmed")
	judgeOccupancy(&record, edge.DispatchOccupancyMeasurementSpec{MinimumPairs: 4})
	if record.Verdict != measurementInsufficient || len(record.Reasons) != 1 || record.Estimate.Statistics.Mean != 42 {
		t.Fatalf("lost evidence: %+v", record)
	}
}

func TestBurstConfiguredJobAndElapsedBudget(t *testing.T) {
	m := occupancyRoot("burst", time.Now(), 1)
	if err := m.Spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	plan := occupancyPlan{workload: &edge.Workload{}, dataset: &edge.Dataset{}, fields: []string{"x"}}
	for _, seed := range []uint64{0, 12345} {
		m.Spec.Seed = seed
		job := loadJob(m, testNode, plan, LoadGeneratorConfig{})
		args := job.Spec.Template.Spec.Containers[0].Args
		seedAt := slices.Index(args, "--seed")
		if seedAt < 0 || args[seedAt+1] != strconv.FormatUint(seed, 10) {
			t.Fatalf("configured seed missing: %v", args)
		}
		at := slices.Index(args, "--duration")
		if at < 0 || args[at+1] != "5.010" {
			t.Fatalf("supply extension missing: %v", args)
		}
		at = slices.Index(args, "--timeout")
		if at < 0 || args[at+1] != "2.011" {
			t.Fatalf("timeout must exceed lifetime and delivery: %v", args)
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--arrivals closed-loop") || strings.Contains(joined, "--rate") {
			t.Fatalf("not a closed-loop burst: %v", args)
		}
		if *job.Spec.ActiveDeadlineSeconds != 9 {
			t.Fatalf("deadline %d", *job.Spec.ActiveDeadlineSeconds)
		}
	}
	for _, margin := range []int32{1, 1000, 1001} {
		bounded := m.Spec
		bounded.DeliveryMarginMillis = margin
		bounded.DrainSeconds = 5
		extension := bounded.RequestLifetimeSeconds + (margin+999)/1000
		bounded.PlannedElapsedSeconds = bounded.PreparationSeconds + bounded.JobSetupSeconds + bounded.CollectionSeconds + bounded.WarmupSeconds + bounded.DurationSeconds + extension + bounded.DrainSeconds
		if err := bounded.ValidateConditions(); err != nil {
			t.Fatal(err)
		}
		job := loadJob(&edge.DispatchOccupancyMeasurement{Spec: bounded}, testNode, plan, LoadGeneratorConfig{})
		if int64(bounded.PreparationSeconds)+*job.Spec.ActiveDeadlineSeconds != int64(bounded.PlannedElapsedSeconds) {
			t.Fatal("job and elapsed budget diverge")
		}
		bounded.PlannedElapsedSeconds--
		if bounded.ValidateConditions() == nil {
			t.Fatal("accepted elapsed budget missing one second")
		}
	}
}

func TestBurstFrozenProfileIdentity(t *testing.T) {
	m := occupancyRoot("burst", time.Now(), 1)
	prep := measuredCPUPreparationFixture()
	p := prep.Instance.Profile
	p.ID = m.Spec.Conditions.ProfileID
	w := &edge.Workload{}
	w.Name = "plc"
	if err := validateOccupancyProfile(m.Spec, p, w, prep); err != nil {
		t.Fatal(err)
	}
	w.Generation++
	if validateOccupancyProfile(m.Spec, p, w, prep) == nil {
		t.Fatal("accepted stale workload")
	}
}
