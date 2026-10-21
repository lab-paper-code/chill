package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"reflect"
	"slices"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
	confidenceLevel float64
	current         map[types.NamespacedName]catalogProfile
}

type catalogProfile struct {
	uid     types.UID
	version string
	profile serving.Profile
}

type CatalogIssue struct {
	Source types.NamespacedName `json:"source"`
	Code   string               `json:"code"`
	Reason string               `json:"reason"`
}

type CatalogView struct {
	Profiles        []serving.Profile `json:"profiles"`
	Insufficiencies []CatalogIssue    `json:"insufficiencies"`
}

func NewCatalog(store *Store, objects evidencestore.LocationReader, maxObjectBytes int64, confidenceLevel float64) (*Catalog, error) {
	if store == nil || store.client == nil {
		return nil, errors.New("native Kubernetes store is required")
	}
	if !(confidenceLevel > 0 && confidenceLevel < 1) {
		return nil, errors.New("profile confidence level must be between zero and one")
	}
	loader, err := profilerevidence.NewLoader(objects, maxObjectBytes)
	if err != nil {
		return nil, err
	}
	return &Catalog{
		store: store, objects: objects, loader: loader, maxObjectBytes: maxObjectBytes, confidenceLevel: confidenceLevel,
		current: make(map[types.NamespacedName]catalogProfile),
	}, nil
}

// Refresh reads every Complete campaign in namespaces. A bad source cannot
// contribute a partial profile; other sources remain usable. Cancellation
// returns no view, so callers cannot publish a partial refresh.
func (c *Catalog) Refresh(ctx context.Context, namespaces []string) (CatalogView, error) {
	view := CatalogView{Profiles: []serving.Profile{}, Insufficiencies: []CatalogIssue{}}
	seen := map[types.NamespacedName]struct{}{}
	for _, namespace := range namespaces {
		campaigns := &edgev1alpha1.BatchEnergyOptimizationList{}
		if err := c.store.client.List(ctx, campaigns, client.InNamespace(namespace)); err != nil {
			return CatalogView{}, err
		}
		for i := range campaigns.Items {
			campaign := &campaigns.Items[i]
			if campaign.DeletionTimestamp != nil || campaign.Status.Phase != catalogCompletePhase || campaign.Status.ObservedGeneration != campaign.Generation {
				continue
			}
			source := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
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
			if len(profile.Batches) > 0 {
				view.Profiles = append(view.Profiles, profile)
			}
		}
	}
	for source := range c.current {
		if _, listed := seen[source]; !listed {
			delete(c.current, source)
		}
	}
	var err error
	view.Profiles, err = latestConditionProfiles(view.Profiles)
	if err != nil {
		return CatalogView{}, err
	}
	return view, nil
}

func completeNativeProfile(profile serving.Profile) bool {
	condition := profile.Condition
	if condition.RuntimeFamily != profilingrun.FirstfloorRuntimeFamily || condition.Firstfloor == nil ||
		profile.RuntimeMetadata == nil || profile.RuntimeMetadata.Runtime != condition.Backend || len(profile.Batches) == 0 ||
		profile.Batches[0].Batch != 1 {
		return false
	}
	if len(condition.CPU.AffinityCPUs) != 0 && !slices.Equal(condition.CPU.AffinityCPUs, profile.RuntimeMetadata.CPUAffinity) {
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

// Plan names, campaign UIDs and SLOs are not execution conditions; source
// completion decides the latest success.
func latestConditionProfiles(profiles []serving.Profile) ([]serving.Profile, error) {
	result := make([]serving.Profile, 0, len(profiles))
	indices := make(map[string]int, len(profiles))
	for _, profile := range profiles {
		condition := profile.Condition
		condition.Name = ""
		condition.PlannedBatchSizes = nil
		payload, err := json.Marshal(struct {
			Condition    profilederivation.CurvePlanSpec `json:"condition"`
			MetricSource metricobserver.SourceIdentity   `json:"metricSource"`
		}{condition, profile.MetricSource})
		if err != nil {
			return nil, fmt.Errorf("encode frozen profile condition: %w", err)
		}
		key := string(payload)
		if index, found := indices[key]; found {
			old := result[index]
			if profile.MeasuredAt.After(old.MeasuredAt) ||
				(profile.MeasuredAt.Equal(old.MeasuredAt) && profile.ID < old.ID) {
				result[index] = profile
			}
			continue
		}
		indices[key] = len(result)
		result = append(result, profile)
	}
	return result, nil
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
	resultReference := apiObjectIdentity(campaign.Status.Result)
	payload, err := c.readObject(ctx, resultReference)
	if err != nil {
		return serving.Profile{}, nil, fmt.Errorf("read terminal result: %w", err)
	}
	result, err := batchenergyoptimization.DecodeResultRecord(payload)
	if err != nil {
		return serving.Profile{}, nil, err
	}
	resultSpec := result.Spec()
	if campaign.Status.Result.SchemaVersion != result.SchemaVersion() || string(resultSpec.Outcome) != campaign.Status.Outcome {
		return serving.Profile{}, nil, errors.New("terminal result schema or outcome contradicts campaign status")
	}
	state := campaign.Spec.State
	if resultSpec.MeasurementState != (batchenergyoptimization.ResultMeasurementState{
		TargetNode: state.TargetNode, ModelSpec: state.Candidate.ModelSpec,
		DeviceClass: state.Candidate.DeviceClass, ExecutionPath: state.Candidate.ExecutionPath, PowerMode: state.Candidate.PowerMode,
	}) {
		return serving.Profile{}, nil, errors.New("terminal result contradicts campaign measurement state")
	}
	planReference := storedObjectIdentity(resultSpec.CurvePlan)
	payload, err = c.readObject(ctx, planReference)
	if err != nil {
		return serving.Profile{}, nil, fmt.Errorf("read frozen curve plan: %w", err)
	}
	plan, err := profilederivation.DecodeCurvePlan(payload)
	if err != nil {
		return serving.Profile{}, nil, err
	}
	condition := plan.Shared()
	if resultSpec.CurvePlan.SchemaVersion != plan.SchemaVersion() || condition.DeviceClass != state.Candidate.DeviceClass ||
		condition.Artifact.ExecutionPath != state.Candidate.ExecutionPath || condition.PowerMode != state.Candidate.PowerMode ||
		condition.MetricSource.NodeName != state.TargetNode || condition.CPU.Request != state.CPURequest.String() {
		return serving.Profile{}, nil, errors.New("frozen curve plan contradicts campaign measurement state")
	}
	if condition.RuntimeFamily != profilingrun.FirstfloorRuntimeFamily {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: "standalone experiment execution is not same-firstfloor serving evidence"}}, nil
	}

	var measuredAt time.Time
	active := make([]profilerevidence.VerifiedEvidence, 0, len(resultSpec.CompletedTrials))
	idle := make([]profilerevidence.VerifiedIdleEvidence, 0, len(resultSpec.CompletedTrials))
	evidenceReferences := []evidencestore.ObjectIdentity{planReference}
	seenRuns := make(map[types.NamespacedName]struct{}, len(resultSpec.CompletedTrials))
	seenRaw := make(map[evidencestore.ObjectLocation]struct{}, len(resultSpec.CompletedTrials))
	seenPods := make(map[string]struct{}, len(resultSpec.CompletedTrials))
	for _, reference := range resultSpec.CompletedTrials {
		runKey := types.NamespacedName{Namespace: reference.Run.Namespace, Name: reference.Run.Name}
		if _, duplicate := seenRuns[runKey]; duplicate {
			return serving.Profile{}, nil, fmt.Errorf("completed Run %s appears more than once", runKey)
		}
		seenRuns[runKey] = struct{}{}
		run := &edgev1alpha1.ProfilingRun{}
		if err := c.store.client.Get(ctx, runKey, run); err != nil {
			return serving.Profile{}, nil, fmt.Errorf("get completed Run %s: %w", runKey, err)
		}
		if err := validateCatalogRun(campaign, run, reference); err != nil {
			return serving.Profile{}, nil, fmt.Errorf("completed Run %s: %w", runKey, err)
		}
		identity := storedObjectIdentity(reference.Evidence)
		var execution profilerevidence.ExecutionEvidence
		if reference.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
			verified, loadErr := c.loader.LoadIdle(ctx, identity)
			if loadErr != nil {
				return serving.Profile{}, nil, fmt.Errorf("load idle Run %s: %w", runKey, loadErr)
			}
			execution, _, err = verified.Records()
			idle = append(idle, verified)
		} else {
			verified, loadErr := c.loader.Load(ctx, identity)
			if loadErr != nil {
				return serving.Profile{}, nil, fmt.Errorf("load active Run %s: %w", runKey, loadErr)
			}
			execution, _, err = verified.Records()
			active = append(active, verified)
		}
		if err != nil {
			return serving.Profile{}, nil, err
		}
		if err := validateCatalogExecution(run, execution); err != nil {
			return serving.Profile{}, nil, fmt.Errorf("completed Run %s: %w", runKey, err)
		}
		if execution.Terminal.Job.CompletionTime.After(measuredAt) {
			measuredAt = execution.Terminal.Job.CompletionTime
		}
		if _, duplicate := seenRaw[execution.RawMeasurement.Location()]; duplicate {
			return serving.Profile{}, nil, errors.New("completed trials reuse one raw measurement object")
		}
		seenRaw[execution.RawMeasurement.Location()] = struct{}{}
		if _, duplicate := seenPods[execution.Terminal.Pod.UID]; duplicate {
			return serving.Profile{}, nil, errors.New("completed trials reuse one execution Pod")
		}
		seenPods[execution.Terminal.Pod.UID] = struct{}{}
		evidenceReferences = append(evidenceReferences, identity, execution.RawMeasurement)
	}
	if len(active) == 0 || len(idle) == 0 {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: "completed active and model-loaded idle raw trials are required"}}, nil
	}
	// The existing projector binds every trial to the frozen plan. It does not
	// apply a new environment quality or scientific admission policy.
	idleTrials := make([]profilederivation.IdleTrial, len(idle))
	for index, verified := range idle {
		if _, err := profilerprojection.ProjectVerifiedCurveState(plan, active[0], verified); err != nil {
			return serving.Profile{}, nil, err
		}
		idleTrials[index], err = profilerprojection.IdleTrial(verified, idle[0])
		if err != nil {
			return serving.Profile{}, nil, err
		}
	}
	baseline, err := profilederivation.DeriveIdleBaseline(idleTrials, profilederivation.PointPolicy{
		Version: "energy-objective-point-v1", MinimumTrials: int(campaign.Spec.TrialsPerBatch), ConfidenceLevel: c.confidenceLevel,
	})
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
		_, raw, err := verified.Records()
		if err != nil {
			return serving.Profile{}, nil, err
		}
		if !reflect.DeepEqual(metricSource, raw.Metrics.Source) {
			return serving.Profile{}, nil, errors.New("active and idle wall-power source identities differ")
		}
		if raw.Runtime.Firstfloor != nil {
			if runtimeMetadata == nil {
				runtimeMetadata, delivery = raw.Runtime.Firstfloor, raw.RunConfig.Delivery
			} else if !reflect.DeepEqual(runtimeMetadata.Runtime, raw.Runtime.Firstfloor.Runtime) {
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
			activeFacts, idleFacts := runtimeMetadata.Runtime, raw.Runtime.Firstfloor.Runtime
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
		ConfidenceLevel: c.confidenceLevel, IdleBaselineWatts: baseline.ModelLoadedIdleMeanWallWatts, MeasuredAt: measuredAt,
		MetricSource: metricSource,
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
		batch.profile.EnergyPerInputJoules = batch.energySum / float64(batch.trials)
		profile.Batches = append(profile.Batches, batch.profile)
	}
	if len(issues) != 0 {
		return serving.Profile{}, issues, nil
	}
	if !completeNativeProfile(profile) {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: "firstfloor requires exact measured batch 1 and initialized measured sizes within selected maxBatchSize"}}, nil
	}
	c.current[source] = catalogProfile{
		uid: campaign.UID, version: campaign.ResourceVersion, profile: cloneCatalogProfile(profile),
	}
	return profile, issues, nil
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
	profile.RuntimeDelivery = slices.Clone(profile.RuntimeDelivery)
	profile.MetricSource.MetricLabels = maps.Clone(profile.MetricSource.MetricLabels)
	if profile.MetricSource.ThermalMetrics != nil {
		thermal := *profile.MetricSource.ThermalMetrics
		profile.MetricSource.ThermalMetrics = &thermal
	}
	return profile
}

type catalogBatch struct {
	profile       serving.BatchProfile
	trials        int
	energySum     float64
	missingMemory bool
}

func (b *catalogBatch) add(trial profilederivation.Trial, energy profilederivation.EnergyTrial, memoryBytes uint64) error {
	if len(trial.NativeSamples) == 0 || len(trial.NativeSamples) != len(trial.LatencyMilliseconds) ||
		math.IsNaN(energy.IncrementalWorkJoulesPerItem) || math.IsInf(energy.IncrementalWorkJoulesPerItem, 0) {
		return errors.New("correlated runner service or measured energy evidence is missing or invalid")
	}
	for _, sample := range trial.NativeSamples {
		nanos := sample.RunnerServiceMilliseconds * 1e6
		if sample.BatchSize != trial.Batch || sample.BatchID == 0 || nanos <= 0 || math.IsNaN(nanos) || math.IsInf(nanos, 0) {
			return errors.New("raw runner service cannot be represented as finite positive nanoseconds")
		}
		b.profile.DurationNanos = append(b.profile.DurationNanos, nanos)
	}
	b.trials++
	b.energySum += energy.IncrementalWorkJoulesPerItem
	if math.IsNaN(b.energySum) || math.IsInf(b.energySum, 0) {
		return errors.New("measured energy aggregate is not finite")
	}
	b.missingMemory = b.missingMemory || memoryBytes == 0
	if memoryBytes > b.profile.MemoryBytes {
		b.profile.MemoryBytes = memoryBytes
	}
	return nil
}

func validateCatalogRun(campaign *edgev1alpha1.BatchEnergyOptimization, run *edgev1alpha1.ProfilingRun, reference batchenergyoptimization.CompletedTrialReference) error {
	if run.Namespace != campaign.Namespace || string(run.UID) != reference.Run.UID || !metav1.IsControlledBy(run, campaign) || run.DeletionTimestamp != nil {
		return errors.New("Run identity or campaign ownership mismatch")
	}
	if run.Spec.MeasurementStateSpec.Candidate != campaign.Spec.State.Candidate || run.Spec.TargetNode != campaign.Spec.State.TargetNode ||
		run.Spec.CPURequest.Cmp(campaign.Spec.State.CPURequest) != 0 || int(run.Spec.Measurement.Batch) != reference.Measurement.Batch {
		return errors.New("Run measurement state or batch contradicts completed trial reference")
	}
	mode := batchenergyoptimization.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == "idle-model-loaded" {
		mode = batchenergyoptimization.WorkloadModeIdle
	}
	if mode != reference.Measurement.WorkloadMode {
		return errors.New("Run workload mode contradicts completed trial reference")
	}
	if err := validateCatalogRunProtocol(campaign, run); err != nil {
		return err
	}
	for _, conditionType := range []string{edgev1alpha1.ProfilingRunConditionExecutionComplete, edgev1alpha1.ProfilingRunConditionEvidenceVerified} {
		condition := meta.FindStatusCondition(run.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != run.Generation {
			return errors.New("Run has no current completed execution and verified evidence")
		}
	}
	if run.Status.Evidence == nil || apiObjectIdentity(run.Status.Evidence) != storedObjectIdentity(reference.Evidence) || run.Status.Evidence.SchemaVersion != reference.Evidence.SchemaVersion {
		return errors.New("completed evidence reference contradicts recorded Run")
	}
	return nil
}

func validateCatalogRunProtocol(campaign *edgev1alpha1.BatchEnergyOptimization, run *edgev1alpha1.ProfilingRun) error {
	measurement := run.Spec.Measurement
	selectedQuota := campaign.Spec.CPUQuota
	if (measurement.CPUQuota == nil) != (selectedQuota == nil) ||
		(measurement.CPUQuota != nil && measurement.CPUQuota.Cmp(*selectedQuota) != 0) {
		return errors.New("campaign trial CPU quota contradicts selected source quota")
	}
	if !slices.Equal(measurement.CPUAffinity, campaign.Spec.CPUAffinity) {
		return errors.New("campaign trial CPU affinity contradicts selected source CPU IDs")
	}
	if measurement.WorkloadMode == "idle-model-loaded" {
		protocol := campaign.Spec.IdleProtocol
		if measurement.WarmupIterations != 0 || measurement.SettlingDurationSeconds == nil ||
			*measurement.SettlingDurationSeconds != protocol.SettlingDurationSeconds ||
			measurement.DurationSeconds != protocol.DurationSeconds || measurement.Dataset != "" ||
			run.Spec.MetricObservation != protocol.MetricObservation {
			return errors.New("idle Run protocol contradicts campaign")
		}
	} else {
		protocol := campaign.Spec.ActiveProtocol
		if measurement.WorkloadMode != "saturated-serial-fixed-batch" || measurement.SettlingDurationSeconds != nil ||
			measurement.WarmupIterations != protocol.WarmupIterations || measurement.DurationSeconds != protocol.DurationSeconds ||
			measurement.Dataset != protocol.Dataset ||
			run.Spec.MetricObservation != protocol.MetricObservation {
			return errors.New("active Run protocol contradicts campaign")
		}
	}
	return nil
}

func validateCatalogExecution(run *edgev1alpha1.ProfilingRun, execution profilerevidence.ExecutionEvidence) error {
	if run.Status.Evidence == nil || run.Status.Job == nil || run.Status.ObservedExecution == nil || run.Status.Evidence.SchemaVersion != execution.SchemaVersion ||
		execution.Terminal.Job.Namespace != run.Namespace || execution.Terminal.Job.Name != run.Status.Job.Name || execution.Terminal.Job.UID != run.Status.Job.UID ||
		execution.Terminal.Pod.Namespace != run.Namespace || execution.Terminal.Pod.Name != run.Status.ObservedExecution.Pod.Name || execution.Terminal.Pod.UID != run.Status.ObservedExecution.Pod.UID ||
		execution.Terminal.Node.Name != run.Status.ObservedExecution.Node.Name || execution.Terminal.Node.UID != run.Status.ObservedExecution.Node.UID {
		return errors.New("stored evidence contradicts recorded Run execution identities")
	}
	if err := validateCatalogRunConfig(run, execution.RunConfig); err != nil {
		return err
	}
	if execution.RunConfig.Actuation == nil {
		if run.Status.Actuation != nil || execution.Actuation != nil {
			return errors.New("fixed execution has unexpected actuation evidence")
		}
	} else if run.Status.Actuation == nil || execution.Actuation == nil ||
		execution.Actuation.Run.Namespace != run.Namespace || execution.Actuation.Run.Name != run.Name || execution.Actuation.Run.UID != string(run.UID) ||
		execution.Actuation.Handoff.Namespace != run.Namespace || execution.Actuation.Handoff.Name != run.Status.Actuation.Name || execution.Actuation.Handoff.UID != run.Status.Actuation.UID {
		return errors.New("stored actuation evidence contradicts completed Run")
	}
	return nil
}

func validateCatalogRunConfig(run *edgev1alpha1.ProfilingRun, config profilingrun.RunConfig) error {
	if config.Candidate.Inputs.ModelSpec.Name != run.Spec.Candidate.ModelSpec || config.State.DeviceClass != run.Spec.Candidate.DeviceClass ||
		config.Candidate.ExecutionPath != run.Spec.Candidate.ExecutionPath || config.State.PowerMode != run.Spec.Candidate.PowerMode ||
		config.TargetNode.Name != run.Spec.TargetNode || config.Measurement.Batch != int(run.Spec.Measurement.Batch) ||
		config.Measurement.WorkloadMode != run.Spec.Measurement.WorkloadMode || config.CPU.Request != run.Spec.CPURequest.String() {
		return errors.New("stored RunConfig contradicts completed Run spec")
	}
	measurement := run.Spec.Measurement
	if measurement.CPUQuota == nil {
		if config.CPU.Policy != profilingrun.AllocatableCPUQuotaPolicy {
			return errors.New("frozen CPU quota policy contradicts completed Run selection")
		}
	} else {
		quota, err := resource.ParseQuantity(config.CPU.Limit)
		if err != nil || config.CPU.Policy != profilingrun.ConfiguredCPUQuotaPolicy || quota.Cmp(*measurement.CPUQuota) != 0 {
			return errors.New("frozen CPU quota contradicts completed Run selection")
		}
	}
	if len(config.CPU.AffinityCPUs) != len(measurement.CPUAffinity) {
		return errors.New("frozen CPU affinity contradicts completed Run selection")
	}
	for index, cpu := range measurement.CPUAffinity {
		if !slices.Contains(config.CPU.AffinityCPUs, int(cpu)) || slices.Contains(measurement.CPUAffinity[:index], cpu) {
			return errors.New("frozen CPU affinity contradicts completed Run selection")
		}
	}
	settling := 0
	if measurement.SettlingDurationSeconds != nil {
		settling = int(*measurement.SettlingDurationSeconds)
	}
	if config.Measurement.WarmupIterations != int(measurement.WarmupIterations) ||
		config.Measurement.DurationSeconds != int(measurement.DurationSeconds) || config.Measurement.SettlingDurationSeconds != settling ||
		(config.Measurement.Dataset == nil) != (measurement.Dataset == "") || (config.Measurement.Dataset != nil && config.Measurement.Dataset.Name != measurement.Dataset) ||
		config.Metrics.IntervalMilliseconds != int(run.Spec.MetricObservation.IntervalMilliseconds) ||
		config.Metrics.TimeoutMilliseconds != int(run.Spec.MetricObservation.TimeoutMilliseconds) ||
		config.Metrics.DurationSeconds != int(run.Spec.MetricObservation.DurationSeconds) {
		return errors.New("stored RunConfig contradicts completed Run protocol")
	}
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

func apiObjectIdentity(reference *edgev1alpha1.ProfilingRunEvidenceReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
}

func storedObjectIdentity(reference batchenergyoptimization.StoredObjectReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
}
