package kube

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

// Catalog is a refresh-only storage boundary. Its returned view is retained by
// the coordinator; inference never reads campaigns or downloads evidence.
// Loader verification proves stored structure and binding, not admission.
// Batch distributions retain every raw latency sample in nanoseconds. Energy
// uses the existing incremental wall-energy/item estimator and arithmetic trial
// mean; memory is the largest measured byte count, never a resource limit.
type Catalog struct {
	store           *Store
	objects         evidencestore.LocationReader
	loader          *profilerevidence.Loader
	maxObjectBytes  int64
	confidenceLevel float64
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
	return &Catalog{store: store, objects: objects, loader: loader, maxObjectBytes: maxObjectBytes, confidenceLevel: confidenceLevel}, nil
}

// Refresh follows only explicitly selected terminal campaigns. A bad source
// cannot contribute a partial profile; independent selections remain usable.
// Cancellation returns no view, so callers cannot publish a partial refresh.
func (c *Catalog) Refresh(ctx context.Context, selections []types.NamespacedName) (CatalogView, error) {
	view := CatalogView{Profiles: []serving.Profile{}, Insufficiencies: []CatalogIssue{}}
	seen := make(map[types.NamespacedName]struct{}, len(selections))
	for _, source := range selections {
		if err := ctx.Err(); err != nil {
			return CatalogView{}, err
		}
		if _, duplicate := seen[source]; duplicate {
			continue
		}
		seen[source] = struct{}{}
		profile, issues, err := c.readProfile(ctx, source)
		if err != nil {
			if ctx.Err() != nil {
				return CatalogView{}, ctx.Err()
			}
			view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: "InvalidEvidence", Reason: err.Error()})
			continue
		}
		view.Insufficiencies = append(view.Insufficiencies, issues...)
		if len(profile.Batches) > 0 {
			if !completeNativeProfile(profile) {
				view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: "MeasurementEvidenceMissing", Reason: "Triton/Python serving requires measured and planned batch sizes 1..maxBatchSize under the frozen native settings"})
				continue
			}
			view.Profiles = append(view.Profiles, profile)
		}
	}
	return view, nil
}

func completeNativeProfile(profile serving.Profile) bool {
	condition := profile.Condition
	if condition.RuntimeFamily != profilingrun.TritonRuntimeFamily || condition.Backend != profilingrun.TritonPythonBackend || condition.Triton == nil ||
		len(profile.Batches) != int(condition.Triton.MaxBatchSize) || len(condition.PlannedBatchSizes) != int(condition.Triton.MaxBatchSize) {
		return false
	}
	for index, batch := range profile.Batches {
		if batch.Batch != index+1 || condition.PlannedBatchSizes[index] != batch.Batch {
			return false
		}
	}
	return len(profile.Batches) > 0
}

//nolint:gocyclo // This refresh boundary follows the explicit ordered campaign, plan, Run, and raw-evidence checks.
func (c *Catalog) readProfile(ctx context.Context, source types.NamespacedName) (serving.Profile, []CatalogIssue, error) {
	if len(validation.IsDNS1123Label(source.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(source.Name)) != 0 {
		return serving.Profile{}, nil, errors.New("campaign namespace and name must be valid Kubernetes names")
	}
	campaign := &edgev1alpha1.BatchEnergyOptimization{}
	if err := c.store.client.Get(ctx, source, campaign); err != nil {
		return serving.Profile{}, nil, fmt.Errorf("get campaign %s: %w", source, err)
	}
	if campaign.DeletionTimestamp != nil || campaign.Status.Phase != "Complete" || campaign.Status.ObservedGeneration != campaign.Generation {
		return serving.Profile{}, nil, errors.New("selected campaign has no current completed result")
	}
	if campaign.Status.Result == nil {
		return serving.Profile{}, nil, errors.New("completed campaign has no terminal result reference")
	}
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
		idleTrials[index], err = profilerprojection.IdleTrial(verified)
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
	batches := make(map[int]*catalogBatch)
	for _, verified := range active {
		if _, err := profilerprojection.ProjectVerifiedCurveState(plan, verified, idle[0]); err != nil {
			return serving.Profile{}, nil, err
		}
		_, raw, err := verified.Records()
		if err != nil {
			return serving.Profile{}, nil, err
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
	profile := serving.Profile{
		ID: source.String() + "/" + string(campaign.UID), Reference: resultReference,
		NodeName: state.TargetNode, Condition: condition, Evidence: evidenceReferences,
		ConfidenceLevel: c.confidenceLevel,
	}
	var issues []CatalogIssue
	for _, batchSize := range condition.PlannedBatchSizes {
		batch := batches[batchSize]
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
	return profile, issues, nil
}

type catalogBatch struct {
	profile       serving.BatchProfile
	trials        int
	energySum     float64
	missingMemory bool
}

func (b *catalogBatch) add(trial profilederivation.Trial, energy profilederivation.EnergyTrial, memoryBytes uint64) error {
	if len(trial.LatencyMilliseconds) == 0 || math.IsNaN(energy.IncrementalWorkJoulesPerItem) || math.IsInf(energy.IncrementalWorkJoulesPerItem, 0) {
		return errors.New("raw latency or measured energy evidence is missing or invalid")
	}
	for _, milliseconds := range trial.LatencyMilliseconds {
		nanos := milliseconds * 1e6
		if nanos <= 0 || math.IsNaN(nanos) || math.IsInf(nanos, 0) {
			return errors.New("raw latency cannot be represented as finite positive nanoseconds")
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
	if measurement.CPUQuota != nil {
		return errors.New("campaign trial has an unexpected configured CPU quota")
	}
	if measurement.WorkloadMode == "idle-model-loaded" {
		protocol := campaign.Spec.IdleProtocol
		if measurement.WarmupIterations != 0 || measurement.SettlingDurationSeconds == nil ||
			*measurement.SettlingDurationSeconds != protocol.SettlingDurationSeconds ||
			measurement.DurationSeconds != protocol.DurationSeconds || measurement.InputGenerator != "none" ||
			measurement.OutputValidation != "none" || run.Spec.MetricObservation != protocol.MetricObservation {
			return errors.New("idle Run protocol contradicts campaign")
		}
	} else {
		protocol := campaign.Spec.ActiveProtocol
		if measurement.WorkloadMode != "saturated-serial-fixed-batch" || measurement.SettlingDurationSeconds != nil ||
			measurement.WarmupIterations != protocol.WarmupIterations || measurement.DurationSeconds != protocol.DurationSeconds ||
			measurement.InputGenerator != protocol.InputGenerator || measurement.OutputValidation != protocol.OutputValidation ||
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
	settling := 0
	if measurement.SettlingDurationSeconds != nil {
		settling = int(*measurement.SettlingDurationSeconds)
	}
	if config.Measurement.WarmupIterations != int(measurement.WarmupIterations) ||
		config.Measurement.DurationSeconds != int(measurement.DurationSeconds) || config.Measurement.SettlingDurationSeconds != settling ||
		config.Measurement.InputGenerator != measurement.InputGenerator || config.Measurement.OutputValidation != measurement.OutputValidation ||
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
