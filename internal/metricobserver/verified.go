package metricobserver

import (
	"encoding/json"
	"errors"
	"fmt"
)

// VerifiedResult is populated only after one metric observation has crossed
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
		return VerifiedResult{}, fmt.Errorf("encode verified metric result: %w", err)
	}
	return VerifiedResult{payload: payload, verified: true}, nil
}

func (r VerifiedResult) Record() (Result, error) {
	if !r.verified {
		return Result{}, errors.New("verified metric result is empty")
	}
	var result Result
	if err := json.Unmarshal(r.payload, &result); err != nil {
		return Result{}, fmt.Errorf("decode verified metric result: %w", err)
	}
	return result, nil
}
