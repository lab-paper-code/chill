package serving

import (
	"math"
	"reflect"
	"testing"
	"time"
)

func pathTimingEvidence(accepted, assigned, sender, nativeMS, elapsed, finalMS int64) (RequestRow, ClientObservation) {
	at := time.Date(2026, time.November, 9, 12, 0, 0, 0, time.UTC)
	acceptedNanos, finalNanos := accepted*int64(time.Millisecond), finalMS*int64(time.Millisecond)
	exchangeNanos, native := (sender+nativeMS)*int64(time.Millisecond), float64(nativeMS)
	row := RequestRow{
		Kind: RequestRowFinal, RequestID: "opaque request", WorkloadUID: "workload", ReceivedAt: at,
		Outcome: outcomeCompleted, ExecutionComplete: true, NativeExecutionComplete: true, NativeOutcome: outcomeCompleted,
		AcceptedNanos: &acceptedNanos, ExchangeNanos: &exchangeNanos, ElapsedNanos: &finalNanos, NativeElapsedMS: &native,
	}
	row.ServerTiming.Record(ServerAssigned, time.Duration(assigned)*time.Millisecond)
	row.ServerTiming.Record(ServerSenderElapsedFrozen, time.Duration(sender)*time.Millisecond)
	client := ClientObservation{
		ObservationID: "opaque observation", RequestID: row.RequestID, WorkloadName: "name", WorkloadUID: row.WorkloadUID,
		ElapsedNanos: elapsed * int64(time.Millisecond), Outcome: "completed", ReportedAt: at.Add(time.Second),
	}
	return row, client
}

func TestPathTimingSampleAccounting(t *testing.T) {
	for _, tc := range []struct {
		name                                                     string
		accepted, assigned, sender, native, elapsed, final          int64
		total, post                                              float64
	}{
		{"normal", 8, 28, 30, 60, 120, 101, 40, 30},
		{"serving wait is not counted twice", 8, 48, 50, 60, 140, 121, 40, 30},
		{"external roundtrip increase", 8, 28, 30, 60, 170, 101, 90, 80},
		{"faster native", 8, 28, 30, 40, 100, 81, 40, 30},
		{"server final may follow client fullread", 8, 28, 30, 60, 120, 121, 40, 30},
		{"observed zero is finite evidence", 0, 0, 0, 0, 0, 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, client := pathTimingEvidence(tc.accepted, tc.assigned, tc.sender, tc.native, tc.elapsed, tc.final)
			// Worker UTC is deliberately unrelated to serving's clock.
			remoteFinished := row.ReceivedAt.Add(24 * time.Hour)
			row.NativeFinishedAt = &remoteFinished
			sample, ok := PathTimingSampleFromRequest(row, client)
			if !ok || sample.TotalNanos != tc.total*float64(time.Millisecond) || sample.PostNanos != tc.post*float64(time.Millisecond) ||
				sample.RequestID != row.RequestID || sample.WorkloadUID != row.WorkloadUID || sample.ReceivedAt != row.ReceivedAt ||
				sample.ClientNanos != float64(client.ElapsedNanos) ||
				sample.ServingQueueNanos != float64(tc.assigned-tc.accepted)*float64(time.Millisecond) ||
				sample.NativeNanos != float64(tc.native)*float64(time.Millisecond) {
				t.Fatalf("wrong reserve: %+v, available=%v", sample, ok)
			}
			if sample.ServingQueueNanos+sample.NativeNanos+sample.TotalNanos != sample.ClientNanos ||
				float64(tc.sender+tc.native)*float64(time.Millisecond)+sample.PostNanos != float64(client.ElapsedNanos) {
				t.Fatal("planning or native accounting counted a duration twice")
			}
		})
	}
}

func TestPathTimingSampleRequiresClosedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*RequestRow, *ClientObservation)
	}{
		{"missing request ID", func(_ *RequestRow, c *ClientObservation) { c.RequestID = "" }},
		{"different request", func(_ *RequestRow, c *ClientObservation) { c.RequestID = "another request" }},
		{"different workload", func(_ *RequestRow, c *ClientObservation) { c.WorkloadUID = "another workload" }},
		{"missing accepted", func(r *RequestRow, _ *ClientObservation) { r.AcceptedNanos = nil }},
		{"missing assigned even with legacy UTC offset", func(r *RequestRow, _ *ClientObservation) {
			r.DispatchedNanos = new(int64)
			r.ServerTiming = RequestTimings{}
			r.ServerTiming.Record(ServerSenderElapsedFrozen, 30*time.Millisecond)
		}},
		{"missing sender", func(r *RequestRow, _ *ClientObservation) {
			r.ServerTiming = RequestTimings{}
			r.ServerTiming.Record(ServerAssigned, 28*time.Millisecond)
		}},
		{"missing native", func(r *RequestRow, _ *ClientObservation) { r.NativeElapsedMS = nil }},
		{"native still running", func(r *RequestRow, _ *ClientObservation) { r.NativeExecutionComplete = false }},
		{"native NaN", func(r *RequestRow, _ *ClientObservation) { *r.NativeElapsedMS = math.NaN() }},
		{"native infinite", func(r *RequestRow, _ *ClientObservation) { *r.NativeElapsedMS = math.Inf(1) }},
		{"native negative", func(r *RequestRow, _ *ClientObservation) { *r.NativeElapsedMS = -1 }},
		{"pending response", func(r *RequestRow, _ *ClientObservation) { r.ElapsedNanos = nil }},
		{"missing exchange", func(r *RequestRow, _ *ClientObservation) { r.ExchangeNanos = nil }},
		{"exchange follows response", func(r *RequestRow, _ *ClientObservation) { *r.ExchangeNanos = *r.ElapsedNanos + 1 }},
		{"assignment precedes acceptance", func(r *RequestRow, _ *ClientObservation) { *r.AcceptedNanos = 29 * int64(time.Millisecond) }},
		{"sender precedes assignment", func(r *RequestRow, _ *ClientObservation) {
			r.ServerTiming = RequestTimings{}
			r.ServerTiming.Record(ServerAssigned, 31*time.Millisecond)
			r.ServerTiming.Record(ServerSenderElapsedFrozen, 30*time.Millisecond)
		}},
		{"negative reserve", func(_ *RequestRow, c *ClientObservation) { c.ElapsedNanos = 89 * int64(time.Millisecond) }},
		{"client timeout with late native completion", func(_ *RequestRow, c *ClientObservation) { c.Outcome = "timeout" }},
		{"client transport error", func(_ *RequestRow, c *ClientObservation) { c.Outcome = "transport_error" }},
		{"client canceled", func(_ *RequestRow, c *ClientObservation) { c.Outcome = "canceled" }},
		{"server timeout with late native completion", func(r *RequestRow, c *ClientObservation) {
			r.Outcome, r.Error, c.Outcome = "timeout", "RequestTimeout", "http_error"
		}},
		{"transport failure with late native completion", func(r *RequestRow, c *ClientObservation) {
			r.Outcome, r.Error, c.Outcome = outcomeError, "BackendUnavailable", "http_error"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row, client := pathTimingEvidence(8, 28, 30, 60, 120, 101)
			tc.change(&row, &client)
			if sample, ok := PathTimingSampleFromRequest(row, client); ok || sample != (PathTimingSample{}) {
				t.Fatalf("incomplete or impossible evidence became a reserve: %+v", sample)
			}
		})
	}
}

func TestPathTimingSampleKeepsFullNativeFailuresSeparate(t *testing.T) {
	for _, tc := range []struct{ outcome, code, native string }{
		{"preprocess_failed", "PreprocessFailed", "preprocess_failed"},
		{outcomeError, "BackendError", "batch_error"},
		{"timeout", "SLODeadlineDrop", "deadline_drop"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			row, client := pathTimingEvidence(8, 28, 30, 60, 120, 101)
			row.Outcome, row.Error, row.NativeOutcome = tc.outcome, tc.code, tc.native
			client.Outcome, client.WorkloadUID = "http_error", ""
			sample, ok := PathTimingSampleFromRequest(row, client)
			if !ok || sample.TotalNanos != 40*float64(time.Millisecond) || sample.PostNanos != 30*float64(time.Millisecond) ||
				row.Outcome != tc.outcome || row.NativeOutcome != tc.native || client.Outcome != "http_error" {
				t.Fatalf("full native failure lost finite timing or changed outcome: %+v, available=%v", sample, ok)
			}
		})
	}
}

func TestBuildPathFeedbackEmpiricalQuantileAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name       string
		percentile float64
		samples    []PathTimingSample
		total, post float64
		source     string
	}{
		{name: "no samples", percentile: 95, total: 7, post: 7, source: "configured"},
		{name: "first sample applies", percentile: 95, samples: []PathTimingSample{{TotalNanos: 40, PostNanos: 30}}, total: 40, post: 30, source: "empirical"},
		{name: "observed zero beats configured margin", percentile: 95, samples: []PathTimingSample{{}}, source: "empirical"},
		{name: "p50 uses empirical rank", percentile: 50, samples: []PathTimingSample{{TotalNanos: 30, PostNanos: 20}, {TotalNanos: 20, PostNanos: 8}, {TotalNanos: 18, PostNanos: 10}, {TotalNanos: 22, PostNanos: 12}}, total: 20, post: 10, source: "empirical"},
		{name: "p95", percentile: 95, samples: []PathTimingSample{{TotalNanos: 22, PostNanos: 12}, {TotalNanos: 18, PostNanos: 8}, {TotalNanos: 30, PostNanos: 20}, {TotalNanos: 20, PostNanos: 10}}, total: 30, post: 20, source: "empirical"},
		{name: "p100", percentile: 100, samples: []PathTimingSample{{TotalNanos: 2, PostNanos: 1}, {TotalNanos: 3, PostNanos: 2}}, total: 3, post: 2, source: "empirical"},
		{name: "below configured margin", percentile: 95, samples: []PathTimingSample{{TotalNanos: 2, PostNanos: 1}}, total: 2, post: 1, source: "empirical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]PathTimingSample(nil), tc.samples...)
			feedback := BuildPathFeedback("workload", tc.samples, SLO{Percentile: tc.percentile}, 7*time.Nanosecond)
			if feedback.WorkloadUID != "workload" || feedback.Percentile != tc.percentile || feedback.SampleCount != len(tc.samples) ||
				feedback.TotalNanos != tc.total || feedback.PostNanos != tc.post || feedback.Source != tc.source {
				t.Fatalf("wrong path feedback: %+v", feedback)
			}
			if len(tc.samples) == 0 {
				if feedback.ObservedMean != nil {
					t.Fatal("configured feedback fabricated observed means")
				}
			} else if feedback.ObservedMean == nil {
				t.Fatal("finite sample lost observed means, including observed zero")
			}
			if !reflect.DeepEqual(tc.samples, before) {
				t.Fatal("quantile calculation reordered input evidence")
			}
		})
	}
}

func TestPathFeedbackObservedMeanUsesSameCausalSamples(t *testing.T) {
	var samples []PathTimingSample
	for _, evidence := range []struct {
		accepted, assigned, sender, native, elapsed, final int64
		outcome                                         string
	}{
		{8, 28, 30, 60, 120, 101, "completed"},
		{8, 48, 50, 40, 160, 101, "completed"},
		{8, 28, 30, 60, 999, 101, "timeout"},
	} {
		row, client := pathTimingEvidence(evidence.accepted, evidence.assigned, evidence.sender, evidence.native, evidence.elapsed, evidence.final)
		client.Outcome = evidence.outcome
		if sample, ok := PathTimingSampleFromRequest(row, client); ok {
			samples = append(samples, sample)
		}
	}
	feedback := BuildPathFeedback("workload", samples, SLO{Percentile: 95}, 7*time.Millisecond)
	mean := feedback.ObservedMean
	if feedback.SampleCount != 2 || mean == nil {
		t.Fatalf("mean population differs from finite reserve population: %+v", feedback)
	}
	ms := float64(time.Millisecond)
	if mean.ClientNanos != 140*ms || mean.ServingQueueNanos != 30*ms ||
		mean.NativeNanos != 50*ms || mean.NonComputeNanos != 60*ms ||
		mean.ClientNanos != mean.ServingQueueNanos+mean.NativeNanos+mean.NonComputeNanos {
		t.Fatalf("joined-sample mean decomposition changed: %+v", mean)
	}
	if feedback.TotalNanos != 80*ms || feedback.PostNanos != 70*ms ||
		mean.NonComputeNanos == feedback.TotalNanos {
		t.Fatalf("descriptive means replaced empirical budgets: %+v", feedback)
	}
	zero := BuildPathFeedback("workload", []PathTimingSample{{WorkloadUID: "workload"}}, SLO{Percentile: 95}, 7*time.Millisecond)
	if zero.ObservedMean == nil || *zero.ObservedMean != (PathObservedMean{}) || zero.TotalNanos != 0 || zero.Source != "empirical" {
		t.Fatalf("real zero sample became unknown/configured: %+v", zero)
	}
}
