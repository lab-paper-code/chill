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

func TestCapacityAdmitsExactByteBoundaries(t *testing.T) {
	request := capacityRequest()
	source := (&fakeSource{}).Identity()
	limits := testLimits()
	need, err := RequiredCapacity(request, source, limits, true)
	if err != nil {
		t.Fatal(err)
	}
	limits.MaxResultBytes = need.ResultBytes
	limits.MemoryBudgetBytes = need.MemoryBytes
	if _, err := RequiredCapacity(request, source, limits, true); err != nil {
		t.Fatalf("exact envelope refused: %v", err)
	}
	limits.MaxResultBytes--
	if _, err := RequiredCapacity(request, source, limits, true); err == nil {
		t.Fatal("undersized result accepted")
	}
	limits.MaxResultBytes++
	limits.MemoryBudgetBytes--
	if _, err := RequiredCapacity(request, source, limits, true); err == nil {
		t.Fatal("undersized memory accepted")
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
	if until.Reads <= ordinary.Reads || until.ResultBytes <= ordinary.ResultBytes {
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

func TestOversizeDiagnosticFailsWithPowerAndPrefixEvidence(t *testing.T) {
	for _, thermal := range []bool{false, true} {
		message := strings.Repeat("untrusted diagnostic ", maximumDiagnosticBytes)
		source := &fakeSource{read: func(context.Context) (Reading, error) {
			if thermal {
				return Reading{Watts: 5, ThermalError: errors.New(message)}, nil
			}
			return Reading{}, errors.New(message)
		}}
		observer, _ := New(source, testLimits())
		result, err := observer.Observe(context.Background(), capacityRequest(), func() {})
		if err == nil {
			t.Fatal("oversize diagnostic silently succeeded")
		}
		if len(result.Failures) != 1 || result.Failures[0].MessageBytes != len(message) ||
			result.Failures[0].Message != message[:maximumDiagnosticBytes] {
			t.Fatalf("diagnostic evidence lost: %+v", result)
		}
		if thermal && (len(result.Samples) != 1 || result.Samples[0].Watts != 5) {
			t.Fatal("power lost with oversized thermal error")
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
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
