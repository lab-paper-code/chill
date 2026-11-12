// Command profile-holdout is the numerical bridge for profile_holdout.py.
// JSON requests and responses are streamed on stdin/stdout; no live I/O occurs.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"gonum.org/v1/gonum/stat"
)

type request struct {
	Trials []trial                      `json:"trials"`
	Idle   []derivation.IdleObservation `json:"idle"`
}

// Offering exposure is load-node monotonic time, separate from power windows.
type phase struct {
	derivation.OperatingPointPhase
	SentInputs   int     `json:"sentInputs"`
	IssueSeconds float64 `json:"issueElapsedSecondsMonotonic"`
}

type trial struct {
	derivation.Trial
	Phases []phase `json:"phases"`
}

type phasePower struct {
	derivation.PhasePower
	SentRatePerSecond *float64 `json:"sentRatePerSecond"`
}

type energyTrial struct {
	derivation.EnergyTrial
	PhasePower []phasePower `json:"phasePower"`
}

type profile struct {
	Batch                          int                        `json:"batch"`
	IdleWatts                      float64                    `json:"idleWatts"`
	EnergyPerInputJoules           float64                    `json:"energyPerInputJoules"`
	DispatchOccupancyNanosPerInput float64                    `json:"dispatchOccupancyNanosPerInput"`
	OperationalDispatchNanos       int64                      `json:"operationalDispatchNanos"`
	ServiceSamples                 []firstfloor.ServiceSample `json:"serviceSamples"`
}

type result struct {
	Profile    profile          `json:"profile"`
	Trials     []energyTrial    `json:"trials"`
	Saturation []map[string]any `json:"saturation"`
}

func main() {
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var input request
		if err := decoder.Decode(&input); err != nil {
			if err == io.EOF {
				return
			}
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		out, err := analyze(input)
		if err != nil {
			if encodeErr := encoder.Encode(map[string]string{"error": err.Error()}); encodeErr != nil {
				os.Exit(1)
			}
			continue
		}
		if err := encoder.Encode(out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func analyze(input request) (result, error) {
	var out result
	if len(input.Trials) == 0 || len(input.Idle) == 0 {
		return out, fmt.Errorf("training and model-loaded idle evidence required")
	}
	idle := make([]float64, len(input.Idle))
	for j, observation := range input.Idle {
		trial, err := derivation.MeasureIdle(observation)
		if err != nil {
			return out, err
		}
		idle[j] = trial.ModelLoadedIdleMeanWallWatts
	}
	out.Profile.Batch = input.Trials[0].Batch
	out.Profile.IdleWatts = stat.Mean(idle, nil)
	baseline := derivation.IdleBaseline{ModelLoadedIdleMeanWallWatts: out.Profile.IdleWatts}
	energy := make([]float64, len(input.Trials))
	var occupancy []float64
	for j, supplied := range input.Trials {
		trial := supplied.Trial
		for _, phase := range supplied.Phases {
			trial.Phases = append(trial.Phases, phase.OperatingPointPhase)
		}
		if trial.Batch != out.Profile.Batch {
			return out, fmt.Errorf("one fixed batch required")
		}
		derived, err := derivation.IncrementalWorkTrial(trial, baseline)
		if err != nil {
			return out, err
		}
		row := energyTrial{EnergyTrial: derived}
		for k, power := range derived.PhasePower {
			observation := supplied.Phases[k]
			var rate *float64
			if observation.Kind != "saturation" {
				if observation.SentInputs != observation.CompletedItems || observation.SentInputs <= 0 ||
					observation.IssueSeconds <= 0 || math.IsNaN(observation.IssueSeconds) || math.IsInf(observation.IssueSeconds, 0) {
					return out, fmt.Errorf("positive offering exposure and complete sent inputs required")
				}
				actual := float64(observation.SentInputs) / observation.IssueSeconds
				rate = &actual
			}
			row.PhasePower = append(row.PhasePower, phasePower{PhasePower: power, SentRatePerSecond: rate})
		}
		out.Trials = append(out.Trials, row)
		energy[j] = derived.IncrementalWorkJoulesPerItem
		occupancy = append(occupancy, derived.DispatchOccupancyNanosPerInput...)
		for _, sample := range derived.NativeSamples {
			out.Profile.ServiceSamples = append(out.Profile.ServiceSamples, sample.ServiceSample)
		}
		for _, phase := range trial.Phases {
			if phase.Kind != "saturation" {
				continue
			}
			var cycles, services []float64
			for k := 1; k < len(phase.NativeSamples); k++ {
				pair := phase.NativeSamples[k-1 : k+1]
				if len(derivation.SaturationDispatchOccupancy(pair, trial.Batch)) == 0 {
					continue
				}
				cycles = append(cycles, float64(pair[1].StartedAt.Sub(pair[0].StartedAt))/1e6)
				services = append(services, float64(pair[0].ServiceDurationNanos)/1e6)
			}
			if len(cycles) > 0 {
				out.Saturation = append(out.Saturation, map[string]any{"trialID": trial.TrialID, "id": phase.ID,
					"pairs": len(cycles), "measuredCycleMS": stat.Mean(cycles, nil), "precedingServiceMS": stat.Mean(services, nil)})
			}
		}
	}
	if len(occupancy) == 0 {
		return out, fmt.Errorf("training saturation has no eligible consecutive backlogged pair")
	}
	out.Profile.EnergyPerInputJoules = stat.Mean(energy, nil)
	out.Profile.DispatchOccupancyNanosPerInput = stat.Mean(occupancy, nil)
	out.Profile.OperationalDispatchNanos = int64(math.Max(0, out.Profile.DispatchOccupancyNanosPerInput))
	return out, nil
}
