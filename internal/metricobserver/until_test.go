package metricobserver

import (
	"context"
	"errors"
	"fmt"
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
				if reads >= 2 && finalFailure {
					return Reading{}, errors.New("final source failure")
				}
				return Reading{Watts: float64(reads)}, nil
			}}
			observer, err := New(source, testLimits())
			if err != nil {
				t.Fatal(err)
			}
			result, err := observer.ObserveUntil(context.Background(), Request{Interval: 20 * time.Millisecond, Duration: 80 * time.Millisecond, RequestTimeout: time.Millisecond}, end, func() { close(end) })
			if (err != nil) != finalFailure {
				t.Fatalf("error=%v finalFailure=%v", err, finalFailure)
			}
			if finalFailure && (!errors.Is(err, context.DeadlineExceeded) || reads < 3 || len(result.Failures) < 2) {
				t.Fatalf("watchdog error=%v reads=%d failures=%+v", err, reads, result.Failures)
			}
			if !finalFailure && reads != 2 {
				t.Fatalf("source reads=%d want immediate and final", reads)
			}
			if !finalFailure && (len(result.Samples) != 2 || result.Samples[1].Watts != 2) {
				t.Fatalf("final sample=%+v", result.Samples)
			}
		})
	}
}

func TestObserveUntilRetriesFinalReadWithinWatchdog(t *testing.T) {
	for _, failedReads := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d failures", failedReads), func(t *testing.T) {
			end := make(chan struct{})
			reads := 0
			var attempts []time.Time
			source := &fakeSource{read: func(context.Context) (Reading, error) {
				reads++
				attempts = append(attempts, time.Now())
				if reads > 1 && reads <= failedReads+1 {
					return Reading{}, errors.New("temporary final power failure")
				}
				return Reading{Watts: float64(reads)}, nil
			}}
			observer, err := New(source, testLimits())
			if err != nil {
				t.Fatal(err)
			}
			request := Request{Interval: 20 * time.Millisecond, Duration: time.Second, RequestTimeout: time.Millisecond}
			result, err := observer.ObserveUntil(context.Background(), request, end, func() { close(end) })
			if err != nil {
				t.Fatalf("final read did not recover: %v", err)
			}
			if reads != failedReads+2 || len(result.Samples) != 2 || len(result.Failures) != failedReads ||
				result.Summary.Attempts != failedReads+2 {
				t.Fatalf("reads=%d result=%+v", reads, result)
			}
			for i := 2; i < len(attempts); i++ {
				if attempts[i].Sub(attempts[i-1]) < request.Interval {
					t.Fatalf("retry interval=%s, want >=%s", attempts[i].Sub(attempts[i-1]), request.Interval)
				}
			}
			if err := result.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObserveUntilWatchdogFailsWithoutProducerEnd(t *testing.T) {
	observer, err := New(&fakeSource{read: func(context.Context) (Reading, error) { return Reading{Watts: 1}, nil }}, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	result, err := observer.ObserveUntil(context.Background(), Request{Interval: 10 * time.Millisecond, Duration: 30 * time.Millisecond, RequestTimeout: time.Millisecond}, make(chan struct{}), func() {})
	if !errors.Is(err, context.DeadlineExceeded) || len(result.Samples) == 0 {
		t.Fatalf("watchdog result=%+v error=%v", result, err)
	}
}
