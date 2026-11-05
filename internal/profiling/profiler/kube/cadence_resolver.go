package kube

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CadenceEvidenceReader reuses the EvaluationRun's existing frozen S3 binding.
// The observation archive uses the current serving store, which can be different.
type CadenceEvidenceReader func(context.Context, *api.EvaluationRun) (evidencestore.Reader, error)

type CadenceObservationArchive interface {
	evidencestore.Reader
	evidencestore.Lister
}

type KubernetesCadenceResolver struct {
	reader       client.Reader
	namespace    string
	evidence     CadenceEvidenceReader
	observations CadenceObservationArchive
}

func NewKubernetesCadenceResolver(reader client.Reader, namespace string, evidence CadenceEvidenceReader, observations CadenceObservationArchive) (*KubernetesCadenceResolver, error) {
	if reader == nil || namespace == "" || evidence == nil || observations == nil {
		return nil, errors.New("cadence requires Kubernetes, retained evaluation evidence and observation archive readers")
	}
	return &KubernetesCadenceResolver{reader: reader, namespace: namespace, evidence: evidence, observations: observations}, nil
}

type cadenceBindingPart struct {
	ID      string             `json:"id"`
	Seconds float64            `json:"seconds"`
	Rates   map[string]float64 `json:"rates"`
}

type cadenceSchedule struct {
	ID      string               `json:"id"`
	Parts   []cadenceBindingPart `json:"parts"`
	Seconds float64              `json:"seconds"`
	Traces  map[string]struct {
		Offsets []int64 `json:"offsetsNS"`
		Offered int     `json:"offered"`
	} `json:"traces"`
}

type cadencePopulationWorkload struct {
	Name         string                         `json:"name"`
	UID          string                         `json:"uid"`
	Model        *api.EvaluationObjectReference `json:"model"`
	Preprocessor *api.EvaluationObjectReference `json:"preprocessor"`
	Dataset      *api.EvaluationObjectReference `json:"dataset"`
}

type cadenceSnapshot struct {
	SchemaVersion string    `json:"schemaVersion"`
	RunUID        types.UID `json:"runUID"`
	Scenarios     map[string]struct {
		Ref       api.EvaluationObjectReference `json:"ref"`
		Spec      api.LoadScenarioSpec          `json:"spec"`
		Workloads []cadencePopulationWorkload   `json:"workloads"`
	} `json:"scenarios"`
	Bindings map[string]struct {
		Schedule []cadenceSchedule `json:"schedule"`
	} `json:"bindings"`
}

type cadencePhaseRecord struct {
	Stage  cadenceSchedule `json:"stage"`
	Starts map[string]struct {
		At time.Time `json:"at"`
	} `json:"starts"`
	Start   time.Time `json:"startUTC"`
	End     time.Time `json:"endUTC"`
	Drained time.Time `json:"drainedUTC"`
}

type cadenceStageSource struct {
	run      *api.EvaluationRun
	stage    *api.EvaluationStage
	objects  evidencestore.Reader
	snapshot cadenceSnapshot
}

// ResolveCadence selects the newest intact completed source in the requested
// frozen input population. Evidence errors in that source remain fail-closed.
func (r *KubernetesCadenceResolver) ResolveCadence(ctx context.Context, config profilingrun.RunConfig) (map[int]*firstfloor.CadenceContract, error) {
	sources, err := r.completedCadenceSources(ctx, &config)
	if err != nil {
		return nil, err
	}
	if len(sources) == 0 {
		return nil, profiler.MissingServingTrace("no completed retained Evaluation serving trace matches Workload %s and its input population", config.Measurement.WorkloadName)
	}
	return r.resolveStage(ctx, &config, &sources[0])
}

func (r *KubernetesCadenceResolver) completedCadenceSources(ctx context.Context, config *profilingrun.RunConfig) ([]cadenceStageSource, error) {
	var runs api.EvaluationRunList
	if err := r.reader.List(ctx, &runs, client.InNamespace(r.namespace)); err != nil {
		return nil, fmt.Errorf("list retained cadence EvaluationRuns: %w", err)
	}
	var sources []cadenceStageSource
retainedRuns:
	for index := range runs.Items {
		run := &runs.Items[index]
		if run.UID == "" || run.Status.FinishedAt == nil || !cadenceCompleted(run.Status.Conditions) {
			continue
		}
		// Structural discovery defects exclude this whole Run, not other
		// populations or intact Runs. Selected artifacts remain fail-closed.
		var candidates []cadenceStageSource
		for _, template := range run.Spec.Stages {
			scenario, err := r.cadenceScenario(ctx, template.LoadScenarioRef)
			if err != nil {
				var prerequisite *profiler.BootstrapServingTraceError
				if errors.As(err, &prerequisite) {
					continue retainedRuns
				}
				return nil, err
			}
			if !cadenceScenarioMatches(scenario, config) {
				continue
			}
			source := cadenceStageSource{run: run}
			if err := r.loadCadenceSnapshot(ctx, &source, scenario); err != nil {
				var prerequisite *profiler.BootstrapServingTraceError
				if errors.As(err, &prerequisite) {
					continue retainedRuns
				}
				return nil, err
			}
			frozen := source.snapshot.Scenarios[string(scenario.UID)]
			if cadencePopulationMatches(frozen.Workloads, frozen.Spec.DatasetRefs, config) {
				candidates = append(candidates, source)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		stages, err := r.completedCadenceStages(ctx, run)
		if err != nil {
			var prerequisite *profiler.BootstrapServingTraceError
			if errors.As(err, &prerequisite) {
				continue // A defective Run does not invalidate another intact Run.
			}
			return nil, err
		}
		for _, stage := range stages {
			for _, source := range candidates {
				frozen, found := source.snapshot.Scenarios[string(stage.Spec.LoadScenarioRef.UID)]
				if found && frozen.Ref == stage.Spec.LoadScenarioRef &&
					cadencePopulationMatches(frozen.Workloads, frozen.Spec.DatasetRefs, config) {
					source.stage = stage
					sources = append(sources, source)
					break
				}
			}
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		a, b := sources[i].stage, sources[j].stage
		if !a.Status.FinishedAt.Equal(b.Status.FinishedAt) {
			return a.Status.FinishedAt.After(b.Status.FinishedAt.Time)
		}
		return string(a.UID) > string(b.UID)
	})
	return sources, nil
}

// completedCadenceStages screens the whole candidate Run before any child can
// become the chosen source. Missing or changed children exclude only this Run.
func (r *KubernetesCadenceResolver) completedCadenceStages(ctx context.Context, run *api.EvaluationRun) ([]*api.EvaluationStage, error) {
	var stages []*api.EvaluationStage
	for _, child := range run.Status.ChildRefs {
		stage := &api.EvaluationStage{}
		if err := r.reader.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: child.Name}, stage); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, profiler.MissingServingTrace("retained EvaluationStage %s is absent", child.Name)
			}
			return nil, fmt.Errorf("get cadence EvaluationStage: %w", err)
		}
		if stage.UID != child.UID || stage.Spec.StageID != child.StageID || !cadenceOwnedBy(stage, run) {
			return nil, profiler.MissingServingTrace("EvaluationStage is not pinned to retained parent %s", run.Name)
		}
		for _, template := range run.Spec.Stages {
			if template.ID == child.StageID && stage.Spec.LoadScenarioRef != template.LoadScenarioRef {
				return nil, profiler.MissingServingTrace("LoadScenario is not pinned to retained parent %s", run.Name)
			}
		}
		if stage.Spec.InputSnapshotRef != *run.Status.InputSnapshotRef {
			return nil, profiler.MissingServingTrace("stage input snapshot is not pinned to the retained parent")
		}
		if stage.Status.FinishedAt != nil && cadenceCompleted(stage.Status.Conditions) {
			stages = append(stages, stage)
		}
	}
	return stages, nil
}

func (r *KubernetesCadenceResolver) cadenceScenario(ctx context.Context, ref api.EvaluationObjectReference) (*api.LoadScenario, error) {
	scenario := &api.LoadScenario{}
	if err := r.reader.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: ref.Name}, scenario); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, profiler.MissingServingTrace("retained LoadScenario %s is absent", ref.Name)
		}
		return nil, fmt.Errorf("get cadence LoadScenario: %w", err)
	}
	if scenario.UID != ref.UID {
		return nil, profiler.MissingServingTrace("LoadScenario UID changed for %s", ref.Name)
	}
	return scenario, nil
}

func cadenceScenarioMatches(scenario *api.LoadScenario, config *profilingrun.RunConfig) bool {
	matching := false
	for _, workload := range scenario.Spec.Workloads {
		matching = matching || string(workload.UID) == config.Measurement.WorkloadUID && workload.Name == config.Measurement.WorkloadName
	}
	return matching
}

func (r *KubernetesCadenceResolver) loadCadenceSnapshot(ctx context.Context, source *cadenceStageSource, scenario *api.LoadScenario) error {
	run := source.run
	if run.Status.FrozenConfig == nil || run.Status.InputSnapshotRef == nil {
		return profiler.MissingServingTrace("retained EvaluationRun %s has no frozen evidence binding/input snapshot", run.Name)
	}
	objects, err := r.evidence(ctx, run)
	if err != nil {
		return err
	}
	var snapshot cadenceSnapshot
	if err := readCadenceJSON(ctx, objects, *run.Status.InputSnapshotRef, &snapshot); err != nil {
		return cadenceReadError("input snapshot", err)
	}
	if snapshot.SchemaVersion != "evaluation-snapshot.v2alpha1" || snapshot.RunUID != run.UID {
		return profiler.MissingServingTrace("input snapshot does not match retained EvaluationRun")
	}
	frozen, found := snapshot.Scenarios[string(scenario.UID)]
	if !found || frozen.Ref.UID != scenario.UID || frozen.Ref.Name != scenario.Name {
		return profiler.MissingServingTrace("input snapshot has no pinned scenario")
	}
	source.objects, source.snapshot = objects, snapshot
	return nil
}

func cadencePopulationMatches(workloads []cadencePopulationWorkload, datasets map[string]api.EvaluationObjectReference, config *profilingrun.RunConfig) bool {
	dataset, found := datasets[config.Measurement.WorkloadName]
	if !found || config.Measurement.Dataset == nil || dataset.Name != config.Measurement.Dataset.Name || string(dataset.UID) != config.Measurement.Dataset.UID {
		return false
	}
	for _, workload := range workloads {
		if workload.UID == config.Measurement.WorkloadUID && workload.Name == config.Measurement.WorkloadName &&
			workload.Model != nil && workload.Model.Name == config.State.Model && string(workload.Model.UID) == config.Candidate.Inputs.ModelSpec.UID &&
			workload.Preprocessor != nil && workload.Preprocessor.Name == config.State.Preprocessor.Name && string(workload.Preprocessor.UID) == config.State.Preprocessor.UID {
			return true
		}
	}
	return false
}

func cadenceCompleted(conditions []metav1.Condition) bool {
	for _, condition := range conditions {
		if condition.Type == "ExecutionComplete" {
			return condition.Status == metav1.ConditionTrue
		}
	}
	return false
}

func cadenceOwnedBy(stage *api.EvaluationStage, run *api.EvaluationRun) bool {
	for _, owner := range stage.OwnerReferences {
		if owner.Controller != nil && *owner.Controller && owner.APIVersion == api.GroupVersion.String() && owner.Kind == "EvaluationRun" && owner.UID == run.UID {
			return true
		}
	}
	return false
}

func (r *KubernetesCadenceResolver) resolveStage(ctx context.Context, config *profilingrun.RunConfig, source *cadenceStageSource) (map[int]*firstfloor.CadenceContract, error) {
	schedule, err := source.materializedSchedule(config.Measurement.WorkloadName)
	if err != nil {
		return nil, err
	}
	artifacts, err := source.readStageArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	phase, err := source.readPhase(ctx, artifacts.phase, schedule.ID)
	if err != nil {
		return nil, err
	}
	if err := source.checkRawCollection(ctx, artifacts, phase); err != nil {
		return nil, err
	}
	trace, err := source.buildTrace(config, schedule, phase)
	if err != nil {
		return nil, err
	}
	trace.Rows, err = source.readRequests(ctx, artifacts.requests)
	if err != nil {
		return nil, err
	}
	trace.Profiles, err = r.readProfiles(ctx, phase.Start, phase.Drained)
	if err != nil {
		return nil, err
	}
	return profiler.BuildServingCadences(*config, trace)
}

func (source *cadenceStageSource) materializedSchedule(workload string) (cadenceSchedule, error) {
	binding, found := source.snapshot.Bindings[source.stage.Spec.StageID]
	if !found || len(binding.Schedule) != 1 {
		return cadenceSchedule{}, profiler.MissingServingTrace("selected stage has no unique materialized schedule")
	}
	schedule := binding.Schedule[0]
	if schedule.ID != source.stage.Spec.Row {
		return cadenceSchedule{}, profiler.MissingServingTrace("materialized schedule is not the selected stage row")
	}
	traceSchedule, found := schedule.Traces[workload]
	if !found || traceSchedule.Offered != len(traceSchedule.Offsets) || len(traceSchedule.Offsets) == 0 {
		return cadenceSchedule{}, profiler.MissingServingTrace("selected workload has no materialized arrivals")
	}
	previous := int64(-1)
	for _, offset := range traceSchedule.Offsets {
		if offset < 0 || offset < previous || float64(offset) >= schedule.Seconds*float64(time.Second) {
			return cadenceSchedule{}, profiler.MissingServingTrace("invalid materialized workload arrival offsets")
		}
		previous = offset
	}
	return schedule, nil
}

type cadenceStageArtifacts struct {
	phase, rawIndex api.EvaluationEvidenceReference
	requests        []api.EvaluationEvidenceReference
}

func (source *cadenceStageSource) readStageArtifacts(ctx context.Context) (cadenceStageArtifacts, error) {
	stage, run := source.stage, source.run
	var procedureRef *api.EvaluationEvidenceReference
	for _, procedure := range stage.Status.Procedures {
		if procedure.Step == "stage" {
			procedureRef = procedure.EvidenceRef
		}
	}
	if procedureRef == nil {
		return cadenceStageArtifacts{}, profiler.MissingServingTrace("selected stage has no retained stage artifact manifest")
	}
	var manifest struct {
		RunUID    types.UID                         `json:"runUID"`
		StageUID  types.UID                         `json:"stageUID"`
		StageID   string                            `json:"stageID"`
		Artifacts []api.EvaluationEvidenceReference `json:"artifacts"`
	}
	if err := readCadenceJSON(ctx, source.objects, *procedureRef, &manifest); err != nil {
		return cadenceStageArtifacts{}, cadenceReadError("stage manifest", err)
	}
	if manifest.RunUID != run.UID || manifest.StageUID != stage.UID || manifest.StageID != stage.Spec.StageID {
		return cadenceStageArtifacts{}, profiler.MissingServingTrace("stage artifact manifest identity mismatch")
	}
	var phaseRef, rawIndexRef *api.EvaluationEvidenceReference
	var requestObjects []api.EvaluationEvidenceReference
	for index := range manifest.Artifacts {
		object := &manifest.Artifacts[index]
		if strings.HasSuffix(object.Key, "/measured/phase.json") {
			if phaseRef != nil {
				return cadenceStageArtifacts{}, profiler.MissingServingTrace("ambiguous selected producer phase artifact")
			}
			phaseRef = object
		}
		if strings.HasSuffix(object.Key, "/measured/raw-source-index.json") {
			if rawIndexRef != nil {
				return cadenceStageArtifacts{}, profiler.MissingServingTrace("ambiguous selected raw collection index")
			}
			rawIndexRef = object
		}
		if strings.Contains(object.Key, "/artifacts/measured/obs/") && strings.HasSuffix(object.Key, "/requests.jsonl.gz") {
			requestObjects = append(requestObjects, *object)
		}
	}
	if phaseRef == nil || rawIndexRef == nil || len(requestObjects) == 0 {
		return cadenceStageArtifacts{}, profiler.MissingServingTrace("selected stage has no phase, complete raw index or immutable requests artifacts")
	}
	return cadenceStageArtifacts{phase: *phaseRef, rawIndex: *rawIndexRef, requests: requestObjects}, nil
}

func (source *cadenceStageSource) readPhase(ctx context.Context, ref api.EvaluationEvidenceReference, scheduleID string) (cadencePhaseRecord, error) {
	var phase cadencePhaseRecord
	if err := readCadenceJSON(ctx, source.objects, ref, &phase); err != nil {
		return cadencePhaseRecord{}, cadenceReadError("actual producer phase", err)
	}
	if phase.Stage.ID != scheduleID || phase.Start.IsZero() || !phase.End.After(phase.Start) || phase.Drained.Before(phase.End) {
		return cadencePhaseRecord{}, profiler.MissingServingTrace("selected phase lacks actual producer window")
	}
	return phase, nil
}

func (source *cadenceStageSource) checkRawCollection(ctx context.Context, artifacts cadenceStageArtifacts, phase cadencePhaseRecord) error {
	var collection struct {
		Complete   bool              `json:"complete"`
		Errors     []json.RawMessage `json:"errors"`
		Population struct {
			Start   time.Time `json:"startUTC"`
			End     time.Time `json:"endUTC"`
			Drained time.Time `json:"drainedUTC"`
		} `json:"requestPopulation"`
		Absent []struct {
			Hour   string `json:"hour"`
			Stream string `json:"stream"`
			Origin string `json:"origin"`
			Reason string `json:"reason"`
		} `json:"absentStreams"`
	}
	if err := readCadenceJSON(ctx, source.objects, artifacts.rawIndex, &collection); err != nil {
		return cadenceReadError("raw collection index", err)
	}
	if !collection.Complete || len(collection.Errors) != 0 || collection.Population.Start.IsZero() || collection.Population.Start.After(phase.Start) ||
		collection.Population.End.Before(phase.End) || collection.Population.Drained.Before(phase.Drained) {
		return profiler.MissingServingTrace("recorded raw collection does not cover the actual phase/drain")
	}
	for hour := phase.Start.UTC().Truncate(time.Hour); !hour.After(phase.Drained.UTC().Truncate(time.Hour)); hour = hour.Add(time.Hour) {
		name, found := hour.Format("20060102T15"), false
		for _, object := range artifacts.requests {
			found = found || strings.HasSuffix(object.Key, "/obs/"+name+"/requests.jsonl.gz")
		}
		for _, absent := range collection.Absent {
			found = found || absent.Hour == name && absent.Stream == "requests" && absent.Origin == "api" && absent.Reason == "source-hour-has-no-stream-segments"
		}
		if !found {
			return profiler.MissingServingTrace("recorded raw collection is missing requests hour %s", name)
		}
	}
	return nil
}

func (source *cadenceStageSource) buildTrace(config *profilingrun.RunConfig, schedule cadenceSchedule, phase cadencePhaseRecord) (profiler.CadenceTrace, error) {
	stage, run := source.stage, source.run
	start, found := phase.Starts[config.Measurement.WorkloadName]
	if !found || start.At.IsZero() {
		return profiler.CadenceTrace{}, profiler.MissingServingTrace("selected phase has GO timing but no actual workload producer start")
	}
	frozen := source.snapshot.Scenarios[string(stage.Spec.LoadScenarioRef.UID)]
	for name, trace := range schedule.Traces {
		if trace.Offered == 0 {
			continue
		}
		producer, found := phase.Starts[name]
		if !found || producer.At.IsZero() {
			return profiler.CadenceTrace{}, profiler.MissingServingTrace("selected sharing population has no actual producer start for %s", name)
		}
	}
	// Semantic context is the frozen declared population/schedule and routing
	// target, not a window, object key, stage UID, seed or empirical sample count.
	contextBody := struct {
		WorkloadUID string                                   `json:"workloadUID"`
		Workloads   []cadencePopulationWorkload              `json:"workloads"`
		DatasetRefs map[string]api.EvaluationObjectReference `json:"datasetRefs"`
		Arrivals    map[string]string                        `json:"arrivals"`
		Parts       []cadenceBindingPart                     `json:"parts"`
		Target      api.TargetIdentity                       `json:"target"`
		Settings    json.RawMessage                          `json:"settings,omitempty"`
	}{WorkloadUID: config.Measurement.WorkloadUID, DatasetRefs: frozen.Spec.DatasetRefs, Arrivals: frozen.Spec.ArrivalDistribution, Parts: schedule.Parts, Target: stage.Spec.Target}
	contextBody.Workloads = append([]cadencePopulationWorkload(nil), frozen.Workloads...)
	sort.Slice(contextBody.Workloads, func(i, j int) bool { return contextBody.Workloads[i].Name < contextBody.Workloads[j].Name })
	if stage.Spec.SettingsPatch != nil {
		contextBody.Settings = stage.Spec.SettingsPatch.Raw
	}
	contextJSON, err := json.Marshal(contextBody)
	if err != nil {
		return profiler.CadenceTrace{}, err
	}
	trace := profiler.CadenceTrace{LoadContext: string(contextJSON), TrialID: string(run.UID) + "/" + string(stage.UID), SourceRevision: stage.Spec.Harness.SourceSHA}
	trace.Phases, err = source.schedulePhases(config.Measurement.WorkloadName, schedule, phase, start.At, contextBody.Settings)
	if err != nil {
		return profiler.CadenceTrace{}, err
	}
	return trace, nil
}

func (source *cadenceStageSource) schedulePhases(workload string, schedule cadenceSchedule, phase cadencePhaseRecord, start time.Time, settings json.RawMessage) ([]profiler.CadenceTracePhase, error) {
	frozen := source.snapshot.Scenarios[string(source.stage.Spec.LoadScenarioRef.UID)]
	phases := make([]profiler.CadenceTracePhase, 0, len(schedule.Parts))
	cursor := start
	for _, part := range schedule.Parts {
		if part.ID == "" || math.IsNaN(part.Seconds) || math.IsInf(part.Seconds, 0) || part.Seconds <= 0 || part.Seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return nil, profiler.MissingServingTrace("invalid recorded schedule duration")
		}
		rate, found := part.Rates[workload]
		if !found || math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 {
			return nil, profiler.MissingServingTrace("recorded schedule has no finite workload rate")
		}
		end := cursor.Add(time.Duration(math.Round(part.Seconds * float64(time.Second))))
		if rate == 0 {
			// The full declared schedule remains in LoadContext above. A zero-load
			// separator has no pacing population; advance its actual window without
			// inventing G=0. Distinct selected phase IDs still prevent adjacency
			// across this interval in the physical join.
			cursor = end
			continue
		}
		sharing, err := json.Marshal(struct {
			Rates    map[string]float64 `json:"rates"`
			Target   api.TargetIdentity `json:"target"`
			Settings json.RawMessage    `json:"settings,omitempty"`
		}{part.Rates, source.stage.Spec.Target, settings})
		if err != nil {
			return nil, err
		}
		offered, err := json.Marshal(struct {
			Rate    float64 `json:"ratePerSecond"`
			Arrival string  `json:"arrival"`
			Seconds float64 `json:"seconds"`
		}{rate, frozen.Spec.ArrivalDistribution[workload], part.Seconds})
		if err != nil {
			return nil, err
		}
		windowStart, windowEnd := cadencePhaseWindow(schedule, phase, start, cursor, end)
		if !windowEnd.After(windowStart) {
			return nil, profiler.MissingServingTrace("recorded phase %s has no common actual producer window", part.ID)
		}
		phases = append(phases, profiler.CadenceTracePhase{Target: firstfloor.CadenceTargetPhase{Name: part.ID, OfferedLoad: string(offered), SharingContext: string(sharing)}, Start: windowStart, End: windowEnd})
		cursor = end
	}
	if len(phases) == 0 {
		return nil, profiler.MissingServingTrace("recorded schedule has no phases")
	}
	return phases, nil
}

func cadencePhaseWindow(schedule cadenceSchedule, phase cadencePhaseRecord, start, cursor, end time.Time) (time.Time, time.Time) {
	windowStart, windowEnd := cursor, minCadenceTime(end, phase.End)
	for name, arrival := range schedule.Traces {
		if arrival.Offered == 0 {
			continue
		}
		producer := phase.Starts[name].At
		otherStart := producer.Add(cursor.Sub(start))
		otherEnd := producer.Add(end.Sub(start))
		if otherStart.After(windowStart) {
			windowStart = otherStart
		}
		windowEnd = minCadenceTime(windowEnd, otherEnd)
	}
	return windowStart, windowEnd
}

func (source *cadenceStageSource) readRequests(ctx context.Context, requestObjects []api.EvaluationEvidenceReference) ([]profiler.CadenceRequestRow, error) {
	sort.Slice(requestObjects, func(i, j int) bool { return requestObjects[i].Key < requestObjects[j].Key })
	var requests []profiler.CadenceRequestRow
	for _, object := range requestObjects {
		stream, err := source.objects.Open(ctx, cadenceObjectIdentity(object))
		if err != nil {
			return nil, cadenceReadError("requests artifact", err)
		}
		compressed, err := gzip.NewReader(stream)
		if err != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("decode actual gzip requests %s: %w", object.Key, err)
		}
		rows, readErr := profiler.ReadCadenceRequests(compressed, firstfloor.CadenceObject{Store: object.Store, Key: object.Key, Size: object.Size})
		closeErr := compressed.Close()
		streamErr := stream.Close()
		if err := errors.Join(readErr, closeErr, streamErr); err != nil {
			return nil, err
		}
		requests = append(requests, rows...)
	}
	return requests, nil
}

func minCadenceTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func cadenceObjectIdentity(ref api.EvaluationEvidenceReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: ref.Store, Key: ref.Key, Size: ref.Size}
}

func cadenceReadError(source string, err error) error {
	if errors.Is(err, evidencestore.ErrNotFound) {
		return profiler.MissingServingTrace("retained %s is unavailable: %v", source, err)
	}
	return fmt.Errorf("read retained cadence %s: %w", source, err)
}

// readCadenceJSON propagates decode and close failures at the evidence boundary.
func readCadenceJSON(ctx context.Context, objects evidencestore.Reader, ref api.EvaluationEvidenceReference, target any) (err error) {
	if ref.Store == "" || ref.Key == "" || ref.Size <= 0 {
		return profiler.MissingServingTrace("immutable evidence identity is incomplete")
	}
	stream, err := objects.Open(ctx, cadenceObjectIdentity(ref))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, stream.Close()) }()
	decoder := json.NewDecoder(io.LimitReader(stream, ref.Size+1))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("retained evidence contains trailing or oversized JSON: %v", err)
	}
	return nil
}

func (r *KubernetesCadenceResolver) readProfiles(ctx context.Context, start, end time.Time) (map[string]profiler.CadenceTraceProfile, error) {
	// Catalog is not in the Evaluation collector's two-stream export. Discover
	// real immutable archive identities through the existing serving S3 lister;
	// never guess object existence or silently reinterpret another stream.
	objects, err := r.observations.List(ctx, "serving/observations.")
	if err != nil {
		return nil, fmt.Errorf("list retained serving catalogs: %w", err)
	}
	profiles := make(map[string]profiler.CadenceTraceProfile)
	for _, object := range objects {
		if !strings.HasSuffix(object.Key, "/catalog.jsonl.gz") {
			continue
		}
		parts := strings.Split(object.Key, "/")
		if len(parts) < 3 {
			continue
		}
		hour, err := time.Parse("20060102T15", parts[len(parts)-2])
		if err != nil || hour.Before(start.UTC().Truncate(time.Hour)) || hour.After(end.UTC().Truncate(time.Hour)) {
			continue
		}
		stream, err := r.observations.Open(ctx, object)
		if err != nil {
			return nil, cadenceReadError("serving catalog", err)
		}
		compressed, err := gzip.NewReader(stream)
		if err != nil {
			_ = stream.Close()
			return nil, err
		}
		decoder := json.NewDecoder(compressed)
		for {
			var row struct {
				Kind string          `json:"kind"`
				Key  string          `json:"key"`
				Body json.RawMessage `json:"body"`
			}
			err = decoder.Decode(&row)
			if err != nil {
				break
			}
			if row.Kind != "profile" {
				continue
			}
			var profile profiler.CadenceTraceProfile
			if err = json.Unmarshal(row.Body, &profile); err != nil {
				break
			}
			profiles[row.Key] = profile
		}
		closeErr := errors.Join(compressed.Close(), stream.Close())
		if err != io.EOF {
			return nil, fmt.Errorf("decode retained serving catalog %s: %w", object.Key, err)
		}
		if closeErr != nil {
			return nil, closeErr
		}
	}
	return profiles, nil
}
