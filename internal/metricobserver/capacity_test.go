package metricobserver

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func testLimits() Limits { return Limits{MaxResultBytes: 8 << 20, MemoryBudgetBytes: 48 << 20} }
func capacityRequest() Request {
	return Request{Interval: 5 * time.Millisecond, Duration: 32 * time.Millisecond, RequestTimeout: time.Millisecond}
}

func TestCapacityCoversActualReadOutcomes(t *testing.T) {
	for _, outcome := range []string{"normal", "source failure", "power with thermal failure"} {
		for _, duration := range []time.Duration{2 * time.Millisecond, 5 * time.Millisecond, 32 * time.Millisecond} {
			t.Run(outcome+"/"+duration.String(), func(t *testing.T) {
				source := &fakeSource{read: func(context.Context) (Reading, error) {
					switch outcome {
					case "source failure":
						return Reading{}, errors.New("offline")
					case "power with thermal failure":
						return Reading{Watts: 5, ThermalError: context.DeadlineExceeded}, nil
					default:
						return Reading{Watts: 5}, nil
					}
				}}
				request := capacityRequest()
				request.Duration = duration
				need, err := RequiredCapacity(request, source.Identity(), testLimits(), false)
				if err != nil {
					t.Fatal(err)
				}
				observer, _ := New(source, testLimits())
				result, err := observer.Observe(context.Background(), request, func() {})
				if err != nil {
					t.Fatal(err)
				}
				if source.reads > need.Reads || result.Summary.Attempts > need.Events {
					t.Fatalf("read accounting: reads=%d need=%+v result=%+v", source.reads, need, result)
				}
				// A failure finishing at the watchdog is excluded like cancellation.
				if outcome == "source failure" && (len(result.Failures) == 0 || source.reads-len(result.Failures) > 1) {
					t.Fatal("source failures discarded")
				}
				if outcome == "power with thermal failure" && (len(result.Samples) != source.reads || len(result.Failures) != source.reads) {
					t.Fatal("paired power/failure discarded")
				}
				payload, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if int64(len(payload)) > need.ResultBytes {
					t.Fatal("JSON escaped the admitted envelope")
				}
			})
		}
	}
}

func TestCapacityDerivesDiagnosticSlotsFromBothByteEnvelopes(t *testing.T) {
	request := capacityRequest()
	source := (&fakeSource{}).Identity()
	limits := testLimits()
	full, err := RequiredCapacity(request, source, limits, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, memory := range []bool{false, true} {
		reduced := limits
		if memory {
			reduced.MemoryBudgetBytes = full.MemoryBytes / 2
		} else {
			reduced.MaxResultBytes = full.ResultBytes / 2
		}
		need, err := RequiredCapacity(request, source, reduced, true)
		if err != nil {
			t.Fatal(err)
		}
		if need.DiagnosticBytes >= full.DiagnosticBytes || need.Reads != full.Reads ||
			need.ResultBytes > reduced.MaxResultBytes || need.MemoryBytes > reduced.MemoryBudgetBytes {
			t.Fatalf("diagnostics did not adapt without changing the schedule: full=%+v reduced=%+v", full, need)
		}
	}
	for _, memory := range []bool{false, true} {
		undersized := limits
		if memory {
			undersized.MemoryBudgetBytes = 1
		} else {
			undersized.MaxResultBytes = 1
		}
		if _, err := RequiredCapacity(request, source, undersized, true); err == nil {
			t.Fatal("undersized fixed records accepted")
		}
	}
	request = capacityRequest()
	request.Duration = time.Duration(math.MaxInt64)
	request.Interval = 2 * time.Nanosecond
	request.RequestTimeout = time.Nanosecond
	if _, err := RequiredCapacity(request, source, testLimits(), true); err == nil {
		t.Fatal("overflowing schedule accepted")
	}
}

func TestCapacityReservesFinalReadAndCadencedRetries(t *testing.T) {
	request := capacityRequest()
	request.Duration = 100 * time.Millisecond
	end := make(chan struct{})
	reads := 0
	source := &fakeSource{read: func(context.Context) (Reading, error) {
		reads++
		if reads == 2 || reads == 3 {
			return Reading{}, errors.New("retry final power")
		}
		return Reading{Watts: 5, ThermalError: context.DeadlineExceeded}, nil
	}}
	ordinary, _ := RequiredCapacity(request, source.Identity(), testLimits(), false)
	until, err := RequiredCapacity(request, source.Identity(), testLimits(), true)
	if err != nil {
		t.Fatal(err)
	}
	if until.Reads <= ordinary.Reads || until.DiagnosticBytes >= ordinary.DiagnosticBytes {
		t.Fatal("final read is not reserved")
	}
	observer, _ := New(source, testLimits())
	result, err := observer.ObserveUntil(context.Background(), request, end, func() { close(end) })
	if err != nil {
		t.Fatal(err)
	}
	if reads != 4 || len(result.Samples) != 2 || len(result.Failures) != 4 || reads > until.Reads || result.Summary.Attempts > until.Events {
		t.Fatalf("final/retry evidence: reads=%d result=%+v capacity=%+v", reads, result, until)
	}
}

func TestLongThermalDiagnosticsNeverShortenPowerWindow(t *testing.T) {
	for _, truncated := range []bool{false, true} {
		request := Request{Interval: 20 * time.Millisecond, Duration: 200 * time.Millisecond, RequestTimeout: time.Millisecond}
		identity := (&fakeSource{}).Identity()
		capacity, err := RequiredCapacity(request, identity, testLimits(), false)
		if err != nil {
			t.Fatal(err)
		}
		// This mirrors a connection-refused diagnostic for a real-length URL.
		message := "read thermal endpoint: Get \"http://155.230.34.243:9102/metrics\": dial tcp 155.230.34.243:9102: connect: connection refused; auxiliary endpoint unavailable"
		if truncated {
			message += strings.Repeat("x", capacity.DiagnosticBytes+1)
		}
		source := &fakeSource{read: func(context.Context) (Reading, error) {
			return Reading{Watts: 5, ThermalError: errors.New(message)}, nil
		}}
		observer, _ := New(source, testLimits())
		result, err := observer.Observe(context.Background(), request, func() {})
		if err != nil || result.EndedAt.Sub(result.StartedAt) < request.Duration ||
			len(result.Samples) == 0 || len(result.Failures) != len(result.Samples) {
			t.Fatalf("long diagnostic interrupted power window: error=%v result=%+v", err, result)
		}
		for _, failure := range result.Failures {
			if failure.MessageTruncated != truncated || (truncated && (failure.MessageBytes != len(message) || failure.Message != message[:capacity.DiagnosticBytes])) ||
				(!truncated && (failure.Message != message || failure.MessageBytes != 0)) {
				t.Fatalf("diagnostic prefix metadata lost: %+v", failure)
			}
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(result)
		if int64(len(payload)) > capacity.ResultBytes {
			t.Fatal("truncated diagnostics exceeded result bound")
		}
	}
}

func TestZeroDiagnosticSlotStillPreservesFailureEvidence(t *testing.T) {
	request := capacityRequest()
	source := &fakeSource{read: func(context.Context) (Reading, error) { return Reading{}, errors.New("source unavailable") }}
	limits := testLimits()
	// Find the fixed-record boundary behaviorally, not by copying accounting.
	low, high := int64(1), limits.MaxResultBytes
	for low < high {
		mid := low + (high-low)/2
		candidate := limits
		candidate.MaxResultBytes = mid
		if _, err := RequiredCapacity(request, source.Identity(), candidate, false); err != nil {
			low = mid + 1
		} else {
			high = mid
		}
	}
	limits.MaxResultBytes = low
	capacity, err := RequiredCapacity(request, source.Identity(), limits, false)
	if err != nil {
		t.Fatal(err)
	}
	if capacity.DiagnosticBytes != 0 {
		t.Fatal("fixed-record boundary did not consume diagnostic space")
	}
	observer, _ := New(source, limits)
	result, err := observer.Observe(context.Background(), request, func() {})
	if err != nil || len(result.Failures) == 0 {
		t.Fatalf("failure evidence lost: %v %+v", err, result)
	}
	for _, failure := range result.Failures {
		if failure.Message != "" || !failure.MessageTruncated || failure.MessageBytes == 0 {
			t.Fatalf("missing truncation evidence: %+v", failure)
		}
	}
	if err := result.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityBoundsEscapedDiagnosticsAndThermalIssues(t *testing.T) {
	request := capacityRequest()
	identity := (&fakeSource{}).Identity()
	identity.ThermalMetrics = &ThermalMetricIdentity{ObservationAvailable: "available", Active: "active", Latched: "latched", CPUCoreThrottleEvents: "core", CPUPackageThrottleEvents: "package", MaximumTemperatureCelsius: "temperature"}
	need, err := RequiredCapacity(request, identity, testLimits(), true)
	if err != nil {
		t.Fatal(err)
	}
	sample, failure := largestRecords(identity)
	sample.Thermal.Issues = append([]MetricIssue(nil), sample.Thermal.Issues...)
	for i := range sample.Thermal.Issues {
		sample.Thermal.Issues[i].Message = strings.Repeat("\x00", need.DiagnosticBytes)
	}
	failure.Message = strings.Repeat("\x00", need.DiagnosticBytes)
	result := Result{Source: identity, StartedAt: sample.ObservedAt, EndedAt: sample.ObservedAt}
	for range need.Reads {
		result.Samples = append(result.Samples, sample)
		result.Failures = append(result.Failures, failure)
	}
	// Synthetic MaxFloat64 latencies would overflow when summed; actual
	// request latencies come from bounded time.Duration values.
	result.Summary = Summary{}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(payload)) > need.ResultBytes {
		t.Fatalf("escaped JSON=%d exceeds bound=%d", len(payload), need.ResultBytes)
	}
}
