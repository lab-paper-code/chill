package profilederivation

import (
	"math"
	"math/rand"
	"testing"

	"gonum.org/v1/gonum/stat"
)

const (
	plan5SimulationAlpha      = 0.05
	plan5SimulationTolerance  = 0.05
	plan5SimulationIterations = 20_000
	plan5SimulationTrials     = 3
)

type plan5CoverageScenario struct {
	name           string
	seed           int64
	epsilon        [4]float64
	coefficient    [4]float64
	activeSD       float64
	baselineSD     float64
	sharedBaseline bool
}

type plan5CoverageResult struct {
	selected   int
	miscovered int
	iterations int
}

func (result plan5CoverageResult) miscoverageRate() float64 {
	return float64(result.miscovered) / float64(result.iterations)
}

func TestPlan5GlobalAllocationCoversPostSelectionSimulation(t *testing.T) {
	baselineHeavy := plan5CoverageScenario{
		name: "shared baseline dominates", seed: 7,
		epsilon:     [4]float64{1, .82, .80, .795},
		coefficient: [4]float64{.30, .20, .15, .10},
		activeSD:    .005, baselineSD: .5, sharedBaseline: true,
	}
	local := simulatePlan5Coverage(t, baselineHeavy, false)
	t.Logf("candidate-local allocation: selected=%d miscoverage=%.5f", local.selected, local.miscoverageRate())
	if local.miscoverageRate() <= plan5SimulationAlpha {
		t.Fatalf("candidate-local allocation did not expose selection leakage: %+v", local)
	}

	scenarios := []plan5CoverageScenario{
		baselineHeavy,
		{
			name: "balanced shared baseline", seed: 11,
			epsilon:     [4]float64{1, .82, .80, .795},
			coefficient: [4]float64{.30, .20, .15, .10},
			activeSD:    .02, baselineSD: .2, sharedBaseline: true,
		},
		{
			name: "active variance dominates", seed: 13,
			epsilon:     [4]float64{1, .82, .80, .795},
			coefficient: [4]float64{.30, .20, .15, .10},
			activeSD:    .2, baselineSD: .01, sharedBaseline: true,
		},
		{
			name: "distinct exact-state baselines", seed: 17,
			epsilon:     [4]float64{1, .82, .80, .795},
			coefficient: [4]float64{.30, .20, .15, .10},
			activeSD:    .01, baselineSD: .35,
		},
	}
	for _, scenario := range scenarios {
		t.Run(scenario.name, func(t *testing.T) {
			result := simulatePlan5Coverage(t, scenario, true)
			t.Logf("global allocation: selected=%d miscoverage=%.5f", result.selected, result.miscoverageRate())
			if result.selected == 0 || result.miscoverageRate() > plan5SimulationAlpha {
				t.Fatalf("global allocation missed nominal family-wise coverage: %+v", result)
			}
		})
	}
}

func simulatePlan5Coverage(
	t *testing.T,
	scenario plan5CoverageScenario,
	globalAllocation bool,
) plan5CoverageResult {
	t.Helper()
	random := rand.New(rand.NewSource(scenario.seed)) //nolint:gosec // deterministic statistical regression
	result := plan5CoverageResult{iterations: plan5SimulationIterations}
	for range plan5SimulationIterations {
		baselineError, baselineVariance := simulateBaselines(random, scenario)
		discovery := simulatePass(random, scenario, baselineError)
		candidate := discoveryCandidate(discovery, plan5SimulationTolerance)
		if candidate < 0 {
			continue
		}
		result.selected++
		confirmation, activeVariance := simulatePassWithVariance(random, scenario, baselineError)
		if !selectedStatementsCover(
			scenario, candidate, confirmation, activeVariance, baselineVariance, globalAllocation,
		) {
			result.miscovered++
		}
	}
	return result
}

func simulateBaselines(
	random *rand.Rand,
	scenario plan5CoverageScenario,
) ([4]float64, [4]float64) {
	var errors [4]float64
	var variances [4]float64
	count := len(errors)
	if scenario.sharedBaseline {
		count = 1
	}
	for index := range count {
		values := normalSamples(random, scenario.baselineSD)
		mean, sampleVariance := stat.MeanVariance(values[:], nil)
		errors[index] = mean
		variances[index] = sampleVariance / plan5SimulationTrials
	}
	if scenario.sharedBaseline {
		for index := 1; index < len(errors); index++ {
			errors[index] = errors[0]
			variances[index] = variances[0]
		}
	}
	return errors, variances
}

func simulatePass(random *rand.Rand, scenario plan5CoverageScenario, baselineError [4]float64) [4]float64 {
	means, _ := simulatePassWithVariance(random, scenario, baselineError)
	return means
}

func simulatePassWithVariance(
	random *rand.Rand,
	scenario plan5CoverageScenario,
	baselineError [4]float64,
) ([4]float64, [4]float64) {
	var means [4]float64
	var variances [4]float64
	for batch := range means {
		values := normalSamples(random, scenario.activeSD)
		mean, sampleVariance := stat.MeanVariance(values[:], nil)
		means[batch] = scenario.epsilon[batch] + mean - scenario.coefficient[batch]*baselineError[batch]
		variances[batch] = sampleVariance
	}
	return means, variances
}

func normalSamples(random *rand.Rand, standardDeviation float64) [plan5SimulationTrials]float64 {
	var values [plan5SimulationTrials]float64
	for index := range values {
		values[index] = random.NormFloat64() * standardDeviation
	}
	return values
}

func discoveryCandidate(discovery [4]float64, tolerance float64) int {
	for candidate := 0; candidate < len(discovery)-1; candidate++ {
		if discovery[candidate] <= 0 {
			continue
		}
		remainingImprovement := math.Inf(-1)
		for later := candidate + 1; later < len(discovery); later++ {
			improvement := (discovery[candidate] - discovery[later]) / discovery[candidate]
			remainingImprovement = math.Max(remainingImprovement, improvement)
		}
		if remainingImprovement <= tolerance {
			return candidate
		}
	}
	return -1
}

func selectedStatementsCover(
	scenario plan5CoverageScenario,
	candidate int,
	confirmation [4]float64,
	activeSampleVariance [4]float64,
	baselineVariance [4]float64,
	globalAllocation bool,
) bool {
	statements := len(confirmation) - candidate
	if globalAllocation {
		statements = (len(confirmation) - 1) * (len(confirmation) + 2) / 2
	}
	quantile := 1 - plan5SimulationAlpha/float64(statements)
	pointComponents := []varianceComponent{
		{variance: activeSampleVariance[candidate] / plan5SimulationTrials, degreesOfFreedom: plan5SimulationTrials - 1},
		{variance: math.Pow(scenario.coefficient[candidate], 2) * baselineVariance[candidate], degreesOfFreedom: plan5SimulationTrials - 1},
	}
	pointVariance, pointDegreesOfFreedom, err := welchSatterthwaite(pointComponents...)
	if err != nil || pointDegreesOfFreedom == nil {
		return false
	}
	pointHalfWidth := studentTQuantile(quantile, *pointDegreesOfFreedom) * math.Sqrt(pointVariance)
	if confirmation[candidate]-pointHalfWidth > scenario.epsilon[candidate] {
		return false
	}

	for later := candidate + 1; later < len(confirmation); later++ {
		components := []varianceComponent{
			{variance: activeSampleVariance[candidate] / plan5SimulationTrials, degreesOfFreedom: plan5SimulationTrials - 1},
			{variance: activeSampleVariance[later] / plan5SimulationTrials, degreesOfFreedom: plan5SimulationTrials - 1},
		}
		if scenario.sharedBaseline {
			components = append(components, varianceComponent{
				variance: math.Pow(scenario.coefficient[candidate]-scenario.coefficient[later], 2) *
					baselineVariance[candidate],
				degreesOfFreedom: plan5SimulationTrials - 1,
			})
		} else {
			components = append(components,
				varianceComponent{
					variance:         math.Pow(scenario.coefficient[candidate], 2) * baselineVariance[candidate],
					degreesOfFreedom: plan5SimulationTrials - 1,
				},
				varianceComponent{
					variance:         math.Pow(scenario.coefficient[later], 2) * baselineVariance[later],
					degreesOfFreedom: plan5SimulationTrials - 1,
				},
			)
		}
		contrastVariance, contrastDegreesOfFreedom, err := welchSatterthwaite(components...)
		if err != nil || contrastDegreesOfFreedom == nil {
			return false
		}
		contrastHalfWidth := studentTQuantile(quantile, *contrastDegreesOfFreedom) * math.Sqrt(contrastVariance)
		estimate := confirmation[candidate] - confirmation[later]
		truth := scenario.epsilon[candidate] - scenario.epsilon[later]
		if estimate+contrastHalfWidth < truth {
			return false
		}
	}
	return true
}
