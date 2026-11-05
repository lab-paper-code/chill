package batchenergyoptimization

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

// DecodeDiscoveryResult validates the claimed three-trial prefix against retained
// completed execution references. Legacy outcomes return discovery=false.
// References beyond the prefix remain audit evidence, never derived points.
func DecodeDiscoveryResult(record ResultRecord, planned []int) (detail DiscoveryResult, discovery bool, err error) {
	if err := record.requireInitialized(); err != nil {
		return detail, false, err
	}
	spec := record.Spec()
	if spec.Outcome != TerminalOutcome(BatchDiscoveryComplete) {
		return detail, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(spec.Detail))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&detail); err != nil {
		return detail, true, fmt.Errorf("decode batch discovery detail: %w", err)
	}
	counts := make(map[Measurement]int, len(spec.CompletedTrials))
	for _, trial := range spec.CompletedTrials {
		counts[Measurement(trial.Measurement)]++
	}
	prefix, err := discoveryPrefix(planned, counts)
	if err != nil {
		return detail, true, err
	}
	if detail.MeasuredBatchSizes == nil || !slices.Equal(detail.MeasuredBatchSizes, prefix) {
		return detail, true, errors.New("batch discovery detail contradicts the completed three-trial prefix")
	}
	if detail.Stop.Reason == DiscoveryStopDomainExhausted {
		if len(prefix) != len(planned) || detail.Stop.FailedMeasurement != nil || detail.Stop.Attempt != 0 || detail.Stop.Run != nil {
			return detail, true, errors.New("domain exhaustion requires the complete candidate domain and no failed trial")
		}
	} else {
		if err := validateDiscoveryBoundary(detail.Stop, planned, prefix, counts); err != nil {
			return detail, true, err
		}
		if detail.Stop.Run == nil {
			return detail, true, errors.New("batch discovery boundary requires its retained failed Run reference")
		}
		if err := validateRunReference(*detail.Stop.Run); err != nil {
			return detail, true, fmt.Errorf("batch discovery boundary Run: %w", err)
		}
		anchored := false
		for _, trial := range spec.ExcludedTrials {
			if trial.Run == *detail.Stop.Run &&
				Measurement(trial.Measurement) == *detail.Stop.FailedMeasurement &&
				trial.Attempt == detail.Stop.Attempt {
				anchored = true
				break
			}
		}
		if !anchored {
			return detail, true, errors.New("batch discovery boundary contradicts retained excluded trial references")
		}
	}
	return detail, true, nil
}

func discoveryPrefix(planned []int, counts map[Measurement]int) ([]int, error) {
	if len(planned) == 0 {
		return nil, errors.New("a non-empty planned batch size set is required")
	}
	if planned[0] != 1 {
		return nil, errors.New("batch discovery must start at batch one")
	}
	for index, batch := range planned {
		if batch < 1 || batch&(batch-1) != 0 || (index > 0 && batch <= planned[index-1]) {
			return nil, errors.New("batch discovery candidates must be strictly increasing powers of two")
		}
	}
	for measurement, count := range counts {
		if count > 3 {
			return nil, fmt.Errorf("%w: %s/b%d: expected=3, completed=%d", profilederivation.ErrEnergyObjectivePolicyMismatch,
				measurement.WorkloadMode, measurement.Batch, count)
		}
		if count < 1 || !slices.Contains(planned, measurement.Batch) ||
			(measurement.WorkloadMode != WorkloadModeActive && measurement != (Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1})) {
			return nil, errors.New("completed trial is outside the batch discovery protocol")
		}
	}
	prefix := make([]int, 0, len(planned))
	baselineComplete := counts[Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}] == 3
	incomplete := false
	for _, batch := range planned {
		count := counts[Measurement{WorkloadMode: WorkloadModeActive, Batch: batch}]
		if count != 0 && (!baselineComplete || incomplete) {
			return nil, errors.New("completed active trials follow an incomplete discovery measurement")
		}
		if count == 3 {
			prefix = append(prefix, batch)
		} else {
			incomplete = true
		}
	}
	return prefix, nil
}

func validateDiscoveryBoundary(stop DiscoveryStop, planned, prefix []int, counts map[Measurement]int) error {
	if !stop.isExecutionBoundary() || stop.FailedMeasurement == nil || stop.Attempt < 1 {
		return errors.New("batch discovery requires a classified boundary with its failed measurement and attempt")
	}
	expected := Measurement{WorkloadMode: WorkloadModeIdle, Batch: 1}
	if counts[expected] == 3 {
		if len(prefix) == len(planned) {
			return errors.New("execution boundary follows an already completed candidate domain")
		}
		expected = Measurement{WorkloadMode: WorkloadModeActive, Batch: planned[len(prefix)]}
	}
	if *stop.FailedMeasurement != expected {
		return errors.New("execution boundary does not identify the first incomplete discovery measurement")
	}
	return nil
}
