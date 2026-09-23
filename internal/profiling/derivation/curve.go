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

// Curve is opaque so only points matching one verified plan and its exact
// baseline can reach a campaign decision.
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
	baselines, err := validateCurveBaselines(plan, baselinesByState)
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
	for _, batchInput := range batches {
		if plannedBatchIndex >= len(plannedBatchSizes) || batchInput.Batch != plannedBatchSizes[plannedBatchIndex] {
			return curvePass{}, fmt.Errorf("curve pass %q batches must follow the batch sizes recorded in the curve plan", input.Role)
		}
		point, err := deriveCurvePoint(batchInput, plan.state, baselines, policy, seenTrials)
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
	state VerifiedCurveState,
	baselines map[string]IdleBaseline,
	policy PointPolicy,
	seenTrials map[string]struct{},
) (Point, error) {
	if len(input.Trials) == 0 {
		return Point{}, fmt.Errorf("batch %d has no completed trials", input.Batch)
	}
	baseline, exists := baselines[state.StateIdentity]
	if !exists {
		return Point{}, fmt.Errorf("batch %d has no exact-state idle baseline", input.Batch)
	}
	completed := make([]EnergyTrial, 0, len(input.Trials))
	for _, run := range input.Trials {
		if run.Batch != input.Batch || run.StateIdentity != state.StateIdentity ||
			run.EnvironmentIdentity != state.EnvironmentIdentity ||
			run.MeasurementProtocolIdentity != state.ActiveProtocolIdentity {
			return Point{}, fmt.Errorf("batch %d trial does not match the plan's verified execution state", input.Batch)
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

func validateCurveBaselines(
	plan VerifiedCurvePlan,
	input map[string]IdleBaseline,
) (map[string]IdleBaseline, error) {
	result := make(map[string]IdleBaseline, len(input))
	state := plan.state
	for key, baseline := range input {
		if key != state.StateIdentity || key != baseline.StateIdentity ||
			baseline.EnvironmentIdentity != state.EnvironmentIdentity ||
			baseline.MeasurementProtocolIdentity != state.IdleProtocolIdentity {
			return nil, fmt.Errorf("idle baseline key %q does not match the plan's verified execution state", key)
		}
		if err := validateIdleBaseline(baseline); err != nil {
			return nil, fmt.Errorf("idle baseline key %q is incomplete", key)
		}
		result[key] = cloneIdleBaseline(baseline)
	}
	return result, nil
}

func cloneIdleBaseline(baseline IdleBaseline) IdleBaseline {
	baseline.Trials = append([]IdleTrial(nil), baseline.Trials...)
	return baseline
}

func clonePoint(point Point) Point {
	point.Trials = append([]EnergyTrial(nil), point.Trials...)
	point.Confidence = clonePointConfidence(point.Confidence)
	return point
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
