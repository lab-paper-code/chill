package metricobserver

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"
)

func TestNewRejectsNilSource(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New() error = nil, want source validation error")
	}
}

func TestNewUsesUTCReceiptTime(t *testing.T) {
	observer, err := New(&fakeSource{})
	if err != nil {
		t.Fatal(err)
	}
	if observer.now().Location() != time.UTC {
		t.Fatalf("receipt timestamp location = %v, want UTC", observer.now().Location())
	}
}

func TestRequestValidate(t *testing.T) {
	valid := Request{
		Interval: time.Second, Duration: time.Minute, RequestTimeout: 500 * time.Millisecond, MaxEvents: 62,
	}
	tests := []struct {
		name    string
		request Request
	}{
		{name: "zero interval", request: Request{Duration: valid.Duration, RequestTimeout: valid.RequestTimeout, MaxEvents: valid.MaxEvents}},
		{name: "zero duration", request: Request{Interval: valid.Interval, RequestTimeout: valid.RequestTimeout, MaxEvents: valid.MaxEvents}},
		{name: "zero timeout", request: Request{Interval: valid.Interval, Duration: valid.Duration, MaxEvents: valid.MaxEvents}},
		{name: "timeout equals interval", request: Request{Interval: valid.Interval, Duration: valid.Duration, RequestTimeout: valid.Interval, MaxEvents: valid.MaxEvents}},
		{name: "timeout exceeds interval", request: Request{Interval: valid.Interval, Duration: valid.Duration, RequestTimeout: 2 * valid.Interval, MaxEvents: valid.MaxEvents}},
		{name: "zero event budget", request: Request{Interval: valid.Interval, Duration: valid.Duration, RequestTimeout: valid.RequestTimeout}},
		{name: "event budget cannot cover window", request: Request{Interval: valid.Interval, Duration: valid.Duration, RequestTimeout: valid.RequestTimeout, MaxEvents: 61}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid request error = %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.request.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want validation error")
			}
		})
	}
}

func TestObservePreservesImmediateAndRepeatedSamples(t *testing.T) {
	active := false
	source := &fakeSource{values: []Reading{
		{Watts: 4.2, Thermal: ThermalReading{ObservationAvailable: boolPointer(true), Active: &active}},
		{Watts: 4.2, Thermal: ThermalReading{ObservationAvailable: boolPointer(true), Active: &active}},
	}}
	observer, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	times := []time.Time{
		time.Unix(0, 0),
		time.Unix(0, int64(time.Millisecond)),
		time.Unix(0, int64(time.Second)),
		time.Unix(0, int64(time.Second+time.Millisecond)),
		time.Unix(0, int64(2*time.Second)),
		time.Unix(0, int64(2*time.Second)),
	}
	observer.now = sequenceClock(t, times)
	ticks := make(chan time.Time, 1)
	ticks <- time.Unix(1, 0)
	close(ticks)

	result, err := observer.observe(context.Background(), time.Second, 2, ticks)
	if err != nil {
		t.Fatal(err)
	}
	if source.reads != 2 {
		t.Fatalf("source reads = %d, want immediate plus one tick", source.reads)
	}
	if len(result.Samples) != 2 || result.Samples[0].Watts != 4.2 || result.Samples[1].Watts != 4.2 {
		t.Fatalf("samples = %#v, want two retained repeated readings", result.Samples)
	}
	if result.Samples[0].Thermal == nil || result.Samples[0].Thermal.Active == nil ||
		*result.Samples[0].Thermal.Active {
		t.Fatalf("thermal sample = %#v", result.Samples[0].Thermal)
	}
	if result.Summary.Attempts != 2 || result.Summary.SuccessfulSamples != 2 || result.Summary.Failures != 0 {
		t.Fatalf("summary = %#v", result.Summary)
	}
	if result.Summary.MaximumSampleGapSeconds != 1 {
		t.Fatalf("maximum gap = %v, want 1 second", result.Summary.MaximumSampleGapSeconds)
	}
	if !result.Summary.SourceTimestampAbsent {
		t.Fatal("source timestamp absence was not preserved")
	}
}

func TestObserveRejectsInsufficientEventCapacityBeforeSourceRead(t *testing.T) {
	source := &fakeSource{values: []Reading{{Watts: 4.2}}}
	observer, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = observer.Observe(context.Background(), Request{
		Interval: time.Second, Duration: time.Minute, RequestTimeout: 500 * time.Millisecond, MaxEvents: 61,
	})
	if err == nil || !strings.Contains(err.Error(), "cannot cover") {
		t.Fatalf("Observe() error = %v, want preflight capacity rejection", err)
	}
	if source.reads != 0 {
		t.Fatalf("preflight rejection performed %d source reads", source.reads)
	}
}

func TestObserveClassifiesSourceFailures(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		reason FailureReason
	}{
		{name: "timeout", err: context.DeadlineExceeded, reason: FailureReasonTimeout},
		{name: "missing metric", err: ErrMetricMissing, reason: FailureReasonMetricMissing},
		{name: "invalid reading", err: ErrInvalidReading, reason: FailureReasonInvalidReading},
		{name: "source read", err: errors.New("connection reset"), reason: FailureReasonSourceRead},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeSource{errs: []error{test.err}}
			observer, err := New(source)
			if err != nil {
				t.Fatal(err)
			}
			observer.now = sequenceClock(t, []time.Time{time.Unix(0, 0), time.Unix(0, 0), time.Unix(0, int64(time.Millisecond)), time.Unix(0, int64(time.Second))})
			ticks := make(chan time.Time)
			close(ticks)
			result, observeErr := observer.observe(context.Background(), time.Second, 1, ticks)
			if observeErr != nil {
				t.Fatal(observeErr)
			}
			if len(result.Failures) != 1 || result.Failures[0].Reason != test.reason {
				t.Fatalf("failures = %#v, want reason %q", result.Failures, test.reason)
			}
		})
	}
}

func TestObserveDoesNotTurnCancellationIntoSourceFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	source := &fakeSource{read: func(context.Context) (Reading, error) {
		cancel()
		return Reading{}, context.Canceled
	}}
	observer, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	result, err := observer.Observe(ctx, Request{
		Interval: time.Second, Duration: time.Minute, RequestTimeout: 500 * time.Millisecond, MaxEvents: 62,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary.Attempts != 0 || result.Summary.Failures != 0 {
		t.Fatalf("cancellation became source evidence: %#v", result.Summary)
	}
}

func TestObserveEnforcesProducerEventCap(t *testing.T) {
	source := &fakeSource{values: []Reading{{Watts: 1}, {Watts: 2}}}
	observer, err := New(source)
	if err != nil {
		t.Fatal(err)
	}
	observer.now = sequenceClock(t, []time.Time{
		time.Unix(0, 0), time.Unix(0, 0), time.Unix(0, 1), time.Unix(0, 2),
	})
	ticks := make(chan time.Time, 1)
	ticks <- time.Unix(1, 0)
	close(ticks)

	result, err := observer.observe(context.Background(), time.Second, 1, ticks)
	if !errors.Is(err, errEventBudgetExceeded) {
		t.Fatalf("observe error = %v, want event budget failure", err)
	}
	if source.reads != 1 || result.Summary.Attempts != 1 {
		t.Fatalf("producer exceeded cap: reads=%d summary=%#v", source.reads, result.Summary)
	}
}

func TestSummarizeUsesRawRecords(t *testing.T) {
	started := time.Unix(0, 0)
	result := Result{
		StartedAt: started,
		EndedAt:   started.Add(4 * time.Second),
		Samples: []Sample{
			{ObservedAt: started.Add(time.Second), RequestLatencySeconds: 0.1},
			{ObservedAt: started.Add(3 * time.Second), RequestLatencySeconds: 0.3},
		},
		Failures: []Failure{{ObservedAt: started.Add(2 * time.Second), RequestLatencySeconds: 0.2}},
	}
	summary := summarize(result)
	if summary.Attempts != 3 || summary.SuccessfulSamples != 2 || summary.Failures != 1 {
		t.Fatalf("summary counts = %#v", summary)
	}
	if math.Abs(summary.MeanRequestLatencySeconds-0.2) > 1e-12 || summary.P95RequestLatencySeconds != 0.3 {
		t.Fatalf("latency summary = %#v", summary)
	}
	if summary.MaximumSampleGapSeconds != 2 || summary.ObservationDurationSeconds != 4 {
		t.Fatalf("duration summary = %#v", summary)
	}
}

func TestResultValidateRejectsDriftedRawFacts(t *testing.T) {
	started := time.Unix(0, 0)
	valid := Result{
		Source:    SourceIdentity{Kind: "edge-metrics", Endpoint: "http://metrics", Metric: "power_watts"},
		StartedAt: started, EndedAt: started.Add(2 * time.Second),
		Samples: []Sample{{
			ObservedAt: started.Add(time.Second), Watts: 4.2, RequestLatencySeconds: 0.1,
			Thermal: &ThermalReading{ObservationAvailable: boolPointer(true), Active: boolPointer(false)},
		}},
	}
	valid.Summary = summarize(valid)
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid result rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Result)
	}{
		{name: "summary", mutate: func(result *Result) { result.Summary.Attempts++ }},
		{name: "sample timestamp", mutate: func(result *Result) {
			result.Samples[0].ObservedAt = result.EndedAt.Add(time.Second)
		}},
		{name: "sample value", mutate: func(result *Result) { result.Samples[0].Watts = -1 }},
		{name: "thermal temperature", mutate: func(result *Result) {
			value := math.NaN()
			result.Samples[0].Thermal.MaximumTemperatureCelsius = &value
		}},
		{name: "thermal issue", mutate: func(result *Result) {
			result.Samples[0].Thermal.Issues = []MetricIssue{{Metric: "thermal", Reason: FailureReasonTimeout, Message: "bad"}}
		}},
		{name: "source identity", mutate: func(result *Result) { result.Source.Metric = "" }},
		{name: "thermal source identity", mutate: func(result *Result) {
			result.Source.ThermalMetrics = &ThermalMetricIdentity{Active: "active"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			candidate.Samples = append([]Sample(nil), valid.Samples...)
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

type fakeSource struct {
	values []Reading
	errs   []error
	read   func(context.Context) (Reading, error)
	reads  int
}

func (s *fakeSource) ReadMetrics(ctx context.Context) (Reading, error) {
	if s.read != nil {
		s.reads++
		return s.read(ctx)
	}
	index := s.reads
	s.reads++
	if index < len(s.errs) && s.errs[index] != nil {
		return Reading{}, s.errs[index]
	}
	return s.values[index], nil
}

func boolPointer(value bool) *bool { return &value }

func (s *fakeSource) Identity() SourceIdentity {
	return SourceIdentity{Kind: "fake", Endpoint: "fake://source", Metric: "power_watts"}
}

func sequenceClock(t *testing.T, values []time.Time) func() time.Time {
	t.Helper()
	index := 0
	return func() time.Time {
		t.Helper()
		if index >= len(values) {
			t.Fatalf("clock called %d times, only %d values configured", index+1, len(values))
		}
		value := values[index]
		index++
		return value
	}
}
