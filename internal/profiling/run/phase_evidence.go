package profilingrun

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

// SupplySizing is the immutable result of sizing saturation from this Run's
// sparse service population. It is not an operator-selected service bound.
type SupplySizing struct {
	Batch                          int     `json:"batch"`
	MaxBatchServiceSeconds         float64 `json:"maxBatchServiceSeconds"`
	ServiceTimeSource              string  `json:"serviceTimeSource"`
	RequestLifetimeSeconds         float64 `json:"requestLifetimeSeconds"`
	MarginSeconds                  float64 `json:"marginSeconds"`
	WorkerCap                      int     `json:"workerCap"`
	QueueBytesCap                  int64   `json:"queueBytesCap"`
	FramedDecodedRequestUpperBytes int64   `json:"framedDecodedRequestUpperBytes"`
	Workers                        int     `json:"workers"`
	LimitingBound                  string  `json:"limitingBound"`
}

func validatePhases(config RunConfig, phases []Phase, sizing SupplySizing, started, ended time.Time) (int, error) {
	points := config.Measurement.OperatingPoints
	if points == nil || len(phases) != len(points.Utilizations)+2 {
		return 0, errors.New("runtime requires sparse, saturation and every utilization phase")
	}
	if err := validateSupplySizing(config, phases[0], sizing); err != nil {
		return 0, err
	}
	previousEnd := started
	seenIDs := make(map[string]bool, len(phases))
	seenRequests := make(map[string]bool)
	calls := 0
	var saturationRate float64
	for index, phase := range phases {
		if phase.ID == "" || seenIDs[phase.ID] {
			return 0, errors.New("phase identity is empty or duplicated")
		}
		seenIDs[phase.ID] = true
		want := "utilization"
		if index == 0 {
			want = "sparse"
		} else if index == 1 {
			want = "saturation"
		}
		if phase.Kind != want {
			return 0, errors.New("operating-point phase sequence is invalid")
		}
		start, err := time.Parse(time.RFC3339Nano, phase.MeasurementStartedAt)
		if err != nil {
			return 0, errors.New("phase start is invalid")
		}
		end, err := time.Parse(time.RFC3339Nano, phase.MeasurementEndedAt)
		if err != nil || !end.After(start) || start.Before(previousEnd) || end.After(ended) {
			return 0, errors.New("phase windows must be ordered inside measurement window")
		}
		previousEnd = end
		if !finitePositive(phase.MeasurementElapsedSeconds) || phase.MeasurementElapsedSeconds < float64(points.PhaseDurationSeconds) {
			return 0, errors.New("phase elapsed duration floor is incomplete")
		}
		if phase.CompletedInputs != len(phase.NativeSamples)*config.Measurement.Batch || phase.CompletedInputs != len(phase.Requests) {
			return 0, errors.New("phase completed-input/request/batch counts disagree")
		}
		if err := validateDriverGC(phase.DriverGC); err != nil {
			return 0, err
		}
		if err := validateIssueEvidence(phase); err != nil {
			return 0, err
		}
		for _, request := range phase.Requests {
			if request.Response.Progress.NativeElapsedMS > float64(points.RequestLifetimeSeconds)*1000 {
				return 0, errors.New("native receipt-to-completion span exceeds frozen request lifetime")
			}
			if seenRequests[request.RequestID] {
				return 0, errors.New("request identity is duplicated across phases")
			}
			seenRequests[request.RequestID] = true
		}
		switch phase.Kind {
		case "saturation":
			if phase.Workers != sizing.Workers || phase.ArrivalRatePerSecond != 0 || phase.Utilization != 0 {
				return 0, errors.New("saturation supply differs from sparse-derived K")
			}
			saturationRate = float64(phase.CompletedInputs) / phase.MeasurementElapsedSeconds
		case "utilization":
			if phase.Utilization != points.Utilizations[index-2] || phase.ArrivalRatePerSecond != phase.Utilization*saturationRate || phase.Workers != 0 {
				return 0, errors.New("utilization phase differs from measured saturation throughput")
			}
		case "sparse":
			if phase.ArrivalRatePerSecond != 1000/points.SparseMeanGapMilliseconds || phase.Utilization != 0 || phase.Workers != 0 {
				return 0, errors.New("sparse arrival rate differs from frozen mean gap")
			}
		}
		for _, latency := range phase.LatencyMilliseconds {
			if !finitePositive(latency) {
				return 0, errors.New("phase client latency must be finite and positive")
			}
		}
		if err := validatePhaseRequests(phase, config.Measurement.Batch, start, end); err != nil {
			return 0, fmt.Errorf("phase %s: %w", phase.ID, err)
		}
		calls += len(phase.NativeSamples)
	}
	return calls, nil
}

func validateSupplySizing(config RunConfig, sparse Phase, sizing SupplySizing) error {
	points := config.Measurement.OperatingPoints
	var maximum int64
	var previousEnd time.Time
	for _, sample := range sparse.NativeSamples {
		if !sample.EarliestEnqueuedAt.IsZero() && (previousEnd.IsZero() || !sample.EarliestEnqueuedAt.Before(previousEnd)) {
			maximum = max(maximum, sample.ServiceDurationNanos)
		}
		previousEnd = sample.FinishedAt
	}
	if maximum <= 0 {
		return errors.New("sparse phase has no observed no-backlog service sample for saturation sizing")
	}
	service := float64(maximum) / 1e9
	if sizing.ServiceTimeSource != "run.sparse.maxObservedSeconds" || sizing.MaxBatchServiceSeconds != service ||
		sizing.Batch != config.Measurement.Batch || sizing.WorkerCap != 64 ||
		sizing.RequestLifetimeSeconds != float64(points.RequestLifetimeSeconds) || sizing.MarginSeconds != float64(points.DeliveryMarginMilliseconds)/1000 ||
		sizing.FramedDecodedRequestUpperBytes != points.FramedDecodedRequestUpperBytes || sizing.QueueBytesCap != points.QueueBytesCap {
		return errors.New("saturation sizing differs from frozen inputs or sparse service observations")
	}
	workers, bound, err := firstfloor.SaturationWorkers(config.Measurement.Batch, config.Actor.MaxQueueRequests, sizing.QueueBytesCap,
		sizing.FramedDecodedRequestUpperBytes, sizing.RequestLifetimeSeconds, sizing.MarginSeconds*1000, service)
	if err != nil {
		return err
	}
	if sizing.Workers != workers {
		return errors.New("saturation workers differ from measured supply formula")
	}
	if sizing.LimitingBound != bound {
		return errors.New("saturation limiting bound differs from measured supply formula")
	}
	return nil
}
func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateIssueEvidence(phase Phase) error {
	if !finitePositive(phase.IssueElapsedSeconds) || phase.IssueElapsedSeconds > phase.MeasurementElapsedSeconds ||
		!finitePositive(phase.AchievedArrivalRatePerSecond) ||
		phase.AchievedArrivalRatePerSecond != float64(phase.ScheduledInputs)/phase.IssueElapsedSeconds ||
		phase.ScheduledInputs != phase.SentInputs || phase.SentInputs != phase.CompletedInputs ||
		phase.ScheduledInputs < 1 || phase.OfferingStartedAt.IsZero() ||
		!phase.OfferingEndedAt.After(phase.OfferingStartedAt) || phase.DrainEndedAt.Before(phase.OfferingEndedAt) {
		return errors.New("phase schedule, send count, rate or offering/drain boundaries are invalid")
	}
	if phase.Kind == "saturation" {
		if phase.IssueLag != nil || phase.ArrivalSeed != "" {
			return errors.New("saturation must omit Poisson seed and issue lag")
		}
		return nil
	}
	lag := phase.IssueLag
	if strings.TrimSpace(phase.ArrivalSeed) == "" || strings.TrimSpace(phase.ArrivalSeed) != phase.ArrivalSeed ||
		lag == nil || lag.Count != phase.SentInputs || lag.Count < 1 {
		return errors.New("Poisson seed or issue lag count is invalid")
	}
	for _, value := range []float64{lag.Min, lag.Mean, lag.P50, lag.P95, lag.P99, lag.Max} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("Poisson issue lag must be finite and nonnegative")
		}
	}
	if lag.Min > lag.P50 || lag.P50 > lag.P95 || lag.P95 > lag.P99 || lag.P99 > lag.Max ||
		lag.Mean < lag.Min || lag.Mean > lag.Max {
		return errors.New("Poisson issue lag statistics are unordered")
	}
	return nil
}

func validateDriverGC(gc *DriverGC) error {
	if gc == nil {
		return errors.New("phase driverGC observation is required")
	}
	for _, value := range []float64{gc.PauseTotalMilliseconds, gc.MaxPauseMilliseconds} {
		if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return errors.New("driverGC pauses must be finite and nonnegative")
		}
	}
	if gc.MaxPauseMilliseconds > gc.PauseTotalMilliseconds ||
		gc.Cycles == 0 && (gc.PauseTotalMilliseconds != 0 || gc.MaxPauseMilliseconds != 0) {
		return errors.New("driverGC cycles and pause observations disagree")
	}
	return nil
}

func validatePhaseRequests(phase Phase, batch int, start, end time.Time) error {
	requests := make(map[string]*PhaseRequest, len(phase.Requests))
	var previousScheduled, lastCompleted time.Time
	for index := range phase.Requests {
		request := &phase.Requests[index]
		progress := &request.Response.Progress
		if request.RequestID == "" || request.RecordID == "" || request.Response.ID != request.RequestID ||
			progress.RequestID != request.RequestID || progress.BatchID == 0 || progress.BatchSize != batch ||
			!request.Response.ExecutionComplete || !progress.ExecutionComplete || progress.Units < 1 || !finitePositive(progress.NativeElapsedMS) ||
			request.ScheduledAt.IsZero() || request.DispatchedAt.Before(request.ScheduledAt) || !request.CompletedAt.After(request.DispatchedAt) ||
			request.ScheduledAt.Before(previousScheduled) || !finitePositive(request.LatencyMilliseconds) {
			return errors.New("request identity, completion or load-node timing is invalid")
		}
		if request.ScheduledAt.Before(phase.OfferingStartedAt) || request.ScheduledAt.After(phase.OfferingEndedAt) ||
			request.CompletedAt.After(phase.DrainEndedAt) {
			return errors.New("request offering/drain coverage is invalid")
		}
		if progress.NativeElapsedMS > float64(request.CompletedAt.Sub(request.DispatchedAt))/float64(time.Millisecond) {
			return errors.New("native request span exceeds dispatched client span")
		}
		previousScheduled = request.ScheduledAt
		if request.CompletedAt.After(lastCompleted) {
			lastCompleted = request.CompletedAt
		}
		if _, found := requests[request.RequestID]; found {
			return errors.New("duplicate request identity")
		}
		http := progress.NativeHTTP
		if http == nil || http.HandlerEnteredAt.IsZero() || http.ReceivedNanos == nil || http.EnqueueNanos == nil || http.FinishedNanos == nil ||
			*http.ReceivedNanos < 0 || *http.EnqueueNanos < *http.ReceivedNanos || *http.FinishedNanos < *http.EnqueueNanos {
			return errors.New("native request receive/queue/finish boundaries are incomplete")
		}
		requests[request.RequestID] = request
	}
	if !previousScheduled.Equal(phase.OfferingEndedAt) || !lastCompleted.Equal(phase.DrainEndedAt) {
		return errors.New("offering and drain ends must match the last scheduled arrival and response")
	}
	for sampleIndex, sample := range phase.NativeSamples {
		if len(sample.RequestIDs) != batch || len(sample.RecordIDs) != batch || len(sample.Units) != batch ||
			sample.StartedAt.IsZero() || !sample.FinishedAt.After(sample.StartedAt) || sample.EarliestEnqueuedAt.IsZero() ||
			sample.StartedAt.Before(start) || sample.FinishedAt.After(end) ||
			sample.LatestEnqueuedAt.Before(sample.EarliestEnqueuedAt) || sample.LatestEnqueuedAt.After(sample.StartedAt) {
			return errors.New("native batch membership or anchors are incomplete")
		}
		var maximumLatency float64
		for index, id := range sample.RequestIDs {
			request := requests[id]
			if request == nil || request.Response.Progress.BatchID != sample.BatchID ||
				sample.RecordIDs[index] != request.RecordID || sample.Units[index] != request.Response.Progress.Units {
				return errors.New("native sample request membership, record or units disagree")
			}
			delete(requests, id)
			if request.Response.Progress.NativeElapsedMS > request.LatencyMilliseconds {
				return errors.New("native request span exceeds client latency")
			}
			maximumLatency = max(maximumLatency, request.LatencyMilliseconds)
		}
		if sampleIndex >= len(phase.LatencyMilliseconds) || phase.LatencyMilliseconds[sampleIndex] != maximumLatency {
			return errors.New("native batch maximum client latency is missing")
		}
	}
	if len(requests) != 0 {
		return errors.New("completed requests have no physical batch sample")
	}
	return nil
}
