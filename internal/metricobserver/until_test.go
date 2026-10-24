package metricobserver

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestObserveUntilRequiresRealFinalRead(t *testing.T) {
	for _, finalFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "real final sample", true: "failed final read is not completion"}[finalFailure], func(t *testing.T) {
			end := make(chan struct{})
			reads := 0
			source := &fakeSource{read: func(context.Context) (Reading, error) {
				reads++
				if reads == 2 && finalFailure {
					return Reading{}, errors.New("final source failure")
				}
				return Reading{Watts: float64(reads)}, nil
			}}
			observer, err := New(source)
			if err != nil {
				t.Fatal(err)
			}
			result, err := observer.ObserveUntil(context.Background(), Request{Interval: time.Second, Duration: time.Second, RequestTimeout: time.Millisecond, MaxEvents: 4}, end, func() { close(end) })
			if (err != nil) != finalFailure {
				t.Fatalf("error=%v finalFailure=%v", err, finalFailure)
			}
			if reads != 2 {
				t.Fatalf("source reads=%d want immediate and final", reads)
			}
			if !finalFailure && (len(result.Samples) != 2 || result.Samples[1].Watts != 2) {
				t.Fatalf("final sample=%+v", result.Samples)
			}
		})
	}
}

func TestObserveUntilWatchdogFailsWithoutProducerEnd(t *testing.T) {
	observer, err := New(&fakeSource{read: func(context.Context) (Reading, error) { return Reading{Watts: 1}, nil }})
	if err != nil {
		t.Fatal(err)
	}
	result, err := observer.ObserveUntil(context.Background(), Request{Interval: 10 * time.Millisecond, Duration: 30 * time.Millisecond, RequestTimeout: time.Millisecond, MaxEvents: 6}, make(chan struct{}), func() {})
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Samples) == 0 {
		t.Fatalf("watchdog result=%+v error=%v", result, err)
	}
}
