package serving

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
)

const prefixBeforeRequest = "prefix-before"

type prefixReadResult struct {
	requests []string
	power    int
	err      error
}

func pausedPrefixRead(snapshot *observationSnapshot, paused chan<- struct{}, resume <-chan struct{}) prefixReadResult {
	result := prefixReadResult{}
	result.err = snapshot.scan("requests", func(record observationRecord) {
		result.requests = append(result.requests, record.Request.RequestID)
		if record.Request.RequestID == prefixBeforeRequest {
			close(paused)
			<-resume
		}
	})
	if result.err != nil {
		return result
	}
	result.err = snapshot.scan("scheduler", func(record observationRecord) {
		if record.Scheduler.Power != nil {
			result.power++
		}
	})
	return result
}

func appendAfterPrefix(j *Journal, rotate bool, at time.Time) error {
	if rotate {
		j.mu.Lock()
		err := j.finish()
		if err == nil {
			err = j.start(time.Now().UTC())
		}
		j.mu.Unlock()
		if err != nil {
			return err
		}
	}
	if err := j.RequestEvent(RequestEvent{
		RequestID: "prefix-after", WorkloadUID: "w", Kind: phaseReceived, At: at, ReceivedAt: at,
	}); err != nil {
		return err
	}
	return j.SchedulerRecord(SchedulerObservation{At: at, Power: &metricobserver.Result{StartedAt: at, EndedAt: at}})
}

func TestObservationReadPrefixAllowsIngressAndPowerAppendWhilePaused(t *testing.T) {
	for _, scenario := range []string{"append", "rotate"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			j, err := NewJournal(JournalConfig{
				Directory: t.TempDir(), Store: "evidence", Window: time.Hour,
				Writer: &observationArchive{directory: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = j.Close(ctx) })
			at := time.Now().UTC()
			if err = j.RequestEvent(RequestEvent{
				RequestID: prefixBeforeRequest, WorkloadUID: "w", Kind: phaseReceived, At: at, ReceivedAt: at,
			}); err != nil {
				t.Fatal(err)
			}
			if err = j.SchedulerRecord(SchedulerObservation{At: at, Power: &metricobserver.Result{StartedAt: at, EndedAt: at}}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := j.snapshot("requests", "scheduler")
			if err != nil {
				t.Fatal(err)
			}
			paused, resume := make(chan struct{}), make(chan struct{}, 1)
			defer close(resume)
			readDone := make(chan prefixReadResult, 1)
			go func() { readDone <- pausedPrefixRead(snapshot, paused, resume) }()
			select {
			case <-paused:
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not reach its captured record")
			}
			appendDone := make(chan error, 1)
			go func() { appendDone <- appendAfterPrefix(j, scenario == "rotate", at.Add(time.Nanosecond)) }()
			select {
			case err = <-appendDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("paused observation reader blocked real ingress/power appends")
			}
			resume <- struct{}{}
			var result prefixReadResult
			select {
			case result = <-readDone:
			case <-time.After(5 * time.Second):
				t.Fatal("reader did not finish its captured prefix")
			}
			if result.err != nil || len(result.requests) != 1 || result.requests[0] != prefixBeforeRequest || result.power != 1 {
				t.Fatalf("reader included later appends or rotation: %+v", result)
			}
			load, err := j.LoadWindow(at.Add(-time.Second), at.Add(time.Second))
			if err != nil || len(load.Arrivals) != 2 || !j.Health().Healthy {
				t.Fatalf("next consumer lost completed appends or reported a raced partial line: %+v %v", load, err)
			}
			power, _, err := j.SchedulerObservationPage(at.Add(-time.Second), at.Add(time.Second), 10, 0)
			if err != nil || len(power) != 2 {
				t.Fatalf("next power consumer lost appended record: %+v %v", power, err)
			}
		})
	}
}

func TestObservationReadPrefixReportsActualTruncation(t *testing.T) {
	ctx := context.Background()
	j, err := NewJournal(JournalConfig{
		Directory: t.TempDir(), Store: "evidence", Window: time.Hour,
		Writer: &observationArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close(ctx) })
	at := time.Now().UTC()
	if err = j.RequestEvent(RequestEvent{RequestID: "truncated", Kind: phaseReceived, At: at}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := j.snapshot("requests")
	if err != nil {
		t.Fatal(err)
	}
	prefix := snapshot.streams["requests"][0]
	if err = j.files["requests"].Truncate(prefix.bytes - 1); err != nil {
		t.Fatal(err)
	}
	err = snapshot.scan("requests", func(observationRecord) {})
	if !errors.Is(err, io.ErrUnexpectedEOF) || !snapshot.windows[0].Incomplete || j.Health().Healthy {
		t.Fatalf("actual truncated prefix did not retain incomplete/error health: %v %+v", err, j.Health())
	}
}
