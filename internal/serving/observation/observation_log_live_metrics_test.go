package observation

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

func TestBlockedObservationWriterPublishesCurrentBacklogMetrics(t *testing.T) {
	at := observationTestHour
	var length, oldest atomic.Int64
	hub := live.New(at)
	t.Cleanup(hub.Close)
	metrics := live.NewMetrics(hub, at, &length, &oldest)
	metrics.SetWorkloads([]live.Workload{{UID: "uid"}})
	log, clock := clockedObservationLog(t, ObservationLogConfig{
		QueueLength: &length, QueueOldestEnqueued: &oldest,
		OnRequestEvent: func(event serving.RequestEvent) {
			if event.Kind == "received" {
				metrics.Received(event.WorkloadUID, event.At)
			}
		},
	}, at)
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sub.Close)
	run := func(operation func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- operation() }()
		awaitObservationLogOperation(t, done)
	}
	run(func() error {
		_, err := sub.Next(context.Background()) // Initial snapshot precedes metrics.
		return err
	})
	type metricsFrame struct {
		Type              string                 `json:"type"`
		QueueLength       int64                  `json:"chill_obslog_queue_length"`
		OldestWaitSeconds float64                `json:"chill_obslog_oldest_wait_seconds"`
		Workloads         map[string]live.Metric `json:"workloads"`
	}
	nextMetrics := func() metricsFrame {
		t.Helper()
		var data []byte
		run(func() error {
			if err := metrics.Flush(clock.now()); err != nil {
				return err
			}
			var err error
			data, err = sub.Next(context.Background())
			return err
		})
		var frame metricsFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			t.Fatal(err)
		}
		if frame.Type != "metrics" {
			t.Fatalf("wanted published metrics frame: %s", data)
		}
		return frame
	}

	entered, release := make(chan error, 1), make(chan struct{})
	resume := sync.OnceFunc(func() { close(release) })
	t.Cleanup(resume)
	var once sync.Once
	log.writeFile = func(file *os.File, data []byte) (int, error) {
		once.Do(func() { entered <- nil; <-release })
		return file.Write(data)
	}
	run(func() error {
		if err := log.RequestEvent(serving.RequestEvent{RequestID: "head", WorkloadUID: "uid", Kind: "received", At: at.Add(100 * time.Millisecond)}); err != nil {
			return err
		}
		return log.RequestEvent(serving.RequestEvent{RequestID: "head", WorkloadUID: "uid", Kind: "assigned", At: at.Add(200 * time.Millisecond),
			Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: at.Add(200 * time.Millisecond)}})
	})
	awaitObservationLogOperation(t, entered)

	clock.set(at.Add(time.Second))
	first := nextMetrics()
	if first.QueueLength != 1 || first.OldestWaitSeconds != 1 || first.Workloads["uid"].Received != 1 {
		t.Fatalf("blocked in-flight head or existing traffic metrics missing: %+v", first)
	}
	// Both request and other-stream admission must return while storage is blocked,
	// even after the consumer has read the existing metrics and backlog gauges.
	run(func() error {
		if err := log.RequestEvent(serving.RequestEvent{RequestID: "tail", WorkloadUID: "uid", Kind: "assigned", At: clock.now(),
			Assignment: &serving.Assignment{Instance: "physical", DispatchedAt: clock.now()}}); err != nil {
			return err
		}
		return log.ManagementEvent(serving.ManagementEvent{WorkloadUID: "uid", Kind: "apply", At: clock.now()})
	})
	clock.set(at.Add(2 * time.Second))
	second := nextMetrics()
	if second.QueueLength <= first.QueueLength || second.OldestWaitSeconds != 2 || second.OldestWaitSeconds <= first.OldestWaitSeconds {
		t.Fatalf("current backlog did not retain its growing enqueue age: first=%+v second=%+v", first, second)
	}

	resume()
	run(func() error { return log.Drain(context.Background()) })
	clock.set(at.Add(3 * time.Second))
	drained := nextMetrics()
	if drained.QueueLength != 0 || drained.OldestWaitSeconds != 0 {
		t.Fatalf("drained backlog retained historical gauges: %+v", drained)
	}
}
