package serving

import (
	"context"
	"time"
)

// PathAssessment keeps a selected candidate's replay separate from observations.
// It is an offline diagnostic, never an admission or profile-reuse decision.
type PathAssessment struct {
	At              time.Time                   `json:"at"`
	From            time.Time                   `json:"from"`
	To              time.Time                   `json:"to"`
	Seed            uint64                      `json:"seed"`
	CandidateID     string                      `json:"candidateID"`
	Keep            bool                        `json:"keep"`
	Selected        []PathPlacement             `json:"selected"`
	PredictedStatus string                      `json:"predictedStatus"`
	PredictedSLO    []SchedulerEstimate         `json:"predictedSLO"`
	ReplayReasons   []string                    `json:"replayReasons,omitempty"`
	ReplayService   *ConditionalServiceEvidence `json:"replayService,omitempty"`
	SelectedBudgets []PathBudget                `json:"selectedBudgets"`
	Requests        int                         `json:"requests"`
	OutsideCohort   int                         `json:"outsideCohort"`
	Unmatched       map[string]int              `json:"unmatched"`
	Observed        []PathObserved              `json:"observed"`
	Limitations     []string                    `json:"limitations"`
}

// PathPlacement retains selected configuration references, never profile bodies.
type PathPlacement struct {
	Instance    string        `json:"instance"`
	WorkloadUID string        `json:"workloadUID"`
	Node        NodeReference `json:"node"`
	ProfileID   string        `json:"profileID"`
	Weight      int32         `json:"weight"`
	MaxBatch    int           `json:"maxBatch"`
	MaxWait     time.Duration `json:"maxWaitNanos"`
}

// PathObserved is request-weighted except Physical, which counts each physical
// (instance,batchID) once. Unknown values stay nil, including unknown idle G.
type PathObserved struct {
	WorkloadUID               string         `json:"workloadUID"`
	DefinitionID              string         `json:"definitionID"`
	ProfileID                 string         `json:"profileID"`
	Node                      NodeReference  `json:"node"`
	DefinitionMatched         bool           `json:"definitionMatched"`
	ProfileMatched            bool           `json:"profileMatched"`
	Requests                  int            `json:"requests"`
	Completed                 int            `json:"completed"`
	Failed                    int            `json:"failed"`
	Incomplete                int            `json:"incomplete"`
	Outcomes                  map[string]int `json:"outcomes"`
	SelectedPlacement         int            `json:"selectedPlacement"`
	DifferentPlacement        int            `json:"differentPlacement"`
	SLOPercentile             float64        `json:"sloPercentile,omitempty"`
	LatencySamples            int            `json:"latencySamples"`
	LatencyMS                 *float64       `json:"latencyMS"`
	SLOExceedances            int            `json:"sloExceedances"`
	SLOLowerBoundExceedances  int            `json:"sloLowerBoundExceedances"`
	SLOUnknown                int            `json:"sloUnknown"`
	PathPercentile            float64        `json:"pathPercentile,omitempty"`
	PathSamples               int            `json:"pathSamples"`
	TotalMS                   *float64       `json:"totalMS"`
	PostMS                    *float64       `json:"postMS"`
	MissingPath               map[string]int `json:"missingPath"`
	FrozenPostSamples         int            `json:"frozenPostSamples"`
	MissingFrozenPost         int            `json:"missingFrozenPost"`
	FrozenPostExceedances     int            `json:"frozenPostExceedances"`
	MaximumFrozenPostExcessMS *float64       `json:"maximumFrozenPostExcessMS"`
	Physical                  PathPhysical   `json:"physical"`
}

type PathPhysical struct {
	Batches              int      `json:"batches"`
	ServiceSamples       int      `json:"serviceSamples"`
	IdleSamples          int      `json:"idleSamples"`
	StartupOrUnknownIdle int      `json:"startupOrUnknownIdle"`
	ServiceMS            *float64 `json:"serviceMS"`
	IdleMS               *float64 `json:"idleMS"`
	ComparedBatches      int      `json:"comparedBatches"`
	// This mean summarizes per-batch conditional percentile queries at actual G;
	// it is neither a worker mean nor a candidate end-to-end percentile.
	MeanConditionalPercentileMS *float64                   `json:"meanConditionalPercentileMS"`
	ConditionalExceedances      int                        `json:"conditionalExceedances"`
	Support                     ConditionalServiceEvidence `json:"support"`
	Missing                     map[string]int             `json:"missing"`
}

// AssessPath replays the chosen candidate with the supplied seed using the same
// replay as capacity and scheduler diagnostics. rows must contain one settled or
// open HTTP row per request plus optional native supplements. Observations use
// the received cohort [ObservationEnd-ObservationWindow,ObservationEnd), as of At.
// Native supplements contribute physical facts only; they never turn an HTTP
// failure or incomplete request into a successful path sample.
func AssessPath(ctx context.Context, settings SchedulerSettings, snapshot SchedulerSnapshot, candidate SchedulerCandidate, seed uint64, rows []*RequestRow) (PathAssessment, error) {
	// Planner estimates and prior replay output are not inputs to this replay.
	candidate.SLO, candidate.Reasons, candidate.Assumptions, candidate.Sources = nil, nil, nil, nil
	candidate.ExpectedJoules, candidate.ConditionalService, candidate.WorstExceedance = nil, nil, 0
	replayed, err := schedulerReplayEstimate(ctx, settings, snapshot, candidate, seed, nil, nil)
	if err != nil {
		return PathAssessment{}, err
	}
	report := PathAssessment{
		At: snapshot.At, From: snapshot.ObservationEnd.Add(-settings.ObservationWindow), To: snapshot.ObservationEnd,
		Seed: seed, CandidateID: candidate.ID, Keep: candidate.Keep,
		PredictedStatus: replayed.Status, PredictedSLO: replayed.SLO, ReplayReasons: replayed.Reasons, ReplayService: replayed.ConditionalService,
		Unmatched: map[string]int{}, Observed: []PathObserved{}, Selected: []PathPlacement{}, SelectedBudgets: []PathBudget{},
		Limitations: []string{
			"Replay repeats compute arrivals and outstanding requests; observations describe a received cohort from a cutoff-consistent export, without a coverage guarantee.",
			"Different placement observations cannot validate the selected target. Matching references do not reconstruct historical routing or batch policy.",
			"Only known acceptance-frozen POST is compared with old requests. Missing, failed and incomplete outcomes remain separate; request-weighted paths and batch-weighted service/G do not establish independent or representative samples.",
		},
	}
	for _, w := range snapshot.Workloads {
		if w != nil {
			report.SelectedBudgets = append(report.SelectedBudgets, ResolvePathBudget(settings, snapshot.PathBudgets, w))
		}
	}
	for _, i := range candidate.Instances {
		id := i.ProfileID
		if i.Profile != nil {
			id = i.Profile.ID
		}
		weight := i.Weight
		if route, explicit := candidate.Routing[i.WorkloadUID]; explicit {
			weight = route[i.ID]
		}
		report.Selected = append(report.Selected, PathPlacement{Instance: i.ID, WorkloadUID: i.WorkloadUID, Node: i.Node, ProfileID: id, Weight: weight, MaxBatch: i.MaxBatch, MaxWait: i.MaxWait})
	}
	groups := map[assessmentKey]*assessmentSamples{}
	groupFor := assessmentGroups(settings, snapshot, candidate, groups)
	cohort, err := assessmentObserveCohort(ctx, &report, candidate, rows, groupFor)
	if err != nil {
		return PathAssessment{}, err
	}
	assessmentObserveBatches(&report, rows, cohort, groupFor)
	report.Observed = assessmentProject(groups)
	return report, ctx.Err()
}
