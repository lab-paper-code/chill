package serving_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func init() {
	serving.RuntimeObservationConstructor = func(config serving.RuntimeTestObservationConfig) (serving.RuntimeTestObservation, error) {
		if err := cleanObservationPredecessor(config.Directory); err != nil {
			return nil, err
		}
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

// Runtime fixtures model a quiescent handoff explicitly. An absent marker,
// including an empty directory, intentionally means unknown in production.
func cleanObservationPredecessor(directory string) error {
	file, err := os.OpenFile(filepath.Join(directory, "async-run.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write([]byte(`{"started":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","clean":true,"gaps":[]}`))
	return errors.Join(err, file.Close())
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
	if _, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{ObservationLog: log}); err == nil {
		t.Fatal("typed nil observation log was accepted")
	}
}
