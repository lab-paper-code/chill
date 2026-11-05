package batchenergyoptimization

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestSnapshotValidateRejectsInvalidPlannedBatchSizes(t *testing.T) {
	tests := []struct {
		name              string
		plannedBatchSizes []int
		want              string
	}{
		{
			name: "empty",
			want: "a non-empty planned batch size set is required",
		},
		{
			name:              "non-positive entry",
			plannedBatchSizes: []int{0, 1},
			want:              "planned batch sizes must be positive",
		},
		{
			name:              "descending pair",
			plannedBatchSizes: []int{2, 1},
			want:              "planned batch sizes must be strictly increasing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (Snapshot{PlannedBatchSizes: tt.plannedBatchSizes}).Validate()
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestDecodeDiscoveryResultValidatesTheRetainedPrefix(t *testing.T) {
	failed := active(2)
	base := DiscoveryResult{
		MeasuredBatchSizes: []int{1},
		Stop: DiscoveryStop{Reason: DiscoveryStopOOM, FailedMeasurement: &failed, Attempt: 2},
	}
	for _, test := range []struct {
		name      string
		completed []Measurement
		detail    DiscoveryResult
		wantError bool
	}{
		{"partial with one retained next-batch trial", append(completedForBatches([]int{1}, 3), active(2)), base, false},
		{"missing idle trial", []Measurement{idle(1), idle(1), active(1), active(1), active(1)}, base, true},
		{"missing active trial", []Measurement{idle(1), idle(1), idle(1), active(1), active(1)}, base, true},
		{"excess active trials", append(completedForBatches([]int{1}, 3), active(1)), base, true},
		{"unclaimed completed batch", completedForBatches([]int{1, 2}, 3), base, true},
		{"active beyond an incomplete batch", append(completedForBatches([]int{1}, 3), active(4)), base, true},
		{"unmeasured batch claimed", completedForBatches([]int{1}, 3), DiscoveryResult{
			MeasuredBatchSizes: []int{1, 2}, Stop: base.Stop,
		}, true},
		{"empty prefix after idle boundary", []Measurement{idle(1)}, DiscoveryResult{
			MeasuredBatchSizes: []int{}, Stop: DiscoveryStop{Reason: DiscoveryStopOOM, FailedMeasurement: &Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}, Attempt: 2},
		}, false},
		{"empty prefix after active boundary", []Measurement{idle(1), idle(1), idle(1), active(1)}, DiscoveryResult{
			MeasuredBatchSizes: []int{}, Stop: DiscoveryStop{Reason: DiscoveryStopMeasurementBudgetExceeded, FailedMeasurement: &Measurement{WorkloadMode: WorkloadModeActive, Batch: 1}, Attempt: 2},
		}, false},
		{"missing claimed prefix", []Measurement{idle(1)}, DiscoveryResult{
			Stop: DiscoveryStop{Reason: DiscoveryStopOOM, FailedMeasurement: &Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}, Attempt: 2},
		}, true},
		{"premature domain exhaustion", completedForBatches([]int{1}, 3), DiscoveryResult{
			MeasuredBatchSizes: []int{1}, Stop: DiscoveryStop{Reason: DiscoveryStopDomainExhausted},
		}, true},
		{"full domain", completedForBatches([]int{1, 2, 4}, 3), DiscoveryResult{
			MeasuredBatchSizes: []int{1, 2, 4}, Stop: DiscoveryStop{Reason: DiscoveryStopDomainExhausted},
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := discoveryTestRecord(t, test.detail, test.completed)
			detail, discovery, err := DecodeDiscoveryResult(record, []int{1, 2, 4})
			if !discovery || (err != nil) != test.wantError {
				t.Fatalf("discovery=%t detail=%+v err=%v", discovery, detail, err)
			}
			if err == nil && !reflect.DeepEqual(detail.MeasuredBatchSizes, test.detail.MeasuredBatchSizes) {
				t.Fatalf("decoded prefix=%v, want %v", detail.MeasuredBatchSizes, test.detail.MeasuredBatchSizes)
			}
		})
	}
}

func TestDecodeDiscoveryResultKeepsLegacyOutcomes(t *testing.T) {
	record, err := NewResultRecord(validResultRecordSpec())
	if err != nil {
		t.Fatal(err)
	}
	if _, discovery, err := DecodeDiscoveryResult(record, nil); discovery || err != nil {
		t.Fatalf("legacy record reinterpreted: discovery=%t err=%v", discovery, err)
	}
}

func TestDecodeDiscoveryResultRejectsMalformedDetail(t *testing.T) {
	spec := validResultRecordSpec()
	spec.Outcome = TerminalOutcome(BatchDiscoveryComplete)
	spec.Detail = json.RawMessage(`{"measuredBatchSizes":[],"stop":{"reason":"OOM"},"unexpected":true}`)
	record, err := NewResultRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, discovery, err := DecodeDiscoveryResult(record, []int{1, 2}); !discovery || err == nil {
		t.Fatalf("malformed discovery accepted: discovery=%t err=%v", discovery, err)
	}
}

func discoveryTestRecord(t *testing.T, detail DiscoveryResult, completed []Measurement) ResultRecord {
	t.Helper()
	spec := validResultRecordSpec()
	spec.Outcome = TerminalOutcome(BatchDiscoveryComplete)
	template := spec.CompletedTrials[0]
	spec.CompletedTrials = nil
	spec.ExcludedTrials = nil
	for index, measurement := range completed {
		trial := template
		trial.Run.Name = fmt.Sprintf("trial-%d", index)
		trial.Run.UID = trial.Run.Name
		trial.Evidence.Key = trial.Run.Name + "/execution.json"
		trial.Measurement = ResultMeasurement(measurement)
		spec.CompletedTrials = append(spec.CompletedTrials, trial)
	}
	var err error
	spec.Detail, err = json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewResultRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	return record
}
