package workloadobserver

import (
	"encoding/json"
	"errors"
	"fmt"
)

// VerifiedResult is populated only after one workload observation has crossed
// its validation boundary.
type VerifiedResult struct {
	payload  []byte
	verified bool
}

func VerifyResult(result Result) (VerifiedResult, error) {
	if err := result.Validate(); err != nil {
		return VerifiedResult{}, err
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return VerifiedResult{}, fmt.Errorf("encode verified workload result: %w", err)
	}
	return VerifiedResult{payload: payload, verified: true}, nil
}

func (r VerifiedResult) Record() (Result, error) {
	if !r.verified {
		return Result{}, errors.New("verified workload result is empty")
	}
	var result Result
	if err := json.Unmarshal(r.payload, &result); err != nil {
		return Result{}, fmt.Errorf("decode verified workload result: %w", err)
	}
	return result, nil
}
