package profilederivation

import (
	"errors"
	"math"
	"time"

	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
)

// measurePower uses the full runtime window and all available power samples.
// It linearly interpolates between samples and holds the endpoint values out
// to the window boundaries. Read failures and environment observations remain
// in raw evidence; they do not decide whether an experiment is usable.
func measurePower(start, end time.Time, samples []Sample) (float64, error) {
	if !end.After(start) {
		return 0, errors.New("measurement window must be positive")
	}
	points := make([]powerwindow.Sample, len(samples))
	for i, sample := range samples {
		points[i] = powerwindow.Sample{ObservedAt: sample.ObservedAt, Watts: sample.Watts}
	}
	duration := end.Sub(start)
	return powerwindow.Integrate(powerwindow.Window{Start: start, End: end, Samples: points}, powerwindow.Policy{
		MaximumBoundaryDistance: duration, MaximumGap: duration,
	})
}

func measureTrial(trial Trial) (float64, error) {
	if trial.Batch < 1 || trial.CompletedCalls < 1 || trial.CompletedItems != trial.CompletedCalls*trial.Batch || len(trial.LatencyMilliseconds) != trial.CompletedCalls {
		return 0, errors.New("trial counts or latency samples are invalid")
	}
	for _, value := range trial.LatencyMilliseconds {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return 0, errors.New("latency must be positive and finite")
		}
	}
	if len(trial.NativeSamples) > 0 {
		if len(trial.NativeSamples) != trial.CompletedCalls {
			return 0, errors.New("native samples are not correlated with client calls")
		}
		seen := make(map[uint64]bool, len(trial.NativeSamples))
		for index, sample := range trial.NativeSamples {
			if sample.BatchID == 0 || seen[sample.BatchID] || sample.BatchSize != trial.Batch {
				return 0, errors.New("native batch identity is invalid")
			}
			seen[sample.BatchID] = true
			for _, value := range []float64{sample.RunnerServiceMilliseconds, sample.BackendCallMilliseconds, sample.NativeRequestMilliseconds} {
				if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
					return 0, errors.New("native span must be positive and finite")
				}
			}
			if sample.BackendCallMilliseconds > sample.RunnerServiceMilliseconds ||
				sample.RunnerServiceMilliseconds > sample.NativeRequestMilliseconds ||
				sample.NativeRequestMilliseconds > trial.LatencyMilliseconds[index] {
				return 0, errors.New("native overlapping span order is invalid")
			}
		}
	}
	return measurePower(trial.WindowStart, trial.WindowEnd, trial.Samples)
}

func MeasureIdle(observation IdleObservation) (IdleTrial, error) {
	if observation.InferenceCountAfterModelLoad != 0 || !observation.ModelResidentAtEnd {
		return IdleTrial{}, errors.New("idle measurement requires a resident model and zero inference calls")
	}
	joules, err := measurePower(observation.WindowStart, observation.WindowEnd, observation.Samples)
	if err != nil {
		return IdleTrial{}, err
	}
	return IdleTrial{
		StateIdentity: observation.StateIdentity, EnvironmentIdentity: observation.EnvironmentIdentity,
		MeasurementProtocolIdentity: observation.MeasurementProtocolIdentity, TrialID: observation.TrialID,
		RawEvidence: observation.RawEvidence, ModelLoadedIdleMeanWallWatts: joules / observation.WindowEnd.Sub(observation.WindowStart).Seconds(),
	}, nil
}
