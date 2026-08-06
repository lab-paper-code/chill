package worker

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
)

// verifiedMetricResult can only be populated after the metric-result file has
// crossed its validation boundary. JSON backing prevents a detached record
// from mutating the sealed value.
type verifiedMetricResult struct {
	payload []byte
	loaded  bool
}

func sealMetricResult(result metricobserver.Result) (verifiedMetricResult, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return verifiedMetricResult{}, fmt.Errorf("encode verified metric result: %w", err)
	}
	return verifiedMetricResult{payload: payload, loaded: true}, nil
}

func (r verifiedMetricResult) record() (metricobserver.Result, error) {
	if !r.loaded {
		return metricobserver.Result{}, errors.New("verified metric result is empty")
	}
	var result metricobserver.Result
	if err := json.Unmarshal(r.payload, &result); err != nil {
		return metricobserver.Result{}, fmt.Errorf("decode sealed metric result: %w", err)
	}
	return result, nil
}

// verifiedWorkloadResult can only be populated after the workload-result file
// has crossed its validation boundary.
type verifiedWorkloadResult struct {
	payload []byte
	loaded  bool
}

func sealWorkloadResult(result workloadobserver.Result) (verifiedWorkloadResult, error) {
	payload, err := json.Marshal(result)
	if err != nil {
		return verifiedWorkloadResult{}, fmt.Errorf("encode verified workload result: %w", err)
	}
	return verifiedWorkloadResult{payload: payload, loaded: true}, nil
}

func (r verifiedWorkloadResult) record() (workloadobserver.Result, error) {
	if !r.loaded {
		return workloadobserver.Result{}, errors.New("verified workload result is empty")
	}
	var result workloadobserver.Result
	if err := json.Unmarshal(r.payload, &result); err != nil {
		return workloadobserver.Result{}, fmt.Errorf("decode sealed workload result: %w", err)
	}
	return result, nil
}
