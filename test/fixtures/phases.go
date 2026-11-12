package fixtures

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// PhaseOptions supplies frozen operating points without importing profiling/run,
// whose package-local tests also consume these fixtures.
type PhaseOptions struct {
	StartedAt                 time.Time
	Duration                  time.Duration
	Batch                     int
	Samples                   int
	Workers                   int
	Utilizations              []float64
	SparseMeanGapMilliseconds float64
	PodUID                    string
	RecordIDs                 []string
	Latencies                 []float64
	SupplySizing              map[string]any
	ServiceNanos              []int64
	GapPattern                bool
}

// ActivePhases builds complete HTTP request and physical batch evidence. The
// JSON conversion shares the stored shape while avoiding a profiling/run cycle.
func ActivePhases[P any](options PhaseOptions) []P {
	phases := make([]map[string]any, len(options.Utilizations)+2)
	var batchID uint64
	var previousFinished time.Time
	for index := range phases {
		start := options.StartedAt.Add(time.Duration(index) * options.Duration)
		end := start.Add(options.Duration)
		kind := "utilization"
		workers, utilization, rate := 0, 0.0, 0.0
		if index == 0 {
			kind, rate = "sparse", 1000/options.SparseMeanGapMilliseconds
		} else if index == 1 {
			kind, workers = "saturation", options.Workers
		} else {
			utilization = options.Utilizations[index-2]
			rate = utilization * float64(options.Samples*options.Batch) / options.Duration.Seconds()
		}
		latencies := make([]float64, options.Samples)
		samples := make([]map[string]any, options.Samples)
		requests := make([]map[string]any, 0, options.Samples*options.Batch)
		for call := range samples {
			batchID++
			arrival := start.Add(time.Duration(call+1) * options.Duration / time.Duration(options.Samples+1))
			started := arrival.Add(200 * time.Microsecond)
			service := int64(700000)
			if len(options.ServiceNanos) > 0 {
				service = options.ServiceNanos[call%len(options.ServiceNanos)]
			}
			pair := ServiceSample(options.PodUID, batchID-1, service)
			if options.GapPattern && call > 0 {
				started = previousFinished.Add(time.Duration(*pair.PrecedingIdleNanos))
				arrival = started.Add(-200 * time.Microsecond)
			}
			finished := started.Add(time.Duration(service))
			serviceMS, backendMS := float64(service)/1e6, .4
			if service != 700000 {
				backendMS = serviceMS / 2
			}
			nativeMS := serviceMS + .2
			if !previousFinished.IsZero() {
				gap := started.Sub(previousFinished).Nanoseconds()
				pair.PrecedingIdleNanos = &gap
			}
			previousFinished = finished
			ids, records, units := make([]string, options.Batch), make([]string, options.Batch), make([]int, options.Batch)
			for member := range ids {
				ids[member] = fmt.Sprintf("request-%d-%d", batchID, member)
				records[member], units[member] = fmt.Sprintf("record-%d", member), 1
				if len(options.RecordIDs) > 0 {
					records[member] = options.RecordIDs[member%len(options.RecordIDs)]
				}
			}
			latency := 1.0
			if len(options.Latencies) > 0 {
				latency = options.Latencies[call%len(options.Latencies)]
			}
			latencies[call] = latency
			samples[call] = map[string]any{
				"batchID": batchID, "batchSize": options.Batch, "requestIDs": ids, "recordIDs": records, "units": units,
				"serviceDurationNanos": pair.ServiceDurationNanos, "precedingIdleNanos": pair.PrecedingIdleNanos, "trialID": pair.TrialID,
				"runnerServiceMs": serviceMS, "preprocessMs": .1, "backendCallMs": backendMS, "postprocessMs": .1, "nativeRequestMs": nativeMS,
				"startedAt": started, "finishedAt": finished, "latestEnqueuedAt": arrival.Add(100 * time.Microsecond),
				"earliestEnqueuedAt": arrival.Add(100 * time.Microsecond),
			}
			for member, id := range ids {
				received, enqueue, finish := int64(0), int64(100000), service+200000
				response := map[string]any{"id": id, "model_name": "mobilenet-v2-050", "executionComplete": true,
					"outputs": []firstfloor.Field{{Name: "label", Data: [][]byte{[]byte("1")}}},
					"progress": map[string]any{"requestID": id, "phase": "complete", "at": finished,
						"batchID": batchID, "batchSize": options.Batch, "executionComplete": true,
						"outcome": "success", "units": 1, "serviceMS": serviceMS, "nativeElapsedMS": nativeMS,
						"nativeHTTP": &firstfloor.HTTPRequestTiming{HandlerEnteredAt: arrival, ReceivedNanos: &received, EnqueueNanos: &enqueue, FinishedNanos: &finish}}}
				requests = append(requests, map[string]any{"requestID": id, "recordID": records[member], "arrivalAt": arrival,
					"sentAt": arrival, "completedAt": arrival.Add(time.Duration(latency * float64(time.Millisecond))), "latencyMs": latency, "response": response})
			}
		}
		phases[index] = map[string]any{"id": fmt.Sprintf("%s-%d", kind, index), "kind": kind,
			"workers": workers, "utilization": utilization, "arrivalRatePerSecond": rate,
			"measurementStartedAt": start.Format(time.RFC3339Nano), "measurementEndedAt": end.Format(time.RFC3339Nano),
			"measurementElapsedSecondsMonotonic": options.Duration.Seconds(), "completedInputs": len(requests),
			"requests": requests, "latencyMs": latencies, "nativeSamples": samples}
		if kind != "saturation" {
			issueElapsed := requests[len(requests)-1]["sentAt"].(time.Time).Sub(start).Seconds()
			phases[index]["achievedArrivalRatePerSecond"] = float64(len(requests)) / issueElapsed
			phases[index]["issueElapsedSecondsMonotonic"] = issueElapsed
			phases[index]["issueLagToleranceMs"] = 10
			phases[index]["issueLagMs"] = map[string]any{"count": len(requests), "min": 0, "mean": 0, "p50": 0, "p95": 0, "p99": 0, "max": 0}
		}
	}
	encoded, err := json.Marshal(phases)
	if err != nil {
		panic(err)
	}
	var result []P
	if err := json.Unmarshal(encoded, &result); err != nil {
		panic(err)
	}
	return result
}

// ApplyActivePhases replaces only phase-dependent runtime fields.
func ApplyActivePhases[R any](result *R, options PhaseOptions) {
	phases := ActivePhases[map[string]any](options)
	duration := options.Duration * time.Duration(len(phases))
	payload, err := json.Marshal(map[string]any{
		"phases": phases, "inferenceCount": options.Samples * len(phases),
		"supplySizing":                       options.SupplySizing,
		"measurementStartedAt":               options.StartedAt.Format(time.RFC3339Nano),
		"measurementEndedAt":                 options.StartedAt.Add(duration).Format(time.RFC3339Nano),
		"measurementElapsedSecondsMonotonic": duration.Seconds(),
	})
	if err != nil {
		panic(err)
	}
	if err := json.Unmarshal(payload, result); err != nil {
		panic(err)
	}
}

// PhaseSupply uses the production sizing formula with the fixture's observed
// sparse maximum service time.
func PhaseSupply(batch, maxQueueRequests int, queueBytesCap, requestBytes int64, lifetimeSeconds, marginMilliseconds float64, observedMaximum ...float64) (int, map[string]any) {
	service := .0007
	if len(observedMaximum) > 0 {
		service = observedMaximum[0]
	}
	workers, bound, err := firstfloor.SaturationWorkers(batch, maxQueueRequests, queueBytesCap, requestBytes, lifetimeSeconds, marginMilliseconds, service)
	if err != nil {
		panic(err)
	}
	return workers, map[string]any{
		"batch": batch, "maxBatchServiceSeconds": service, "serviceTimeSource": "run.sparse.maxObservedSeconds",
		"requestLifetimeSeconds": lifetimeSeconds, "marginSeconds": marginMilliseconds / 1000, "workerCap": 64,
		"queueBytesCap": queueBytesCap, "framedDecodedRequestUpperBytes": requestBytes,
		"workers": workers, "limitingBound": bound,
	}
}

// OperatingPoints copies the common frozen fields into an API fixture type.
func OperatingPoints[P any](source any) P {
	encoded, err := json.Marshal(source)
	if err != nil {
		panic(err)
	}
	var result P
	if err := json.Unmarshal(encoded, &result); err != nil {
		panic(err)
	}
	return result
}
