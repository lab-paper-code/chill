package profilingrun

import (
	"errors"
	"fmt"
	"math"
	"slices"
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

func validatePhaseRequests(phase Phase, batch int, start, end time.Time) error {
	requests := make(map[string]struct{}, len(phase.Requests))
	byBatch := make(map[uint64][]*PhaseRequest, len(phase.NativeSamples))
	for index := range phase.Requests {
		request := &phase.Requests[index]
		progress := &request.Response.Progress
		if request.RequestID == "" || request.RecordID == "" || request.Response.ID != request.RequestID ||
			progress.RequestID != request.RequestID || progress.BatchID == 0 || progress.BatchSize != batch ||
			!request.Response.ExecutionComplete || !progress.ExecutionComplete || progress.ServiceSample == nil ||
			request.ArrivalAt.IsZero() || request.SentAt.Before(request.ArrivalAt) || !request.CompletedAt.After(request.SentAt) || !finitePositive(request.LatencyMilliseconds) {
			return errors.New("request identity, completion or load-node timing is invalid")
		}
		if _, found := requests[request.RequestID]; found {
			return errors.New("duplicate request identity")
		}
		http := progress.NativeHTTP
		if http == nil || http.HandlerEnteredAt.IsZero() || http.ReceivedNanos == nil || http.EnqueueNanos == nil || http.FinishedNanos == nil ||
			*http.ReceivedNanos < 0 || *http.EnqueueNanos < *http.ReceivedNanos || *http.FinishedNanos < *http.EnqueueNanos {
			return errors.New("native request receive/queue/finish boundaries are incomplete")
		}
		requests[request.RequestID] = struct{}{}
		byBatch[progress.BatchID] = append(byBatch[progress.BatchID], request)
	}
	for _, sample := range phase.NativeSamples {
		members := byBatch[sample.BatchID]
		if len(members) != batch || sample.StartedAt.IsZero() || !sample.FinishedAt.After(sample.StartedAt) || sample.EarliestEnqueuedAt.IsZero() ||
			sample.StartedAt.Before(start) || sample.FinishedAt.After(end) ||
			sample.LatestEnqueuedAt.Before(sample.EarliestEnqueuedAt) || sample.LatestEnqueuedAt.After(sample.StartedAt) {
			return errors.New("native batch membership or anchors are incomplete")
		}
		var recordIDs []string
		for _, request := range members {
			progress := &request.Response.Progress
			if len(progress.BatchRequestIDs) != batch || !slices.Contains(progress.BatchRequestIDs, request.RequestID) ||
				progress.BatchStartedAt == nil || progress.BatchFinishedAt == nil ||
				!progress.BatchStartedAt.Equal(sample.StartedAt) || !progress.BatchFinishedAt.Equal(sample.FinishedAt) ||
				progress.ServiceSample.ServiceDurationNanos != sample.ServiceDurationNanos || progress.ServiceSample.TrialID != sample.TrialID {
				return errors.New("request batch membership or service differs from batch sample")
			}
			recordIDs = append(recordIDs, request.RecordID)
		}
		got, want := slices.Clone(recordIDs), slices.Clone(sample.RecordIDs)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return errors.New("batch record population differs from completed requests")
		}
		delete(byBatch, sample.BatchID)
	}
	if len(byBatch) != 0 {
		return errors.New("completed requests have no physical batch sample")
	}
	return nil
}
