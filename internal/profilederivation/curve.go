package profilederivation

import (
	"errors"
	"fmt"
	"sort"
)

type PassRole string

const (
	PassDiscovery    PassRole = "discovery"
	PassConfirmatory PassRole = "confirmatory"
)

// CurveRun keeps an exact projected trial beside the admission result that
// authorized its use.
type CurveRun struct {
	Trial     Trial           `json:"trial"`
	Admission AdmissionResult `json:"admission"`
}

// PassBatch either carries the admitted Runs needed for one point or an
// explicit capability boundary. An empty Runs slice alone is not a boundary.
type PassBatch struct {
	Batch              int        `json:"batch"`
	Runs               []CurveRun `json:"runs,omitempty"`
	CapabilityBoundary bool       `json:"capabilityBoundary,omitempty"`
}

type Pass struct {
	Role    PassRole    `json:"role"`
	Batches []PassBatch `json:"batches"`
}

type curvePass struct {
	role               PassRole
	points             []Point
	capabilityBoundary *int
}

// Curve is opaque so only exact-family, exact-baseline points can reach a
// campaign decision.
type Curve struct {
	family    CurveFamily
	passes    []curvePass
	baselines map[string]IdleBaseline
}

func DeriveCurve(
	passes []Pass,
	family CurveFamily,
	baselinesByState map[string]IdleBaseline,
	policy PointPolicy,
) (Curve, error) {
	if err := family.requireVerified(); err != nil {
		return Curve{}, err
	}
	if err := validatePointPolicy(policy); err != nil {
		return Curve{}, err
	}
	if len(passes) == 0 {
		return Curve{}, errors.New("curve requires at least one pass")
	}
	baselines, err := validateCurveBaselines(family, baselinesByState)
	if err != nil {
		return Curve{}, err
	}

	curve := Curve{family: family, baselines: baselines}
	seenRoles := make(map[PassRole]struct{}, len(passes))
	seenTrials := make(map[string]struct{})
	for _, inputPass := range passes {
		if _, exists := seenRoles[inputPass.Role]; exists {
			return Curve{}, fmt.Errorf("duplicate curve pass role %q", inputPass.Role)
		}
		seenRoles[inputPass.Role] = struct{}{}
		derivedPass, err := deriveCurvePass(inputPass, family, baselines, policy, seenTrials)
		if err != nil {
			return Curve{}, err
		}
		curve.passes = append(curve.passes, derivedPass)
	}
	sort.Slice(curve.passes, func(i, j int) bool {
		return curvePassOrder(curve.passes[i].role) < curvePassOrder(curve.passes[j].role)
	})
	return curve, nil
}

func deriveCurvePass(
	input Pass,
	family CurveFamily,
	baselines map[string]IdleBaseline,
	policy PointPolicy,
	seenTrials map[string]struct{},
) (curvePass, error) {
	if input.Role != PassDiscovery && input.Role != PassConfirmatory {
		return curvePass{}, fmt.Errorf("unsupported curve pass role %q", input.Role)
	}
	if len(input.Batches) == 0 {
		return curvePass{}, fmt.Errorf("curve pass %q has no batches", input.Role)
	}
	batches := append([]PassBatch(nil), input.Batches...)
	sort.Slice(batches, func(i, j int) bool { return batches[i].Batch < batches[j].Batch })
	plannedBatchSizes := family.contract.record.Shared.PlannedBatchSizes
	if input.Role == PassDiscovery && batches[0].Batch != plannedBatchSizes[0] {
		return curvePass{}, errors.New("discovery pass must start at the first planned batch size")
	}
	result := curvePass{role: input.Role}
	plannedBatchIndex := sort.SearchInts(plannedBatchSizes, batches[0].Batch)
	exhaustedBoundary := plannedBatchSizes[len(plannedBatchSizes)-1] + 1
	for index, batchInput := range batches {
		if batchInput.CapabilityBoundary {
			if len(batchInput.Runs) != 0 || index != len(batches)-1 ||
				plannedBatchIndex != len(plannedBatchSizes) || batchInput.Batch != exhaustedBoundary {
				return curvePass{}, errors.New("capability boundary must follow exhaustion of the planned batch size set")
			}
			boundary := batchInput.Batch
			result.capabilityBoundary = &boundary
			continue
		}
		if plannedBatchIndex >= len(plannedBatchSizes) || batchInput.Batch != plannedBatchSizes[plannedBatchIndex] {
			return curvePass{}, fmt.Errorf("curve pass %q batches must follow the frozen planned batch size sequence", input.Role)
		}
		point, err := deriveCurvePoint(batchInput, family.state, baselines, policy, seenTrials)
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
	state VerifiedCurveFamilyState,
	baselines map[string]IdleBaseline,
	policy PointPolicy,
	seenTrials map[string]struct{},
) (Point, error) {
	if len(input.Runs) == 0 {
		return Point{}, fmt.Errorf("batch %d has neither admitted Runs nor an explicit capability boundary", input.Batch)
	}
	baseline, exists := baselines[state.StateIdentity]
	if !exists {
		return Point{}, fmt.Errorf("batch %d has no exact-state idle baseline", input.Batch)
	}
	admitted := make([]AdmittedTrial, 0, len(input.Runs))
	for _, run := range input.Runs {
		if run.Trial.Batch != input.Batch || run.Trial.StateIdentity != state.StateIdentity ||
			run.Trial.EnvironmentIdentity != state.EnvironmentIdentity ||
			run.Trial.MeasurementProtocolIdentity != state.ActiveProtocolIdentity {
			return Point{}, fmt.Errorf("batch %d trial does not match its verified family state", input.Batch)
		}
		if run.Trial.TrialID == "" {
			return Point{}, fmt.Errorf("batch %d contains an empty trial identity", input.Batch)
		}
		if _, exists := seenTrials[run.Trial.TrialID]; exists {
			return Point{}, fmt.Errorf("trial %q appears in more than one curve position", run.Trial.TrialID)
		}
		seenTrials[run.Trial.TrialID] = struct{}{}
		trial, err := IncrementalTrial(run.Trial, run.Admission, baseline)
		if err != nil {
			return Point{}, fmt.Errorf("derive batch %d trial %q: %w", input.Batch, run.Trial.TrialID, err)
		}
		admitted = append(admitted, trial)
	}
	point, err := Aggregate(admitted, policy)
	if err != nil {
		return Point{}, fmt.Errorf("aggregate batch %d: %w", input.Batch, err)
	}
	return point, nil
}

func (c Curve) FamilyName() string { return c.family.Name() }

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
			if pass.capabilityBoundary != nil {
				boundary := *pass.capabilityBoundary
				result.capabilityBoundary = &boundary
			}
			return result, true
		}
	}
	return curvePass{}, false
}

func (c Curve) discoveryOnly() (Curve, error) {
	discovery, exists := c.pass(PassDiscovery)
	if !exists {
		return Curve{}, errors.New("curve has no discovery pass")
	}
	baselines := make(map[string]IdleBaseline, len(c.baselines))
	for state, baseline := range c.baselines {
		baselines[state] = cloneIdleBaseline(baseline)
	}
	return Curve{family: c.family, passes: []curvePass{discovery}, baselines: baselines}, nil
}

func (f CurveFamily) requireVerified() error {
	return f.contract.requireFrozen()
}

func validateCurveBaselines(
	family CurveFamily,
	input map[string]IdleBaseline,
) (map[string]IdleBaseline, error) {
	result := make(map[string]IdleBaseline, len(input))
	state := family.state
	for key, baseline := range input {
		if key != state.StateIdentity || key != baseline.StateIdentity ||
			baseline.EnvironmentIdentity != state.EnvironmentIdentity ||
			baseline.MeasurementProtocolIdentity != state.IdleProtocolIdentity {
			return nil, fmt.Errorf("idle baseline key %q does not match the verified curve family", key)
		}
		if err := validateIdleBaseline(baseline); err != nil {
			return nil, fmt.Errorf("idle baseline key %q is incomplete", key)
		}
		result[key] = cloneIdleBaseline(baseline)
	}
	return result, nil
}

func curvePassOrder(role PassRole) int {
	if role == PassDiscovery {
		return 0
	}
	return 1
}

func cloneIdleBaseline(baseline IdleBaseline) IdleBaseline {
	baseline.Trials = append([]IdleTrial(nil), baseline.Trials...)
	return baseline
}

func clonePoint(point Point) Point {
	point.Trials = append([]AdmittedTrial(nil), point.Trials...)
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
