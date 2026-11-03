package state

import (
	bytes "bytes"
	json "encoding/json"
	slices "slices"
	strings "strings"

	serving "github.com/lab-paper-code/chill/internal/serving"
)

// ManualOperation prepares or removes one instance of a Workload outside
// autonomous control.
type ManualOperation struct {
	Kind           string                `json:"kind"`
	WorkloadUID    string                `json:"workloadUID"`
	ProfileID      string                `json:"profileID,omitempty"`
	Node           serving.NodeReference `json:"node"`
	Instance       string                `json:"instance,omitempty"`
	FixedBatchSize int                   `json:"fixedBatchSize,omitempty"`
	MaxBatch       *int                  `json:"maxBatch,omitempty"`
	Reason         string                `json:"reason"`
	Weights        map[string]int32      `json:"weights,omitempty"`
	WorkloadUIDs   []string              `json:"workloadUIDs,omitempty"`
	Scope          string                `json:"scope,omitempty"`
	ActorUsername  string                `json:"-"`
	ActorUID       string                `json:"-"`
}

// DecodeManualOperation rejects fields belonging to another operation kind,
// even when their supplied value is zero. HTTP and the CLI share this boundary.
func DecodeManualOperation(raw json.RawMessage) (ManualOperation, error) {
	var request ManualOperation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decodeErr := decoder.Decode(&request)
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return request, &serving.Error{Code: "InvalidBody", Message: err.Error()}
	}
	allowed := []string{"kind", "workloadUID", "reason"}
	switch request.Kind {
	case OperationPrepare:
		allowed = append(allowed, "profileID", "node", "fixedBatchSize", "maxBatch")
	case OperationRemove:
		allowed = append(allowed, "node", "instance")
	case OperationRoute:
		allowed = append(allowed, "weights")
	case OperationUnroute, OperationRelease:
	case OperationReset:
		allowed = []string{"kind", "workloadUIDs", "scope", "reason"}
	default:
		return request, &serving.Error{Code: "InvalidBody", Message: "kind must be prepare, remove, route, unroute, release or reset", Field: "kind"}
	}
	for field := range fields {
		if !slices.Contains(allowed, field) {
			return request, &serving.Error{Code: "InvalidBody", Message: field + " is not valid for " + request.Kind, Field: field}
		}
	}
	if decodeErr != nil {
		return request, &serving.Error{Code: "InvalidBody", Message: decodeErr.Error()}
	}
	if strings.TrimSpace(request.Reason) == "" {
		return request, &serving.Error{Code: "InvalidBody", Message: "reason is required", Field: "reason"}
	}
	return request, nil
}
