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
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"gonum.org/v1/gonum/stat"
)

type request struct {
	Trials            []trial                      `json:"trials"`
	Idle              []derivation.IdleObservation `json:"idle"`
	Heldout           []heldoutPhase               `json:"heldout"`
	SchedulerSettings *serving.SchedulerSettings   `json:"schedulerSettings"`
	SupportTrials     []trial                      `json:"supportTrials"`
	SupportIdle       []derivation.IdleObservation `json:"supportIdle"`
}

// Diagnostic power and scheduled-input rate share the explicit offering window.
type phase struct {
	derivation.OperatingPointPhase
	ScheduledInputs   int       `json:"scheduledInputs"`
	OfferingSeconds   float64   `json:"issueElapsedSecondsMonotonic"`
	OfferingStartedAt time.Time `json:"offeringStartedAt"`
	OfferingEndedAt   time.Time `json:"offeringEndedAt"`
}

// Arrivals are client scheduledAt timestamps, never dispatches or completions.
type heldoutPhase struct {
	TrialID                    string        `json:"trialID"`
	PhaseID                    string        `json:"phaseID"`
	Arrivals                   []time.Time   `json:"arrivals"`
	SLOPercentile              float64       `json:"sloPercentile"`
	SLOTargetLatency           time.Duration `json:"sloTargetLatencyNanos"`
	RequestLifetimeSeconds     float64       `json:"requestLifetimeSeconds"`
	DeliveryMarginMilliseconds float64       `json:"deliveryMarginMilliseconds"`
}

type latencyPrediction struct {
	TrialID       string                         `json:"trialID"`
	PhaseID       string                         `json:"phaseID"`
	ExpectedNanos *float64                       `json:"expectedNanos"`
	FeedbackScore *serving.FeedbackWorkloadScore `json:"feedbackScore,omitempty"`
	Reasons       []string                       `json:"reasons,omitempty"`
}

type trial struct {
	derivation.Trial
	Phases []phase `json:"phases"`
}

type phasePower struct {
	derivation.PhasePower
	ScheduledRatePerSecond *float64 `json:"scheduledRatePerSecond"`
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
	Profile    profile             `json:"profile"`
	Trials     []energyTrial       `json:"trials"`
	Saturation []map[string]any    `json:"saturation"`
	Latency    []latencyPrediction `json:"latency"`
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
	if len(input.Heldout) > 0 && input.SchedulerSettings == nil {
		return out, fmt.Errorf("frozen schedulerSettings required for heldout latency evaluation")
	}
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
		for k := range derived.PhasePower {
			observation := supplied.Phases[k]
			if observation.ScheduledInputs <= 0 || observation.OfferingSeconds <= 0 ||
				math.IsNaN(observation.OfferingSeconds) || math.IsInf(observation.OfferingSeconds, 0) {
				return out, fmt.Errorf("positive offering exposure and scheduled inputs required")
			}
			offering := observation.OperatingPointPhase
			offering.WindowStart, offering.WindowEnd = observation.OfferingStartedAt, observation.OfferingEndedAt
			power, err := derivation.MeasurePhasePower(offering)
			if err != nil {
				return out, err
			}
			power.TrialID = trial.TrialID
			rate := float64(observation.ScheduledInputs) / observation.OfferingSeconds
			row.PhasePower = append(row.PhasePower, phasePower{PhasePower: power, ScheduledRatePerSecond: &rate})
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
	// Other-batch populations remain separate physical trials. Derive each
	// with the same API, then assemble one measured catalog without inventing B=1.
	profiles := []profile{out.Profile}
	support := make(map[int][]trial)
	for _, supplied := range input.SupportTrials {
		if supplied.Batch == out.Profile.Batch {
			return out, fmt.Errorf("supportTrials must contain only other batches")
		}
		support[supplied.Batch] = append(support[supplied.Batch], supplied)
	}
	var supportBatches []int
	for batch := range support {
		supportBatches = append(supportBatches, batch)
	}
	sort.Ints(supportBatches)
	for _, batch := range supportBatches {
		derived, err := analyze(request{Trials: support[batch], Idle: input.SupportIdle})
		if err != nil {
			return out, fmt.Errorf("support batch %d: %w", batch, err)
		}
		profiles = append(profiles, derived.Profile)
	}
	var batches []serving.BatchProfile
	if len(input.Heldout) > 0 {
		batches = make([]serving.BatchProfile, 0, len(profiles))
		for _, measured := range profiles {
			distribution, err := firstfloor.NewServiceDistribution(measured.ServiceSamples)
			if err != nil {
				return out, err
			}
			batches = append(batches, serving.BatchProfile{
				Batch: measured.Batch, MemoryBytes: 1, EnergyPerInputJoules: measured.EnergyPerInputJoules,
				ServiceSamples: measured.ServiceSamples, ServiceDistribution: distribution,
			})
		}
		sort.Slice(batches, func(i, j int) bool { return batches[i].Batch < batches[j].Batch })
	}
	out.Latency = make([]latencyPrediction, 0, len(input.Heldout))
	for _, heldout := range input.Heldout {
		predicted, err := predictLatency(out.Profile, batches, heldout, input.SchedulerSettings.Clone())
		if err != nil {
			return out, err
		}
		out.Latency = append(out.Latency, predicted)
	}
	return out, nil
}

func predictLatency(training profile, batches []serving.BatchProfile, heldout heldoutPhase, settings serving.SchedulerSettings) (latencyPrediction, error) {
	out := latencyPrediction{TrialID: heldout.TrialID, PhaseID: heldout.PhaseID}
	if heldout.TrialID == "" || heldout.PhaseID == "" || len(heldout.Arrivals) == 0 {
		return out, fmt.Errorf("heldout trialID, phaseID and client scheduledAt arrivals required")
	}
	if heldout.SLOPercentile <= 0 || heldout.SLOPercentile > 100 || math.IsNaN(heldout.SLOPercentile) ||
		math.IsInf(heldout.SLOPercentile, 0) || heldout.SLOTargetLatency <= 0 ||
		heldout.RequestLifetimeSeconds <= 0 || math.IsNaN(heldout.RequestLifetimeSeconds) ||
		math.IsInf(heldout.RequestLifetimeSeconds, 0) ||
		heldout.RequestLifetimeSeconds >= float64(math.MaxInt64)/float64(time.Second) ||
		heldout.DeliveryMarginMilliseconds < 0 || math.IsNaN(heldout.DeliveryMarginMilliseconds) ||
		math.IsInf(heldout.DeliveryMarginMilliseconds, 0) || len(heldout.Arrivals) > math.MaxInt32 {
		return out, fmt.Errorf("heldout SLO, positive request lifetime and nonnegative delivery margin required")
	}
	start, last := heldout.Arrivals[0], heldout.Arrivals[0]
	for _, scheduledAt := range heldout.Arrivals {
		if scheduledAt.IsZero() {
			return out, fmt.Errorf("heldout client scheduledAt must be nonzero")
		}
		if scheduledAt.Before(start) {
			start = scheduledAt
		}
		if scheduledAt.After(last) {
			last = scheduledAt
		}
	}
	if err := settings.Validate(); err != nil {
		return out, fmt.Errorf("frozen schedulerSettings: %w", err)
	}
	// Anchor the unchanged scheduler observation window after the final scheduledAt.
	// Reject a short window instead of silently discarding heldout arrivals.
	at := last.Add(time.Nanosecond)
	if at.Sub(start) > settings.ObservationWindow {
		return out, fmt.Errorf("schedulerSettings observationWindowNanos must cover all heldout scheduledAt arrivals")
	}
	start = at.Add(-settings.ObservationWindow)
	lifetime := time.Duration(heldout.RequestLifetimeSeconds * float64(time.Second))
	definition := &serving.Definition{
		UID: "heldout", Model: serving.Model{Name: "model", UID: "model",
			Paths: []serving.ModelPath{{Name: "cpu", Artifact: "model", Files: []string{"/model"}, RuntimeFamily: "firstfloor", Backend: "ort-cpu"}}},
		SLO:             serving.SLO{Percentile: heldout.SLOPercentile, TargetLatency: heldout.SLOTargetLatency},
		Admission:       serving.Admission{Policy: serving.AdmissionPolicyDelay, MaxQueuedRequests: int32(len(heldout.Arrivals))},
		RequestLifetime: &lifetime,
	}
	node := serving.SchedulerNode{
		Node: serving.NodeReference{Name: "offline", UID: "offline"}, PowerMode: "fixed",
		Ready: true, MemoryBytes: math.MaxUint64, ObservedAt: at,
	}
	// Resource units are nonbinding offline placement bookkeeping; only training
	// service, incremental energy and dispatch occupancy enter numerical prediction.
	measured := &serving.Profile{
		ID: "training", NodeName: node.Node.Name, IdleBaselineWatts: training.IdleWatts,
		Condition: derivation.CurvePlanSpec{
			LogicalModel: "model", NodeUID: node.Node.UID, PowerMode: node.PowerMode,
			RuntimeFamily: "firstfloor", Backend: "ort-cpu",
			Artifact: derivation.CurveArtifactContract{Name: "model", ExecutionPath: "cpu", Files: []string{"/model"}},
		},
		Batches: batches,
	}
	instance := serving.SchedulerInstance{
		ID: "fixed", Node: node.Node, Profile: measured, WorkloadUID: definition.UID,
		Allocation: serving.SchedulerAllocation{MemoryBytes: math.MaxUint64}, Ready: true,
		Routable: map[string]bool{definition.UID: true}, Capacity: len(heldout.Arrivals),
		Weight: 1, MaxBatch: training.Batch, ObservedAt: at, ArrivalAgesMS: map[string][]float64{},
	}
	// Keep frozen planner numerics and comparison/observation periods. Bind the
	// offline workload, measured dispatch occupancy and recorded delivery margin.
	settings.AutonomousWorkloads = []string{definition.UID}
	settings.DefaultDispatchOccupancyNanos = training.OperationalDispatchNanos
	settings.CompletionMarginMS = heldout.DeliveryMarginMilliseconds
	snapshot := serving.SchedulerSnapshot{
		At: at, ObservationEnd: at, ObservedSince: start,
		Workloads: []*serving.Definition{definition}, Nodes: []serving.SchedulerNode{node},
		Instances: []serving.SchedulerInstance{instance},
		Options:   []serving.SchedulerOption{{Instance: instance, CompatibleUIDs: []string{definition.UID}}},
		Routing:   map[string]map[string]int32{definition.UID: {instance.ID: 1}},
		Arrivals:  make([]serving.SchedulerArrival, len(heldout.Arrivals)),
	}
	for j, scheduledAt := range heldout.Arrivals {
		snapshot.Arrivals[j] = serving.SchedulerArrival{
			ID: fmt.Sprintf("scheduled-%09d", j), At: scheduledAt, Definition: definition,
			WorkloadUID: definition.UID, Accepted: true, Compute: true, Lifetime: lifetime,
		}
	}
	decision, err := serving.EvaluateScheduler(context.Background(), settings, snapshot)
	if err != nil {
		return out, err
	}
	out.Reasons = append(out.Reasons, decision.Reasons...)
	out.Reasons = append(out.Reasons, decision.Keep.Reasons...)
	for _, estimate := range decision.Keep.SLO {
		if estimate.UID == definition.UID {
			out.ExpectedNanos = estimate.ExpectedNanos
			break
		}
	}
	for j := range decision.Keep.Feedback {
		if decision.Keep.Feedback[j].UID == definition.UID {
			out.FeedbackScore = &decision.Keep.Feedback[j]
			break
		}
	}
	if out.ExpectedNanos == nil && len(out.Reasons) == 0 && out.FeedbackScore == nil {
		out.Reasons = []string{"scheduler returned no keep SLO estimate"}
	}
	return out, nil
}
