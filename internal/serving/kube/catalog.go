package kube

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	campaignevidence "github.com/lab-paper-code/chill/internal/profiling/campaign/evidence"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const invalidEvidenceCode = "InvalidEvidence"
const catalogCompletePhase = "Complete"

// Catalog lists native Campaigns and retains one verified terminal profile per
// name/UID/resourceVersion. Inference never reads campaigns or downloads
// evidence; callers own the mutable parts of each returned view.
// Loader verification proves stored structure and binding, not admission.
// Batch distributions retain every raw RUNNER SERVICE sample in nanoseconds,
// never client roundtrip or backend-call time. Energy uses the existing exact
// incremental wall-energy/item estimator; memory is measured lifetime peak.
type Catalog struct {
	store           *Store
	objects         evidencestore.LocationReader
	loader          *profilerevidence.Loader
	maxObjectBytes  int64
	evidence        campaignevidence.Reader
	current         map[types.NamespacedName]catalogProfile
	sourceMu        sync.RWMutex
	sourceCheckedAt time.Time
	sourceError     string
	revision        string
}

type catalogProfile struct {
	uid     types.UID
	version string
	profile serving.Profile
	facts   serving.MeasurementFacts
}

type CatalogIssue struct {
	Source types.NamespacedName `json:"source"`
	Code   string               `json:"code"`
	Reason string               `json:"reason"`
}

type CatalogView struct {
	Profiles            []serving.Profile                        `json:"profiles"`
	CurrentMeasurements []profilederivation.MeasurementSelection `json:"currentMeasurements"`
	Insufficiencies     []CatalogIssue                           `json:"insufficiencies"`
	ReuseChecks         []serving.ProfileReuseCheck              `json:"reuseChecks,omitempty"`
	MeasurementFacts    map[string]serving.MeasurementFacts      `json:"measurementFacts"`
	SourceCheckedAt     time.Time                                `json:"-"`
	SourceError         string                                   `json:"-"`
	Revision            string                                   `json:"-"`
}

func NewCatalog(store *Store, objects evidencestore.LocationReader, maxObjectBytes int64) (*Catalog, error) {
	if store == nil || store.client == nil {
		return nil, errors.New("native Kubernetes store is required")
	}
	loader, err := profilerevidence.NewLoader(objects, maxObjectBytes)
	if err != nil {
		return nil, err
	}
	catalog := &Catalog{
		store: store, objects: objects, loader: loader, maxObjectBytes: maxObjectBytes,
		current: make(map[types.NamespacedName]catalogProfile),
	}
	catalog.evidence = campaignevidence.Reader{
		Runs: store.client, ReadObject: catalog.readObject,
		LoadActive: func(ctx context.Context, _ *edgev1alpha1.ProfilingRun, identity evidencestore.ObjectIdentity) (profilerevidence.VerifiedEvidence, error) {
			return catalog.loader.Load(ctx, identity)
		},
		LoadIdle: func(ctx context.Context, _ *edgev1alpha1.ProfilingRun, identity evidencestore.ObjectIdentity) (profilerevidence.VerifiedIdleEvidence, error) {
			return catalog.loader.LoadIdle(ctx, identity)
		},
	}
	return catalog, nil
}

// Refresh reads every Complete campaign in namespaces. A bad source cannot
// contribute a partial profile; other sources remain usable. Cancellation
// returns no view, so callers cannot publish a partial refresh.
func (c *Catalog) Refresh(ctx context.Context, namespaces []string) (view CatalogView, err error) {
	defer func() {
		c.sourceMu.Lock()
		defer c.sourceMu.Unlock()
		if err != nil {
			c.sourceError = err.Error()
			return
		}
		c.sourceCheckedAt, c.sourceError, c.revision = view.SourceCheckedAt, "", view.Revision
	}()
	view = CatalogView{Profiles: []serving.Profile{}, Insufficiencies: []CatalogIssue{}, MeasurementFacts: map[string]serving.MeasurementFacts{}}
	var revisions []string
	seen := map[types.NamespacedName]struct{}{}
	reuseRequests := map[string]string{}
	var candidates []profilederivation.MeasurementSelection
	var replacedProfiles []serving.Profile
	names, replacements := map[string]string{}, map[string]string{}
	for _, namespace := range namespaces {
		campaigns := &edgev1alpha1.BatchEnergyOptimizationList{}
		if err := c.store.client.List(ctx, campaigns, client.InNamespace(namespace)); err != nil {
			return CatalogView{}, err
		}
		for i := range campaigns.Items {
			campaign := &campaigns.Items[i]
			id := campaign.Namespace + "/" + campaign.Name + "/" + string(campaign.UID)
			names[campaign.Namespace+"/"+campaign.Name] = id
			if name := campaign.Annotations[profilederivation.ReplacesMeasurementAnnotation]; name != "" && campaign.DeletionTimestamp == nil {
				replacements[id] = campaign.Namespace + "/" + name
			}
		}
		replaced := profilederivation.ReplacedMeasurements(replacements, names)
		replacementDiagnostics := replacementCampaignSelection(campaigns.Items)
		for i := range campaigns.Items {
			campaign := &campaigns.Items[i]
			revisions = append(revisions, campaign.Namespace+"/"+campaign.Name+"/"+string(campaign.UID)+"/"+campaign.ResourceVersion)
			id := campaign.Namespace + "/" + campaign.Name + "/" + string(campaign.UID)
			if campaign.DeletionTimestamp != nil {
				continue
			}
			state := campaign.Spec.State.Candidate
			candidateIndex := len(candidates)
			candidates = append(candidates, profilederivation.MeasurementSelection{
				ID: id, Model: state.ModelSpec, DeviceClass: state.DeviceClass, ExecutionPath: state.ExecutionPath, PowerMode: state.PowerMode,
				MeasuredAt: profilederivation.MeasurementTime(time.Time{}, campaign.Status.Conditions),
			})
			source := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
			if reason := replacementDiagnostics[campaign.Name]; reason != "" {
				view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: "ReplacementSelection", Reason: reason})
			}
			if replaced[id] {
				if campaign.Status.Phase == catalogCompletePhase && campaign.Status.ObservedGeneration == campaign.Generation {
					seen[source] = struct{}{}
					profile, _, readErr := c.readProfile(ctx, campaign)
					if ctx.Err() != nil {
						return CatalogView{}, ctx.Err()
					}
					if readErr == nil && len(profile.Batches) > 0 {
						replacedProfiles = append(replacedProfiles, profile)
					}
				}
				continue
			}
			if campaign.Status.Phase != catalogCompletePhase || campaign.Status.ObservedGeneration != campaign.Generation {
				continue
			}
			if reason, invalid := campaign.Annotations[profilingrun.InvalidAnnotation]; invalid {
				view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: measurementInvalidCode, Reason: reason})
				continue
			}
			if _, duplicate := seen[source]; duplicate {
				continue
			}
			seen[source] = struct{}{}
			profile, issues, err := c.readProfile(ctx, campaign)
			if err != nil {
				if ctx.Err() != nil {
					return CatalogView{}, ctx.Err()
				}
				view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: "InvalidEvidence", Reason: err.Error()})
				continue
			}
			view.Insufficiencies = append(view.Insufficiencies, issues...)
			candidates[candidateIndex].MeasuredAt = profilederivation.MeasurementTime(profile.MeasuredAt, campaign.Status.Conditions)
			if len(profile.Batches) > 0 {
				view.Profiles = append(view.Profiles, profile)
				view.MeasurementFacts[profile.ID] = cloneMeasurementFacts(c.current[source].facts)
				if request, found := campaign.Annotations[profileReuseAnnotation]; found {
					reuseRequests[profile.ID] = request
				}
			}
		}
	}
	for source := range c.current {
		if _, listed := seen[source]; !listed {
			delete(c.current, source)
		}
	}
	view.Profiles, err = serving.ExcludeReplacedConditionProfiles(append(view.Profiles, replacedProfiles...), profilederivation.ReplacedMeasurements(replacements, names))
	if err != nil {
		return CatalogView{}, err
	}
	view, err = applyProfileReuse(view, reuseRequests)
	if err != nil {
		return CatalogView{}, err
	}
	view.CurrentMeasurements = profilederivation.CurrentMeasurements(candidates, profilederivation.ReplacedMeasurements(replacements, names))
	slices.Sort(revisions)
	view.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(revisions, "\x00"))))
	view.SourceCheckedAt = time.Now().UTC()
	return view, nil
}

// SourceStatus reports the last real source check, not the snapshot read time.
func (c *Catalog) SourceStatus() (time.Time, string, string) {
	c.sourceMu.RLock()
	defer c.sourceMu.RUnlock()
	return c.sourceCheckedAt, c.sourceError, c.revision
}

// Operator replacement is selection, not a second admission policy. Differences
// and ineffective annotations are diagnostics; normal evidence safety remains.
func replacementCampaignSelection(campaigns []edgev1alpha1.BatchEnergyOptimization) map[string]string {
	diagnostics := make(map[string]string)
	byName := make(map[string]*edgev1alpha1.BatchEnergyOptimization, len(campaigns))
	for i := range campaigns {
		byName[campaigns[i].Name] = &campaigns[i]
	}
	for i := range campaigns {
		replacement := &campaigns[i]
		name := replacement.Annotations[profilederivation.ReplacesMeasurementAnnotation]
		if name == "" || replacement.DeletionTimestamp != nil {
			continue
		}
		original := byName[name]
		if original == nil || original == replacement {
			diagnostics[replacement.Name] = "replacement ignored: target must be a distinct existing campaign"
			continue
		}
		diagnostics[name] = "superseded by " + replacement.Name + "; original evidence retained"
		if !reflect.DeepEqual(original.Spec, replacement.Spec) {
			diagnostics[replacement.Name] = "operator replacement changes specification; selection retained as declared"
		}
	}
	return diagnostics
}

func completeNativeProfile(profile serving.Profile) bool {
	condition := profile.Condition
	if condition.RuntimeFamily != profilingrun.FirstfloorRuntimeFamily || condition.Firstfloor == nil ||
		profile.RuntimeMetadata == nil || profile.RuntimeMetadata.Runtime != condition.Backend || len(profile.Batches) == 0 ||
		profile.Batches[0].Batch != 1 {
		return false
	}
	previous := 0
	for _, batch := range profile.Batches {
		if batch.Batch <= previous || batch.Batch > int(condition.Firstfloor.MaxBatchSize) ||
			!slices.Contains(condition.PlannedBatchSizes, batch.Batch) ||
			!slices.Contains(profile.RuntimeMetadata.InitializedBatches, batch.Batch) {
			return false
		}
		previous = batch.Batch
	}
	return true
}

// CurrentProfiles is the executable selection, separate from public history.
// A latest failed measurement has no profile, so cannot revive an old success.
func (view CatalogView) CurrentProfiles() []serving.Profile {
	current := make(map[string]bool, len(view.CurrentMeasurements))
	for _, measurement := range view.CurrentMeasurements {
		current[measurement.ID] = true
	}
	profiles := make([]serving.Profile, 0, len(current))
	for _, profile := range view.Profiles {
		id := profile.ID
		if profile.Reuse != nil {
			id = profile.Reuse.ConfirmationProfileID
		}
		if current[id] {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

//nolint:gocyclo // This refresh boundary follows the explicit ordered campaign, plan, Run, and raw-evidence checks.
func (c *Catalog) readProfile(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization) (serving.Profile, []CatalogIssue, error) {
	source := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
	if campaign.Status.Result == nil {
		delete(c.current, source)
		return serving.Profile{}, nil, errors.New("completed campaign has no terminal result reference")
	}
	if admitted, found := c.current[source]; found &&
		admitted.uid == campaign.UID && admitted.version == campaign.ResourceVersion {
		return cloneCatalogProfile(admitted.profile), nil, nil
	}
	// A new native identity/version must pass the full evidence boundary.
	// Never retain the old success if that new admission fails.
	delete(c.current, source)
	retained, err := c.evidence.Read(ctx, campaign)
	if err != nil {
		return serving.Profile{}, nil, err
	}
	resultReference := retained.ResultIdentity
	state := campaign.Spec.State
	plan := retained.Plan
	condition := plan.Shared()
	if condition.RuntimeFamily != profilingrun.FirstfloorRuntimeFamily {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: "standalone experiment execution is not same-firstfloor serving evidence"}}, nil
	}

	trials, err := c.evidence.ReadTrials(ctx, campaign, retained)
	if err != nil {
		return serving.Profile{}, nil, err
	}
	active, idle := trials.Active, trials.Idle
	measuredAt, evidenceReferences := trials.MeasuredAt, trials.Evidence
	activeSources, idleSources := trials.ActiveSources, trials.IdleSources
	if len(active) == 0 || len(idle) == 0 {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: "completed active and model-loaded idle raw trials are required"}}, nil
	}
	// The existing projector binds every trial to the frozen plan. It does not
	// apply a new environment quality or scientific admission policy.
	idleTrials := make([]profilederivation.IdleTrial, len(idle))
	var idleMemory profilederivation.MemoryObservation
	for index, verified := range idle {
		if _, err := profilerprojection.ProjectVerifiedCurveState(plan, active[0], verified); err != nil {
			return serving.Profile{}, nil, err
		}
		idleTrials[index], err = profilerprojection.IdleTrial(verified, idle[0])
		if err != nil {
			return serving.Profile{}, nil, err
		}
		execution, raw, err := verified.Records()
		if err != nil {
			return serving.Profile{}, nil, err
		}
		idleMemory = profilederivation.MaxMemory(idleMemory, profilederivation.ObservedMemory(
			raw.Runtime.MemoryBytes, raw.Runtime.MemorySource, "idle",
			profilederivation.RawEvidenceIdentity{Store: execution.RawMeasurement.Store, Key: execution.RawMeasurement.Key, Size: execution.RawMeasurement.Size}))
	}
	confidence, err := campaign.Spec.PointPolicy.Confidence()
	if err != nil {
		return serving.Profile{}, nil, err
	}
	energyPolicy := profilederivation.PointPolicy{
		Version: "serving-energy-resolution-v1", MinimumTrials: int(campaign.Spec.TrialsPerBatch), ConfidenceLevel: confidence,
	}
	baseline, err := profilederivation.DeriveIdleBaseline(idleTrials, energyPolicy)
	if err != nil {
		return serving.Profile{}, nil, fmt.Errorf("restore measured idle baseline: %w", err)
	}
	_, idleRaw, err := idle[0].Records()
	if err != nil {
		return serving.Profile{}, nil, err
	}
	metricSource := idleRaw.Metrics.Source
	batches := make(map[int]*catalogBatch)
	var runtimeMetadata *profilingrun.FirstfloorRuntimeEvidence
	var delivery profilingrun.CandidateRuntimeDelivery
	for _, verified := range active {
		if _, err := profilerprojection.ProjectVerifiedCurveState(plan, verified, idle[0]); err != nil {
			return serving.Profile{}, nil, err
		}
		execution, raw, err := verified.Records()
		if err != nil {
			return serving.Profile{}, nil, err
		}
		if !reflect.DeepEqual(metricSource, raw.Metrics.Source) {
			return serving.Profile{}, nil, errors.New("active and idle wall-power source identities differ")
		}
		if raw.Runtime.Firstfloor != nil {
			if runtimeMetadata == nil {
				runtimeMetadata, delivery = raw.Runtime.Firstfloor, raw.RunConfig.Delivery
			} else if !reflect.DeepEqual(sharedRuntimeFacts(runtimeMetadata.Runtime), sharedRuntimeFacts(raw.Runtime.Firstfloor.Runtime)) {
				return serving.Profile{}, nil, errors.New("firstfloor trials have different actual initialized runner conditions")
			}
		}
		trial, err := profilerprojection.Trial(verified)
		if err != nil {
			return serving.Profile{}, nil, err
		}
		energy, err := profilederivation.IncrementalWorkTrial(trial, baseline)
		if err != nil {
			return serving.Profile{}, nil, fmt.Errorf("derive measured energy for batch %d: %w", trial.Batch, err)
		}
		batch := batches[trial.Batch]
		if batch == nil {
			batch = &catalogBatch{profile: serving.BatchProfile{Batch: trial.Batch}}
			batches[trial.Batch] = batch
		}
		if err := batch.add(trial, energy, raw.Runtime.MemoryBytes); err != nil {
			return serving.Profile{}, nil, err
		}
		batch.memory = profilederivation.MaxMemory(batch.memory, profilederivation.ObservedMemory(
			raw.Runtime.MemoryBytes, raw.Runtime.MemorySource, "active",
			profilederivation.RawEvidenceIdentity{Store: execution.RawMeasurement.Store, Key: execution.RawMeasurement.Key, Size: execution.RawMeasurement.Size}))
		quality := profilerprojection.MeasurementQuality(raw.RunConfig, raw.Metrics, trial.WindowStart, trial.WindowEnd, condition.ActiveProtocol)
		quality.TrialID = trial.TrialID
		batch.quality = append(batch.quality, quality)
	}
	if runtimeMetadata != nil {
		for _, verified := range idle {
			_, raw, err := verified.Records()
			if err != nil {
				return serving.Profile{}, nil, err
			}
			if !reflect.DeepEqual(metricSource, raw.Metrics.Source) {
				return serving.Profile{}, nil, errors.New("idle wall-power source identities differ")
			}
			if raw.Runtime.Firstfloor == nil {
				return serving.Profile{}, nil, errors.New("idle firstfloor actual runner facts are missing")
			}
			activeFacts, idleFacts := sharedRuntimeFacts(runtimeMetadata.Runtime), sharedRuntimeFacts(raw.Runtime.Firstfloor.Runtime)
			// Warmup is outside both measurement windows; active protocol performs
			// warmup and the model-loaded idle protocol performs none.
			activeFacts.WarmupIterations, idleFacts.WarmupIterations = 0, 0
			if !reflect.DeepEqual(activeFacts, idleFacts) {
				return serving.Profile{}, nil, errors.New("idle and active actual firstfloor runner conditions differ")
			}
		}
	}
	profile := serving.Profile{
		ID: source.String() + "/" + string(campaign.UID), Reference: resultReference,
		NodeName: state.TargetNode, Condition: condition, Evidence: evidenceReferences,
		ConfidenceLevel: confidence, IdleBaselineWatts: baseline.ModelLoadedIdleMeanWallWatts,
		MeasuredAt:     measuredAt,
		MetricSource:   metricSource,
		TrialsPerBatch: int(campaign.Spec.TrialsPerBatch),
	}
	facts := serving.MeasurementFacts{
		Request:         profilederivation.MeasurementRequestReference{Namespace: campaign.Namespace, Name: campaign.Name, UID: string(campaign.UID)},
		ResultReference: profilederivation.RawEvidenceIdentity{Store: resultReference.Store, Key: resultReference.Key, Size: resultReference.Size},
		Condition:       condition, PointPolicy: energyPolicy, Batches: []profilederivation.MeasurementBatchFacts{},
	}
	if runtimeMetadata != nil {
		profile.RuntimeMetadata = &runtimeMetadata.Runtime
		if profile.RuntimeDelivery, err = json.Marshal(delivery); err != nil {
			return serving.Profile{}, nil, err
		}
	}
	var issues []CatalogIssue
	for _, batchSize := range condition.PlannedBatchSizes {
		batch := batches[batchSize]
		if batch == nil && batchSize == 1 {
			issues = append(issues, CatalogIssue{Source: source, Code: "MeasurementEvidenceMissing", Reason: "firstfloor serving requires exact measured batch 1"})
		}
		if batch == nil {
			continue // A terminal minimum search need not measure the whole plan.
		}
		if batch.missingMemory {
			issues = append(issues, CatalogIssue{Source: source, Code: "MemoryEvidenceMissing", Reason: fmt.Sprintf("batch %d has no measured runtime memory bytes for every trial", batchSize)})
			continue
		}
		if batch.trials < int(campaign.Spec.TrialsPerBatch) {
			issues = append(issues, CatalogIssue{Source: source, Code: "MeasurementEvidenceMissing", Reason: fmt.Sprintf("batch %d has %d completed raw trials; campaign requires %d", batchSize, batch.trials, campaign.Spec.TrialsPerBatch)})
			continue
		}
		memory := profilederivation.MaxMemory(batch.memory, idleMemory)
		batch.profile.MemoryBytes, batch.profile.MemoryBasis = memory.Bytes, memory.Basis
		batch.profile.EnergyPerInputJoules = batch.energySum / float64(batch.trials)
		point, err := profilederivation.Aggregate(batch.energyTrials, energyPolicy)
		if err != nil {
			return serving.Profile{}, nil, fmt.Errorf("derive measured energy confidence for batch %d: %w", batchSize, err)
		}
		if batch.profile.EnergyPerInputJoules < 0 {
			lower := point.MeanIncrementalWorkJoulesPerItem - point.Confidence.HalfWidth
			upper := point.MeanIncrementalWorkJoulesPerItem + point.Confidence.HalfWidth
			if upper < 0 {
				issues = append(issues, CatalogIssue{Source: source, Code: "NegativeIncrementalEnergy",
					Reason: fmt.Sprintf("batch %d incremental energy %g J/item has %g%% CI [%g, %g] J/item below zero; remeasurement required", batchSize, point.MeanIncrementalWorkJoulesPerItem, confidence*100, lower, upper)})
				continue
			}
			batch.profile.EnergyPerInputJoules = 0
			batch.profile.EnergyBelowResolution = true
		}
		profile.Batches = append(profile.Batches, batch.profile)
		row := profilederivation.MeasurementPointFacts(point, baseline, batch.clientLatencies, batch.completedItems, batch.completedCalls)
		row.PowerSource = profilerprojection.MeasurementPowerSource(metricSource)
		row.Quality = append(row.Quality, batch.quality...)
		for _, verified := range idle {
			execution, raw, err := verified.Records()
			if err != nil {
				return serving.Profile{}, nil, err
			}
			windowStart, windowEnd, err := raw.Runtime.MeasurementWindow()
			if err != nil {
				return serving.Profile{}, nil, err
			}
			quality := profilerprojection.MeasurementQuality(raw.RunConfig, raw.Metrics, windowStart, windowEnd, condition.IdleProtocol)
			quality.TrialID = execution.Terminal.Pod.UID
			row.Quality = append(row.Quality, quality)
		}
		for _, reference := range append(slices.Clone(idleSources), activeSources[batchSize]...) {
			row.Sources = append(row.Sources, profilederivation.RawEvidenceIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size})
		}
		facts.Batches = append(facts.Batches, row)
		profile.Batches[len(profile.Batches)-1].MeanWallWatts = batch.wallPowerSum / float64(batch.trials)
	}
	if len(issues) != 0 {
		return serving.Profile{}, issues, nil
	}
	if !completeNativeProfile(profile) {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: "firstfloor requires exact measured batch 1 and initialized measured sizes within selected maxBatchSize"}}, nil
	}
	c.current[source] = catalogProfile{
		uid: campaign.UID, version: campaign.ResourceVersion, profile: cloneCatalogProfile(profile), facts: cloneMeasurementFacts(facts),
	}
	return profile, issues, nil
}

// CPU observations describe shared execution, not a frozen resource allocation.
// Return a comparison copy without changing the retained raw observations.
func sharedRuntimeFacts(facts firstfloor.RuntimeMetadata) firstfloor.RuntimeMetadata {
	facts.CPUAffinity, facts.CPUSet, facts.CgroupCPUMax = nil, "", ""
	facts.IntraOpThreads, facts.InterOpThreads = 0, 0
	return facts
}

// Only mutable ownership is copied; source facts are not decoded or validated
// again. Callers cannot mutate the admitted entry through returned slices/maps.
func cloneCatalogProfile(profile serving.Profile) serving.Profile {
	profile.Evidence = slices.Clone(profile.Evidence)
	profile.Batches = slices.Clone(profile.Batches)
	for i := range profile.Batches {
		profile.Batches[i].DurationNanos = slices.Clone(profile.Batches[i].DurationNanos)
	}
	profile.Condition = profilederivation.CloneCurvePlanSpec(profile.Condition)
	if profile.RuntimeMetadata != nil {
		metadata := *profile.RuntimeMetadata
		metadata.ModelFiles = slices.Clone(metadata.ModelFiles)
		metadata.Inputs = slices.Clone(metadata.Inputs)
		metadata.Outputs = slices.Clone(metadata.Outputs)
		metadata.Providers = slices.Clone(metadata.Providers)
		metadata.AvailableProviders = slices.Clone(metadata.AvailableProviders)
		metadata.CPUAffinity = slices.Clone(metadata.CPUAffinity)
		metadata.InitializedBatches = slices.Clone(metadata.InitializedBatches)
		metadata.RKNN = slices.Clone(metadata.RKNN)
		profile.RuntimeMetadata = &metadata
	}
	if profile.Reuse != nil {
		check := *profile.Reuse
		check.Batches = slices.Clone(check.Batches)
		if check.IdlePowerRelativeDifference != nil {
			value := *check.IdlePowerRelativeDifference
			check.IdlePowerRelativeDifference = &value
		}
		if check.Record != nil {
			reference := *check.Record
			check.Record = &reference
		}
		profile.Reuse = &check
	}
	profile.RuntimeDelivery = slices.Clone(profile.RuntimeDelivery)
	profile.MetricSource.MetricLabels = maps.Clone(profile.MetricSource.MetricLabels)
	if profile.MetricSource.ThermalMetrics != nil {
		thermal := *profile.MetricSource.ThermalMetrics
		profile.MetricSource.ThermalMetrics = &thermal
	}
	return profile
}

type catalogBatch struct {
	profile         serving.BatchProfile
	trials          int
	energySum       float64
	energyTrials    []profilederivation.EnergyTrial
	missingMemory   bool
	memory          profilederivation.MemoryObservation
	wallPowerSum    float64
	clientLatencies []float64
	completedItems  int
	completedCalls  int
	quality         []profilederivation.MeasurementTrialQuality
}

func (b *catalogBatch) add(trial profilederivation.Trial, energy profilederivation.EnergyTrial, memoryBytes uint64) error {
	if len(trial.NativeSamples) == 0 || len(trial.NativeSamples) != len(trial.LatencyMilliseconds) ||
		math.IsNaN(energy.IncrementalWorkJoulesPerItem) || math.IsInf(energy.IncrementalWorkJoulesPerItem, 0) {
		return errors.New("correlated runner service or measured energy evidence is missing or invalid")
	}
	wallPower := energy.ActiveWindowWallJoulesPerItem / energy.IdleBaselineSecondsPerItem
	if !(energy.IdleBaselineSecondsPerItem > 0) || wallPower < 0 || math.IsNaN(wallPower) || math.IsInf(wallPower, 0) {
		return errors.New("measured active wall power or duration is missing or invalid")
	}
	for _, sample := range trial.NativeSamples {
		nanos := sample.RunnerServiceMilliseconds * 1e6
		if sample.BatchSize != trial.Batch || sample.BatchID == 0 || nanos <= 0 || math.IsNaN(nanos) || math.IsInf(nanos, 0) {
			return errors.New("raw runner service cannot be represented as finite positive nanoseconds")
		}
		b.profile.DurationNanos = append(b.profile.DurationNanos, nanos)
	}
	b.trials++
	b.clientLatencies = append(b.clientLatencies, trial.LatencyMilliseconds...)
	b.completedItems += trial.CompletedItems
	b.completedCalls += trial.CompletedCalls
	b.energySum += energy.IncrementalWorkJoulesPerItem
	// Confidence needs only trial sufficient statistics, not another sample copy.
	energy.NativeSamples, energy.ClientLatencyMilliseconds = nil, nil
	b.energyTrials = append(b.energyTrials, energy)
	b.wallPowerSum += wallPower
	if math.IsNaN(b.energySum) || math.IsInf(b.energySum, 0) || math.IsNaN(b.wallPowerSum) || math.IsInf(b.wallPowerSum, 0) {
		return errors.New("measured energy or wall power aggregate is not finite")
	}
	b.missingMemory = b.missingMemory || memoryBytes == 0
	return nil
}

func (c *Catalog) readObject(ctx context.Context, identity evidencestore.ObjectIdentity) ([]byte, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	if identity.Size > c.maxObjectBytes {
		return nil, errors.New("catalog object exceeds configured byte limit")
	}
	stream, err := c.objects.OpenLocation(ctx, identity.Location())
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, identity.Size+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := evidencestore.ValidateSize(bytes.NewReader(payload), identity.Size); err != nil {
		return nil, err
	}
	return payload, nil
}
