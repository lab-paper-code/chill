package serving_test

import (
	"context"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func init() {
	serving.RuntimeObservationConstructor = func(config serving.RuntimeTestObservationConfig) (serving.RuntimeTestObservation, error) {
		log, err := observation.NewObservationLog(observation.ObservationLogConfig{
			Directory: config.Directory, Store: config.Store, Writer: config.Writer,
			Retention: config.Retention, Diagnostics: config.Diagnostics,
		})
		if err != nil {
			return nil, err
		}
		return &runtimeObservation{ObservationLog: log}, nil
	}
}

type runtimeObservation struct{ *observation.ObservationLog }

func (l *runtimeObservation) HasRequest(uid, id string) (bool, error) {
	result, err := l.Query(context.Background(), uid, time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil {
		return false, err
	}
	for _, row := range result.Requests {
		if row.RequestID == id {
			return true, nil
		}
	}
	return false, nil
}

func TestRuntimeRejectsNilConcreteObservation(t *testing.T) {
	var log *observation.ObservationLog
	if _, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{QueueLimit: 1, ObservationLog: log}); err == nil {
		t.Fatal("typed nil observation log was accepted")
	}
}
