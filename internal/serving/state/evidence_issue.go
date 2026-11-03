package state

// CostIssue is one measurement that contributes no cost, or, with code
// SequenceIncomplete, a record of an operation sequence that stopped early;
// that record keeps its own verdict.
type CostIssue struct {
	Source string `json:"source"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}
