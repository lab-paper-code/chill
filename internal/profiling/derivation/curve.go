package profilederivation

import (
	"errors"
	"fmt"
	"sort"
)

type PassRole string

const (
	PassSearch PassRole = "search"
)

type PassBatch struct {
	Batch  int     `json:"batch"`
	Trials []Trial `json:"runs,omitempty"`
}

type Pass struct {
	Role    PassRole    `json:"role"`
	Batches []PassBatch `json:"batches"`
}

type curvePass struct {
	role   PassRole
	points []Point
}

// Curve retains points and the idle baseline assembled from condition-bound
// evidence for one verified plan.
type Curve struct {
	plan      VerifiedCurvePlan
	passes    []curvePass
	baselines map[string]IdleBaseline
}

func DeriveCurve(
	passes []Pass,
	plan VerifiedCurvePlan,
	baselinesByState map[string]IdleBaseline,
	policy PointPolicy,
) (Curve, error) {
	if err := plan.requireVerified(); err != nil {
		return Curve{}, err
	}
	if err := validatePointPolicy(policy); err != nil {
		return Curve{}, err
	}
	if len(passes) != 1 || passes[0].Role != PassSearch {
		return Curve{}, errors.New("curve requires exactly one search pass")
	}
	baselines, err := curveBaselines(baselinesByState)
	if err != nil {
		return Curve{}, err
	}

	curve := Curve{plan: plan, baselines: baselines}
	seenTrials := make(map[string]struct{})
	for _, inputPass := range passes {
		derivedPass, err := deriveCurvePass(inputPass, plan, baselines, policy, seenTrials)
		if err != nil {
			return Curve{}, err
		}
		curve.passes = append(curve.passes, derivedPass)
	}
	return curve, nil
}

func deriveCurvePass(
	input Pass,
	plan VerifiedCurvePlan,
	baselines map[string]IdleBaseline,
	policy PointPolicy,
	seenTrials map[string]struct{},
) (curvePass, error) {
	if input.Role != PassSearch {
		return curvePass{}, fmt.Errorf("unsupported curve pass role %q", input.Role)
	}
	if len(input.Batches) == 0 {
		return curvePass{}, fmt.Errorf("curve pass %q has no batches", input.Role)
	}
	batches := append([]PassBatch(nil), input.Batches...)
	sort.Slice(batches, func(i, j int) bool { return batches[i].Batch < batches[j].Batch })
	plannedBatchSizes := plan.recorded.record.Shared.PlannedBatchSizes
	if batches[0].Batch != plannedBatchSizes[0] {
		return curvePass{}, errors.New("search pass must start at the first planned batch size")
	}
	result := curvePass{role: input.Role}
	plannedBatchIndex := sort.SearchInts(plannedBatchSizes, batches[0].Batch)
	var baseline IdleBaseline
	for _, value := range baselines {
		baseline = value
	}
	for _, batchInput := range batches {
		if plannedBatchIndex >= len(plannedBatchSizes) || batchInput.Batch != plannedBatchSizes[plannedBatchIndex] {
			return curvePass{}, fmt.Errorf("curve pass %q batches must follow the batch sizes recorded in the curve plan", input.Role)
		}
		point, err := deriveCurvePoint(batchInput, baseline, policy, seenTrials)
		if err != nil {
			return curvePass{}, err
		}
		result.points = append(result.points, point)
		plannedBatchIndex++
	}
	return result, nil
}

func deriveCurvePoint(
	input PassBatch,
	baseline IdleBaseline,
	policy PointPolicy,
	seenTrials map[string]struct{},
) (Point, error) {
	if len(input.Trials) == 0 {
		return Point{}, fmt.Errorf("batch %d has no completed trials", input.Batch)
	}
	completed := make([]EnergyTrial, 0, len(input.Trials))
	for _, run := range input.Trials {
		if run.Batch != input.Batch {
			return Point{}, fmt.Errorf("batch %d trial does not match its curve position", input.Batch)
		}
		if run.TrialID == "" {
			return Point{}, fmt.Errorf("batch %d contains an empty trial identity", input.Batch)
		}
		if _, exists := seenTrials[run.TrialID]; exists {
			return Point{}, fmt.Errorf("trial %q appears in more than one curve position", run.TrialID)
		}
		seenTrials[run.TrialID] = struct{}{}
		trial, err := IncrementalWorkTrial(run, baseline)
		if err != nil {
			return Point{}, fmt.Errorf("derive batch %d trial %q: %w", input.Batch, run.TrialID, err)
		}
		completed = append(completed, trial)
	}
	point, err := Aggregate(completed, policy)
	if err != nil {
		return Point{}, fmt.Errorf("aggregate batch %d: %w", input.Batch, err)
	}
	return point, nil
}

func (c Curve) PlanName() string { return c.plan.Name() }

func (c Curve) Points() []Point {
	var result []Point
	for _, pass := range c.passes {
		for _, point := range pass.points {
			result = append(result, clonePoint(point))
		}
	}
	return result
}

func (c Curve) IdleBaselinesByState() map[string]IdleBaseline {
	result := make(map[string]IdleBaseline, len(c.baselines))
	for state, baseline := range c.baselines {
		result[state] = cloneIdleBaseline(baseline)
	}
	return result
}

func (c Curve) pass(role PassRole) (curvePass, bool) {
	for _, pass := range c.passes {
		if pass.role == role {
			result := curvePass{role: pass.role, points: make([]Point, len(pass.points))}
			for index, point := range pass.points {
				result.points[index] = clonePoint(point)
			}
			return result, true
		}
	}
	return curvePass{}, false
}

func (p VerifiedCurvePlan) requireVerified() error {
	return p.recorded.requireInitialized()
}

// curveBaselines copies the one baseline already assembled from condition-bound
// idle evidence. Identity strings are lookup labels, not validation gates.
func curveBaselines(input map[string]IdleBaseline) (map[string]IdleBaseline, error) {
	if len(input) != 1 {
		return nil, errors.New("curve requires exactly one idle baseline")
	}
	result := make(map[string]IdleBaseline, 1)
	for key, baseline := range input {
		result[key] = cloneIdleBaseline(baseline)
	}
	return result, nil
}

func cloneIdleBaseline(baseline IdleBaseline) IdleBaseline {
	baseline.Trials = append([]IdleTrial(nil), baseline.Trials...)
	return baseline
}

func clonePoint(point Point) Point {
	point.Trials = cloneEnergyTrials(point.Trials)
	point.Confidence = clonePointConfidence(point.Confidence)
	return point
}

func cloneEnergyTrials(trials []EnergyTrial) []EnergyTrial {
	cloned := append([]EnergyTrial(nil), trials...)
	for index := range cloned {
		cloned[index].ClientLatencyMilliseconds = append([]float64(nil), trials[index].ClientLatencyMilliseconds...)
		cloned[index].NativeSamples = append([]NativeServiceSample(nil), trials[index].NativeSamples...)
	}
	return cloned
}

func clonePointConfidence(confidence PointConfidence) PointConfidence {
	if confidence.VarianceOfMean != nil {
		variance := *confidence.VarianceOfMean
		confidence.VarianceOfMean = &variance
	}
	if confidence.EffectiveDegreesOfFreedom != nil {
		degreesOfFreedom := *confidence.EffectiveDegreesOfFreedom
		confidence.EffectiveDegreesOfFreedom = &degreesOfFreedom
	}
	return confidence
}
