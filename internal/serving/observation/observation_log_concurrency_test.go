package observation

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// The consumer boundary is real blocked storage, not a manually held memory lock.
func TestBlockedAppendDoesNotBlockRequestAdmission(t *testing.T) {
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, observationTestHour)
	entered, release := make(chan struct{}), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		once.Do(func() { close(entered); <-release })
		return file.Write(data)
	}
	if err := log.RequestEvent(serving.RequestEvent{RequestID: "assignment", Kind: "assigned", At: observationTestHour,
		Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: observationTestHour}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("writer did not reach storage")
	}
	admitted := make(chan error, 1)
	go func() {
		err := log.RequestEvent(serving.RequestEvent{RequestID: "other", Kind: "received", At: observationTestHour})
		if err == nil {
			err = log.RequestClosed("assignment", serving.RequestTimings{})
		}
		admitted <- err
	}()
	select {
	case err := <-admitted:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("request admission waited for blocked Write")
	}
	resume()
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2 || rows[0].Kind != serving.RequestRowAssigned || rows[1].Kind != serving.RequestRowFinal {
		t.Fatalf("ordered assignment/final lost: %+v", rows)
	}
}
