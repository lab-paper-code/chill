package serving

import (
	"encoding/json"
	"time"
)

// UnmarshalJSON enforces the current maximum-demand evidence boundary.
func (s *SchedulerDownState) UnmarshalJSON(raw []byte) error {
	type state SchedulerDownState
	record := struct {
		*state
		WindowMax   *float64       `json:"windowMax"`
		WindowNanos *time.Duration `json:"windowNanos"`
		Supply      *float64       `json:"supply"`
	}{state: (*state)(s)}
	if err := json.Unmarshal(raw, &record); err != nil {
		return err
	}
	if record.WindowMax == nil || record.WindowNanos == nil || record.Supply == nil {
		return &Error{Code: "IncompleteObservation", Message: "scaleDown requires windowMax, windowNanos and supply"}
	}
	s.WindowMax, s.WindowNanos, s.Supply = *record.WindowMax, *record.WindowNanos, *record.Supply
	return nil
}
