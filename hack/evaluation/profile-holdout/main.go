// Command profile-holdout is the numerical bridge for profile_holdout.py.
// JSON requests and responses are streamed on stdin/stdout; no live I/O occurs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"gonum.org/v1/gonum/stat"
)

type traceInput struct {
	Arrivals         []time.Time `json:"arrivals"`
	InitialIdleNanos *int64      `json:"initialIdleNanos"`
	Percentile       float64     `json:"percentile"`
	TargetNanos      int64       `json:"targetNanos"`
	LifetimeSeconds  float64     `json:"lifetimeSeconds"`
	MarginMS         float64     `json:"marginMS"`
}

type request struct {
	Trials []derivation.Trial           `json:"trials"`
	Idle   []derivation.IdleObservation `json:"idle"`
	Tests  []traceInput                 `json:"tests"`
	Seed   uint64                       `json:"seed"`
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
	Profile    profile                  `json:"profile"`
	Trials     []derivation.EnergyTrial `json:"trials"`
	Traces     []serving.FeedbackTrace  `json:"traces"`
	Saturation []map[string]any         `json:"saturation"`
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
	for j, trial := range input.Trials {
		if trial.Batch != out.Profile.Batch {
			return out, fmt.Errorf("one fixed batch required")
		}
		derived, err := derivation.IncrementalWorkTrial(trial, baseline)
		if err != nil {
			return out, err
		}
		out.Trials = append(out.Trials, derived)
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
	distribution, err := firstfloor.NewServiceDistribution(out.Profile.ServiceSamples)
	if err != nil {
		return out, err
	}
	batch := serving.BatchProfile{Batch: out.Profile.Batch, ServiceSamples: out.Profile.ServiceSamples,
		ServiceDistribution: distribution, EnergyPerInputJoules: out.Profile.EnergyPerInputJoules}
	for _, test := range input.Tests {
		trace, err := replay(test, batch, out.Profile.OperationalDispatchNanos, input.Seed)
		if err != nil {
			return out, err
		}
		out.Traces = append(out.Traces, trace)
	}
	return out, nil
}

func replay(input traceInput, batch serving.BatchProfile, occupancy int64, seed uint64) (serving.FeedbackTrace, error) {
	if len(input.Arrivals) == 0 || len(input.Arrivals)%batch.Batch != 0 || input.TargetNanos <= 0 ||
		input.Percentile <= 0 || input.Percentile >= 100 || input.LifetimeSeconds <= 0 || input.MarginMS < 0 {
		return serving.FeedbackTrace{}, fmt.Errorf("invalid finite replay trace")
	}
	start, end := input.Arrivals[0], input.Arrivals[0]
	for _, at := range input.Arrivals {
		if at.IsZero() {
			return serving.FeedbackTrace{}, fmt.Errorf("arrival timestamp required")
		}
		if at.Before(start) {
			start = at
		}
		if at.After(end) {
			end = at
		}
	}
	end = end.Add(time.Nanosecond) // half-open window includes the final arrival once
	span := end.Sub(start)
	settings := serving.SchedulerSettings{ObservationWindow: span, ComparisonPeriod: span,
		CompletionMarginMS: input.MarginMS, DefaultDispatchOccupancyNanos: occupancy}
	native := &modelcompat.FirstfloorSettings{MaxBatchSize: int32(batch.Batch), MaxQueueSize: int32(len(input.Arrivals) + batch.Batch)}
	p := &serving.Profile{ID: "holdout-training", Condition: derivation.CurvePlanSpec{Firstfloor: native,
		PlannedBatchSizes: []int{batch.Batch}}, Batches: []serving.BatchProfile{batch}}
	definition := &serving.Definition{UID: "holdout", SLO: serving.SLO{Percentile: input.Percentile, TargetLatency: time.Duration(input.TargetNanos)},
		Model:     serving.Model{Paths: []serving.ModelPath{{Firstfloor: native}}},
		Admission: serving.Admission{Policy: serving.AdmissionPolicyDelay, MaxQueuedRequests: int32(len(input.Arrivals) + batch.Batch)}}
	instance := serving.SchedulerInstance{ID: "holdout", WorkloadUID: definition.UID, Profile: p,
		Ready: true, Capacity: len(input.Arrivals) + batch.Batch, MaxBatch: batch.Batch, FixedBatchSize: batch.Batch,
		Routable: map[string]bool{definition.UID: true}, Weight: 1, ObservedAt: start,
		ArrivalAgesMS: map[string][]float64{}, LastServiceEndAgeNanos: input.InitialIdleNanos}
	arrivals := make([]serving.SchedulerArrival, len(input.Arrivals))
	for j, at := range input.Arrivals {
		arrivals[j] = serving.SchedulerArrival{ID: fmt.Sprintf("r%09d", j), At: at, Definition: definition,
			Compute: true, Accepted: true, WorkloadUID: definition.UID, Lifetime: time.Duration(input.LifetimeSeconds * 1e9)}
	}
	snapshot := serving.SchedulerSnapshot{At: start, ObservationEnd: end, Workloads: []*serving.Definition{definition},
		Instances: []serving.SchedulerInstance{instance}, Routing: map[string]map[string]int32{definition.UID: {instance.ID: 1}}, Arrivals: arrivals}
	candidate := serving.SchedulerCandidate{ID: "keep", Keep: true, Instances: snapshot.Instances, Routing: snapshot.Routing}
	return serving.ReplayFeedbackTrace(context.Background(), settings, snapshot, candidate, seed)
}
