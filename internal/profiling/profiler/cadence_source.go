package profiler

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"reflect"
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const BootstrapServingTraceReason = "BootstrapServingTracePrerequisite"

const (
	cadenceNativeRequestKind   = "native"
	cadenceAssignedRequestKind = "assigned"
	cadenceFinalRequestKind    = "final"
)

// BootstrapServingTraceError means profiling needs a retained, attributable
// serving population first. It is not permission to synthesize a cadence.
type BootstrapServingTraceError struct{ Detail string }

func (e *BootstrapServingTraceError) Error() string {
	return BootstrapServingTraceReason + ": " + e.Detail
}

func (e *BootstrapServingTraceError) Unwrap() error { return ErrBootstrapServingTraceRequired }

func MissingServingTrace(format string, args ...any) error {
	return &BootstrapServingTraceError{Detail: fmt.Sprintf(format, args...)}
}

// CadenceTracePhase carries actual producer windows, not procedure timestamps.
type CadenceTracePhase struct {
	Target     firstfloor.CadenceTargetPhase
	Start, End time.Time
}

// CadenceTraceProfile is the deciding identity from a retained serving catalog
// body. It deliberately does not import the serving application into profiling.
type CadenceTraceProfile struct {
	ID        string                          `json:"id"`
	Condition profilederivation.CurvePlanSpec `json:"condition"`
}

// CadenceRequestRow decodes the physical facts of the existing requests stream.
// Pointers preserve absent native facts; proxy offsets remain a separate boundary.
type CadenceRequestRow struct {
	Kind         string `json:"kind"`
	RequestID    string `json:"requestID"`
	WorkloadUID  string `json:"workloadUID"`
	DefinitionID string `json:"definitionID"`
	Node         *struct {
		UID string `json:"uid"`
	} `json:"node"`
	Instance                string                     `json:"instance"`
	ProfileID               string                     `json:"profileID"`
	BatchID                 uint64                     `json:"batchID"`
	BatchSize               int                        `json:"batchSize"`
	ReceivedAt              time.Time                  `json:"receivedAt"`
	RunningNanos            *int64                     `json:"runningNanos"`
	ExecutedNanos           *int64                     `json:"executedNanos"`
	NativeQueuedAt          *time.Time                 `json:"nativeQueuedAt"`
	NativeRunningAt         *time.Time                 `json:"nativeRunningAt"`
	NativeFinishedAt        *time.Time                 `json:"nativeFinishedAt"`
	NativeExecutionComplete *bool                      `json:"nativeExecutionComplete"`
	NativeOutcome           string                     `json:"nativeOutcome"`
	NativeError             string                     `json:"nativeError"`
	ServiceDurationNanos    int64                      `json:"serviceDurationNanos"`
	PrecedingIdleNanos      *int64                     `json:"precedingIdleNanos"`
	NativeTrialID           string                     `json:"nativeTrialID"`
	ExecutionComplete       bool                       `json:"executionComplete"`
	Outcome                 string                     `json:"outcome"`
	Error                   string                     `json:"error"`
	Object                  firstfloor.CadenceObject   `json:"-"`
	Objects                 []firstfloor.CadenceObject `json:"-"`
}

func ReadCadenceRequests(input io.Reader, object firstfloor.CadenceObject) ([]CadenceRequestRow, error) {
	decoder := json.NewDecoder(input)
	var rows []CadenceRequestRow
	for {
		var row CadenceRequestRow
		if err := decoder.Decode(&row); err != nil {
			if err == io.EOF {
				return rows, nil
			}
			return nil, fmt.Errorf("decode serving requests %s: %w", object.Key, err)
		}
		if row.RequestID == "" ||
			(row.Kind != cadenceAssignedRequestKind && row.Kind != cadenceFinalRequestKind && row.Kind != cadenceNativeRequestKind) {
			return nil, fmt.Errorf("serving requests %s contains an invalid row", object.Key)
		}
		row.Object = object
		rows = append(rows, row)
	}
}

// CadenceTrace is one selected recorded schedule. Neither another evaluation nor
// another context is pooled into it. Only G is extracted from these rows.
type CadenceTrace struct {
	LoadContext, TrialID, SourceRevision string
	Phases                               []CadenceTracePhase
	Profiles                             map[string]CadenceTraceProfile
	Rows                                 []CadenceRequestRow
}

type cadenceRequest struct {
	assigned, final *CadenceRequestRow
	native          []CadenceRequestRow
}

type cadenceBatch struct {
	id         uint64
	instance   string
	rows       []CadenceRequestRow
	invalid    bool
	start, end time.Time
	boundary   string
	phase      int
	profile    CadenceTraceProfile
}

type cadenceObservedGap struct {
	before, after     *cadenceBatch
	matchingExecution bool
}

// BuildServingCadences keeps every known physical batch as a barrier before
// filtering by phase/configuration/completeness. Final HTTP facts supersede
// assigned facts; sparse native records fill only missing native fields.
func BuildServingCadences(config profilingrun.RunConfig, trace CadenceTrace) (map[int]*firstfloor.CadenceContract, error) {
	byInstance, blockedInstances, err := collectCadenceBatches(config, trace)
	if err != nil {
		return nil, err
	}
	gaps := make([][]cadenceObservedGap, len(trace.Phases))
	matchingProfiles := make(map[string]bool, len(trace.Profiles))
	for id, profile := range trace.Profiles {
		matchingProfiles[id] = sameCadenceExecution(profile, config)
	}
	instances := make([]string, 0, len(byInstance))
	for instance := range byInstance {
		instances = append(instances, instance)
	}
	sort.Strings(instances)
	for _, instance := range instances {
		if blockedInstances[instance] {
			continue
		}
		var batches []*cadenceBatch
		for _, batch := range byInstance[instance] {
			prepareCadenceBatch(batch, config, trace)
			batches = append(batches, batch)
		}
		sort.Slice(batches, func(i, j int) bool { return batches[i].id < batches[j].id })
		for i := 1; i < len(batches); i++ {
			before, after := batches[i-1], batches[i]
			if before.invalid || after.invalid || after.id-before.id != 1 || before.phase < 0 || before.phase != after.phase ||
				before.boundary != after.boundary || after.start.Before(before.end) || before.rows[0].ProfileID != after.rows[0].ProfileID ||
				before.rows[0].DefinitionID != after.rows[0].DefinitionID || len(before.rows) != len(after.rows) ||
				before.rows[0].NativeTrialID != after.rows[0].NativeTrialID ||
				after.boundary == firstfloor.NativeExchangeGapBoundary && after.rows[0].PrecedingIdleNanos == nil {
				continue
			}
			gaps[after.phase] = append(gaps[after.phase], cadenceObservedGap{before, after, matchingProfiles[after.rows[0].ProfileID]})
		}
	}
	batches := config.Measurement.WarmupBatchSizes
	if len(batches) == 0 {
		batches = []int{config.Measurement.Batch}
	}
	contracts := make(map[int]*firstfloor.CadenceContract, len(batches))
	for _, batch := range batches {
		contract, err := freezeServingCadence(config, trace, gaps, batch)
		if err != nil {
			return nil, fmt.Errorf("cadence for batch %d: %w", batch, err)
		}
		contracts[batch] = contract
	}
	return contracts, nil
}

func collectCadenceRequests(rows []CadenceRequestRow) (map[string]*cadenceRequest, error) {
	requests := make(map[string]*cadenceRequest)
	for _, row := range rows {
		request := requests[row.RequestID]
		if request == nil {
			request = &cadenceRequest{}
			requests[row.RequestID] = request
		}
		switch row.Kind {
		case cadenceFinalRequestKind:
			if request.final != nil && !sameCadenceFinal(*request.final, row) {
				return nil, MissingServingTrace("conflicting final facts for request %s", row.RequestID)
			}
			copy := row
			request.final = &copy
		case cadenceAssignedRequestKind:
			copy := row
			request.assigned = &copy
		case cadenceNativeRequestKind:
			request.native = append(request.native, row)
		}
	}
	return requests, nil
}

func collectCadenceBatches(config profilingrun.RunConfig, trace CadenceTrace) (map[string]map[uint64]*cadenceBatch, map[string]bool, error) {
	requests, err := collectCadenceRequests(trace.Rows)
	if err != nil {
		return nil, nil, err
	}
	byInstance := make(map[string]map[uint64]*cadenceBatch)
	blockedInstances := make(map[string]bool)
	for _, request := range requests {
		row, valid := joinCadenceRequest(request)
		if !cadenceRowInWindow(row, trace.Phases) {
			continue
		}
		if row.Instance == "" {
			// A native record without physical identity cannot be placed safely.
			if row.WorkloadUID == config.Measurement.WorkloadUID && (row.NativeQueuedAt != nil || row.NativeRunningAt != nil || row.NativeFinishedAt != nil) {
				return nil, nil, MissingServingTrace("native request %s has no physical instance", row.RequestID)
			}
			continue
		}
		if row.BatchID == 0 {
			blockedInstances[row.Instance] = true
			continue
		}
		batches := byInstance[row.Instance]
		if batches == nil {
			batches = make(map[uint64]*cadenceBatch)
			byInstance[row.Instance] = batches
		}
		batch := batches[row.BatchID]
		if batch == nil {
			batch = &cadenceBatch{id: row.BatchID, instance: row.Instance, phase: -1}
			batches[row.BatchID] = batch
		}
		batch.invalid = batch.invalid || !valid
		batch.rows = append(batch.rows, row)
	}
	return byInstance, blockedInstances, nil
}

func freezeServingCadence(config profilingrun.RunConfig, trace CadenceTrace, gaps [][]cadenceObservedGap, targetBatch int) (*firstfloor.CadenceContract, error) {
	contract := &firstfloor.CadenceContract{
		Target:     firstfloor.CadenceTarget{LoadContext: trace.LoadContext, Boundary: firstfloor.NativeExchangeGapBoundary, ConditionalModel: firstfloor.ConditionalServiceModel},
		Provenance: firstfloor.CadenceProvenance{Seed: 0, DrawRule: firstfloor.CadenceDrawRule},
	}
	for _, phase := range trace.Phases {
		contract.Target.Schedule = append(contract.Target.Schedule, phase.Target)
		contract.Provenance.Phases = append(contract.Provenance.Phases, firstfloor.CadencePhase{Name: phase.Target.Name})
	}
	var population []firstfloor.CadenceElementRef
	sourceIndexes := make(map[firstfloor.CadenceSource]int)
	addSource := func(source firstfloor.CadenceSource) int {
		if index, found := sourceIndexes[source]; found {
			return index
		}
		index := len(contract.Provenance.Sources)
		contract.Provenance.Sources = append(contract.Provenance.Sources, source)
		sourceIndexes[source] = index
		return index
	}
	for phaseIndex, observed := range gaps {
		// Same conditions/K first; otherwise borrow only G from this context.
		// Prefer direct native progress over legacy offsets within that choice.
		exact, boundary := false, ""
		for _, gap := range observed {
			exact = exact || gap.matchingExecution && len(gap.after.rows) == targetBatch
		}
		for _, gap := range observed {
			if exact && !(gap.matchingExecution && len(gap.after.rows) == targetBatch) {
				continue
			}
			if boundary == "" || cadenceBoundaryRank(gap.after.boundary) > cadenceBoundaryRank(boundary) {
				boundary = gap.after.boundary
			}
		}
		for _, gap := range observed {
			if exact && !(gap.matchingExecution && len(gap.after.rows) == targetBatch) || gap.after.boundary != boundary {
				continue
			}
			batch, previous := gap.after, gap.before
			profile, row := batch.profile, batch.rows[0]
			phase := trace.Phases[phaseIndex]
			source := firstfloor.CadenceSource{
				Object: row.Object, Boundary: batch.boundary, SourceRevision: trace.SourceRevision,
				LoadContext: trace.LoadContext, Workload: row.WorkloadUID, NodeUID: profile.Condition.NodeUID,
				ExecutionPath: profile.Condition.Artifact.ExecutionPath, PowerMode: profile.Condition.PowerMode,
				PhysicalInstanceID: batch.instance, TrialID: trace.TrialID + "/" + batch.instance + "/" + row.NativeTrialID,
				ObservedBatch: len(batch.rows), WindowStart: phase.Start, WindowEnd: phase.End, Borrowed: !(gap.matchingExecution && len(batch.rows) == targetBatch),
			}
			sourceIndex := addSource(source)
			for _, members := range [2][]CadenceRequestRow{previous.rows, batch.rows} {
				for _, member := range members {
					for _, object := range member.Objects {
						source.Object = object
						addSource(source)
					}
				}
			}
			elements := &contract.Provenance.Phases[phaseIndex].Elements
			population = append(population, firstfloor.CadenceElementRef{Phase: phase.Target.Name, ElementIndex: len(*elements)})
			gapNanos := batch.start.Sub(previous.end).Nanoseconds()
			if batch.boundary == firstfloor.NativeExchangeGapBoundary {
				gapNanos = *row.PrecedingIdleNanos
			}
			*elements = append(*elements, firstfloor.CadenceGapElement{GapNanos: gapNanos, SourceIndex: sourceIndex, BatchID: batch.id})
		}
		if len(contract.Provenance.Phases[phaseIndex].Elements) == 0 {
			return nil, MissingServingTrace("no complete consecutive physical batches in recorded phase %s", trace.Phases[phaseIndex].Target.Name)
		}
	}
	if len(population) == 0 {
		return nil, MissingServingTrace("selected recorded schedule has no attributable serving gaps")
	}
	// Reuse the existing resampling seed 0. Flatten actual records, not phases:
	// observed phase cardinality determines weight. The frozen sequence cycles
	// explicitly if elapsed-floor stopping needs more calls than the N floor.
	random := rand.New(rand.NewPCG(0, 0))
	for range max(1, config.Measurement.MinServiceSamples+1) {
		contract.Provenance.Draws = append(contract.Provenance.Draws, population[random.IntN(len(population))])
	}
	if err := contract.Validate(); err != nil {
		return nil, MissingServingTrace("invalid retained serving population: %v", err)
	}
	return contract, nil
}

func sameCadenceFinal(a, b CadenceRequestRow) bool {
	a.Object, b.Object = firstfloor.CadenceObject{}, firstfloor.CadenceObject{}
	a.Objects, b.Objects = nil, nil
	return reflect.DeepEqual(a, b)
}

func joinCadenceRequest(request *cadenceRequest) (CadenceRequestRow, bool) {
	var row CadenceRequestRow
	if request.final != nil {
		row = *request.final
	} else if request.assigned != nil {
		row = *request.assigned
	}
	if row.RequestID != "" {
		row.Objects = append(row.Objects, row.Object)
	}
	valid := true
	for _, supplement := range request.native {
		if row.RequestID == "" {
			row = supplement
			row.Objects = append(row.Objects, row.Object)
			continue
		}
		row.Objects = append(row.Objects, supplement.Object)
		for _, pair := range [][2]string{{row.Instance, supplement.Instance}, {row.WorkloadUID, supplement.WorkloadUID}, {row.DefinitionID, supplement.DefinitionID}} {
			if pair[0] != "" && pair[1] != "" && pair[0] != pair[1] {
				valid = false
			}
		}
		if row.Instance == "" {
			row.Instance = supplement.Instance
		}
		if row.WorkloadUID == "" {
			row.WorkloadUID = supplement.WorkloadUID
		}
		if row.DefinitionID == "" {
			row.DefinitionID = supplement.DefinitionID
		}
		if row.BatchID == 0 {
			row.BatchID = supplement.BatchID
		} else if supplement.BatchID != 0 && row.BatchID != supplement.BatchID {
			valid = false
		}
		if row.BatchSize == 0 {
			row.BatchSize = supplement.BatchSize
		} else if supplement.BatchSize != 0 && row.BatchSize != supplement.BatchSize {
			valid = false
		}
		finished := row.NativeFinishedAt != nil
		merge := func(target **time.Time, source *time.Time) {
			if *target == nil {
				*target = source
			}
		}
		merge(&row.NativeQueuedAt, supplement.NativeQueuedAt)
		merge(&row.NativeRunningAt, supplement.NativeRunningAt)
		merge(&row.NativeFinishedAt, supplement.NativeFinishedAt)
		if supplement.NativeFinishedAt != nil {
			if !finished {
				if row.NativeExecutionComplete == nil {
					row.NativeExecutionComplete = supplement.NativeExecutionComplete
				}
				if row.NativeOutcome == "" {
					row.NativeOutcome = supplement.NativeOutcome
				}
				if row.NativeError == "" {
					row.NativeError = supplement.NativeError
				}
			}
			if row.ServiceDurationNanos == 0 && supplement.ServiceDurationNanos > 0 {
				row.ServiceDurationNanos, row.PrecedingIdleNanos, row.NativeTrialID = supplement.ServiceDurationNanos, supplement.PrecedingIdleNanos, supplement.NativeTrialID
				row.Object = supplement.Object
			}
		}
	}
	return row, valid
}

func prepareCadenceBatch(batch *cadenceBatch, config profilingrun.RunConfig, trace CadenceTrace) {
	sort.Slice(batch.rows, func(i, j int) bool { return batch.rows[i].RequestID < batch.rows[j].RequestID })
	first := batch.rows[0]
	profile, known := trace.Profiles[first.ProfileID]
	batch.profile = profile
	if !known || first.BatchSize <= 0 || len(batch.rows) != first.BatchSize || profile.Condition.NodeUID == "" ||
		profile.Condition.Artifact.ExecutionPath == "" || profile.Condition.PowerMode == "" {
		batch.invalid = true
	}
	for _, row := range batch.rows {
		if row.WorkloadUID != config.Measurement.WorkloadUID || row.ProfileID != first.ProfileID || row.DefinitionID != first.DefinitionID ||
			row.BatchSize != first.BatchSize || row.Node != nil && row.Node.UID != profile.Condition.NodeUID {
			batch.invalid = true
		}
		prepareCadenceRowTiming(batch, &row, &first)
	}
	for index, phase := range trace.Phases {
		if !batch.start.Before(phase.Start) && batch.start.Before(phase.End) && !batch.end.After(phase.End) {
			batch.phase = index
			break
		}
	}
}

func prepareCadenceRowTiming(batch *cadenceBatch, row, first *CadenceRequestRow) {
	if row.NativeQueuedAt != nil || row.NativeRunningAt != nil || row.NativeFinishedAt != nil {
		prepareNativeCadenceRowTiming(batch, row, first)
		return
	}
	if row.RunningNanos == nil || row.ExecutedNanos == nil || row.ReceivedAt.IsZero() || !row.ExecutionComplete || row.Outcome != "completed" || row.Error != "" {
		batch.invalid = true
		return
	}
	start, end := row.ReceivedAt.Add(time.Duration(*row.RunningNanos)), row.ReceivedAt.Add(time.Duration(*row.ExecutedNanos))
	extendCadenceBatchWindow(batch, start, end, "legacy-proxy-executed-to-next-running")
}

func prepareNativeCadenceRowTiming(batch *cadenceBatch, row, first *CadenceRequestRow) {
	// Partial native progress is a barrier, never a legacy fallback. These
	// actor timestamps are not the canonical IPC exchange boundary.
	if row.NativeQueuedAt == nil || row.NativeRunningAt == nil || row.NativeFinishedAt == nil || row.NativeOutcome != "completed" || row.NativeError != "" ||
		row.NativeExecutionComplete == nil || !*row.NativeExecutionComplete {
		batch.invalid = true
		return
	}
	start, end := *row.NativeRunningAt, *row.NativeFinishedAt
	if row.NativeQueuedAt.After(start) {
		batch.invalid = true
	}
	boundary := "native-actor-finished-to-next-running-proxy"
	if row.ServiceDurationNanos > 0 {
		if row.NativeTrialID == "" || row.NativeTrialID != first.NativeTrialID || row.ServiceDurationNanos != first.ServiceDurationNanos ||
			(row.PrecedingIdleNanos == nil) != (first.PrecedingIdleNanos == nil) ||
			row.PrecedingIdleNanos != nil && first.PrecedingIdleNanos != nil && *row.PrecedingIdleNanos != *first.PrecedingIdleNanos {
			batch.invalid = true
		}
		boundary = firstfloor.NativeExchangeGapBoundary
		if row.PrecedingIdleNanos != nil && *row.PrecedingIdleNanos < 0 {
			batch.invalid = true
		}
	}
	if row.ServiceDurationNanos < 0 || row.ServiceDurationNanos == 0 && row.PrecedingIdleNanos != nil {
		batch.invalid = true
	}
	extendCadenceBatchWindow(batch, start, end, boundary)
}

func extendCadenceBatchWindow(batch *cadenceBatch, start, end time.Time, boundary string) {
	if end.Before(start) {
		batch.invalid = true
	}
	if batch.boundary == "" {
		batch.start, batch.end, batch.boundary = start, end, boundary
	} else {
		if batch.boundary != boundary {
			batch.invalid = true
		}
		if start.Before(batch.start) {
			batch.start = start
		}
		if end.After(batch.end) {
			batch.end = end
		}
	}
}

func cadenceRowInWindow(row CadenceRequestRow, phases []CadenceTracePhase) bool {
	known := false
	for _, anchor := range []*time.Time{&row.ReceivedAt, row.NativeQueuedAt, row.NativeRunningAt, row.NativeFinishedAt} {
		if anchor == nil || anchor.IsZero() {
			continue
		}
		known = true
		for _, phase := range phases {
			if !anchor.Before(phase.Start) && anchor.Before(phase.End) {
				return true
			}
		}
	}
	// Unplaceable physical progress remains a barrier rather than disappearing.
	return !known
}

func cadenceBoundaryRank(boundary string) int {
	switch boundary {
	case firstfloor.NativeExchangeGapBoundary:
		return 3
	case "native-actor-finished-to-next-running-proxy":
		return 2
	default:
		return 1
	}
}

func sameCadenceExecution(profile CadenceTraceProfile, config profilingrun.RunConfig) bool {
	observed := profilederivation.PerformanceCondition(profile.Condition)
	// The trace context is resolved independently from measured-profile cadence;
	// this comparison prefers the requested execution recipe, not foreign S/J.
	observed.ActiveProtocol = profilederivation.CurveProtocolContract{}
	expected := observed
	expected.NodeUID, expected.DeviceClass, expected.LogicalModel = config.TargetNode.UID, config.State.DeviceClass, config.State.Model
	expected.RuntimeFamily, expected.Backend, expected.Firstfloor = config.State.RuntimeFamily, config.State.Backend, config.State.Firstfloor
	expected.PowerMode, expected.Actor = config.State.PowerMode, config.Actor
	expected.Artifact = profilederivation.CurveArtifactContract{ExecutionPath: config.Candidate.ExecutionPath, Name: config.State.Artifact, Files: config.State.ArtifactFiles, Image: config.Images.Artifact}
	p := config.State.Preprocessor
	expected.Preprocessor = profilederivation.CurvePreprocessorContract{Name: p.Name, Image: p.Image, Architecture: p.Architecture, Path: p.Path, MaxNewTokens: p.MaxNewTokens, Inputs: p.Inputs, Outputs: p.Outputs}
	expected.RuntimeAccess = nil
	if access := config.Delivery.RuntimeAccess; access != nil {
		expected.RuntimeAccess = &profilederivation.CurveRuntimeAccessContract{Privileged: access.Privileged, SupplementalGroups: access.SupplementalGroups, Resources: access.Resources, GPUUUID: access.GPUUUID}
	}
	return reflect.DeepEqual(observed, profilederivation.PerformanceCondition(expected))
}
