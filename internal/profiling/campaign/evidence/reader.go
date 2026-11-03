package evidence

import (
	"context"
	"errors"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"golang.org/x/sync/errgroup"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ponytail: eight trials per window cap store connections and transient decode
// buffers; tune only with store load/memory measurements, not campaign size.
const trialConcurrency = 8

var (
	// ErrRunEvidenceAbsent identifies a Run without a retained evidence reference.
	ErrRunEvidenceAbsent = errors.New("completed evidence reference contradicts recorded Run")
	// ErrRunEvidenceMismatch identifies a reference that differs from the result.
	ErrRunEvidenceMismatch = errors.New("completed evidence reference contradicts recorded Run")
	// ErrUnsupportedWorkloadMode identifies a mode outside active and loaded idle.
	ErrUnsupportedWorkloadMode = errors.New("active Run protocol contradicts campaign")
	// ErrActiveProtocolMismatch identifies an active Run whose protocol differs.
	ErrActiveProtocolMismatch = errors.New("active Run protocol contradicts campaign")
)

// Reader reconstructs retained campaign inputs without selecting measurements,
// admitting serving profiles, or applying a reporting/derivation policy.
// Load callbacks retain the caller's Evidence Store binding and read limits.
type Reader struct {
	Runs       client.Reader
	ReadObject func(context.Context, evidencestore.ObjectIdentity) ([]byte, error)
	LoadActive func(context.Context, *edgev1alpha1.ProfilingRun, evidencestore.ObjectIdentity) (profilerevidence.VerifiedEvidence, error)
	LoadIdle   func(context.Context, *edgev1alpha1.ProfilingRun, evidencestore.ObjectIdentity) (profilerevidence.VerifiedIdleEvidence, error)
}

// Campaign binds the decoded terminal result and frozen plan to their identities.
type Campaign struct {
	Result         batchenergyoptimization.ResultRecord
	Plan           profilederivation.CurvePlan
	ResultIdentity evidencestore.ObjectIdentity
	PlanIdentity   evidencestore.ObjectIdentity
}

// Trials contains verified inputs and lineage in completed-reference order.
// Empty Active or Idle is not an error: consumers own completeness policy.
type Trials struct {
	Active        []profilerevidence.VerifiedEvidence
	Idle          []profilerevidence.VerifiedIdleEvidence
	MeasuredAt    time.Time
	Evidence      []evidencestore.ObjectIdentity
	ActiveSources map[int][]evidencestore.ObjectIdentity
	IdleSources   []evidencestore.ObjectIdentity
}

// Read loads the terminal result before reading and binding its frozen plan.
// Campaign lifecycle/selection gates remain with the caller.
func (r *Reader) Read(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization) (Campaign, error) {
	if campaign.Status.Result == nil {
		return Campaign{}, errors.New("completed campaign has no terminal result reference")
	}
	resultReference := apiObjectIdentity(campaign.Status.Result)
	payload, err := r.ReadObject(ctx, resultReference)
	if err != nil {
		return Campaign{}, fmt.Errorf("read terminal result: %w", err)
	}
	result, err := batchenergyoptimization.DecodeResultRecord(payload)
	if err != nil {
		return Campaign{}, err
	}
	return r.ReadPlan(ctx, campaign, result)
}

// ReadPlan checks the decoded result's schema/outcome against campaign status,
// then loads and binds its frozen plan without fetching the result again.
func (r *Reader) ReadPlan(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization, result batchenergyoptimization.ResultRecord) (Campaign, error) {
	if reference := campaign.Status.Result; reference != nil &&
		(reference.SchemaVersion != result.SchemaVersion() || string(result.Spec().Outcome) != campaign.Status.Outcome) {
		return Campaign{}, errors.New("terminal result schema or outcome contradicts campaign status")
	}
	return r.LoadPlan(ctx, campaign, result)
}

// LoadPlan binds the decoded result and frozen plan to campaign measurement
// state, without checking the result's schema/outcome against campaign status.
// Reporting uses this entry independently of serving's status acceptance policy.
func (r *Reader) LoadPlan(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization, result batchenergyoptimization.ResultRecord) (Campaign, error) {
	if campaign.Status.Result == nil {
		return Campaign{}, errors.New("completed campaign has no terminal result reference")
	}
	resultSpec := result.Spec()
	state := campaign.Spec.State
	if resultSpec.MeasurementState != (batchenergyoptimization.ResultMeasurementState{
		TargetNode: state.TargetNode, ModelSpec: state.Candidate.ModelSpec,
		DeviceClass: state.Candidate.DeviceClass, ExecutionPath: state.Candidate.ExecutionPath, PowerMode: state.Candidate.PowerMode,
	}) {
		return Campaign{}, errors.New("terminal result contradicts campaign measurement state")
	}
	planReference := storedObjectIdentity(resultSpec.CurvePlan)
	payload, err := r.ReadObject(ctx, planReference)
	if err != nil {
		return Campaign{}, fmt.Errorf("read frozen curve plan: %w", err)
	}
	plan, err := profilederivation.DecodeCurvePlan(payload)
	if err != nil {
		return Campaign{}, err
	}
	condition := plan.Shared()
	if resultSpec.CurvePlan.SchemaVersion != plan.SchemaVersion() || condition.DeviceClass != state.Candidate.DeviceClass ||
		condition.Artifact.ExecutionPath != state.Candidate.ExecutionPath || condition.PowerMode != state.Candidate.PowerMode ||
		condition.MetricSource.NodeName != state.TargetNode {
		return Campaign{}, errors.New("frozen curve plan contradicts campaign measurement state")
	}
	return Campaign{Result: result, Plan: plan, ResultIdentity: apiObjectIdentity(campaign.Status.Result), PlanIdentity: planReference}, nil
}

// ReadTrials loads completed trials in bounded windows. Worker completion order
// never chooses the first error, duplicate diagnostic, or lineage ordering.
func (r *Reader) ReadTrials(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization, retained Campaign) (Trials, error) {
	resultSpec := retained.Result.Spec()
	planReference := retained.PlanIdentity
	var measuredAt time.Time
	active := make([]profilerevidence.VerifiedEvidence, 0, len(resultSpec.CompletedTrials))
	idle := make([]profilerevidence.VerifiedIdleEvidence, 0, len(resultSpec.CompletedTrials))
	evidenceReferences := []evidencestore.ObjectIdentity{planReference}
	activeSources := map[int][]evidencestore.ObjectIdentity{}
	idleSources := []evidencestore.ObjectIdentity{planReference}
	seenRuns := make(map[types.NamespacedName]struct{}, len(resultSpec.CompletedTrials))
	seenRaw := make(map[evidencestore.ObjectLocation]struct{}, len(resultSpec.CompletedTrials))
	seenPods := make(map[string]struct{}, len(resultSpec.CompletedTrials))
	for start := 0; start < len(resultSpec.CompletedTrials); start += trialConcurrency {
		references := resultSpec.CompletedTrials[start:min(start+trialConcurrency, len(resultSpec.CompletedTrials))]
		trials := r.readCompletedTrials(ctx, campaign, references)
		for index, reference := range references {
			runKey := types.NamespacedName{Namespace: reference.Run.Namespace, Name: reference.Run.Name}
			if _, duplicate := seenRuns[runKey]; duplicate {
				return Trials{}, fmt.Errorf("completed Run %s appears more than once", runKey)
			}
			seenRuns[runKey] = struct{}{}
			trial := &trials[index]
			if trial.err != nil {
				return Trials{}, trial.err
			}
			identity := storedObjectIdentity(reference.Evidence)
			execution := trial.execution
			if reference.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
				idle = append(idle, trial.idle)
			} else {
				active = append(active, trial.active)
			}
			if execution.Terminal.Job.CompletionTime.After(measuredAt) {
				measuredAt = execution.Terminal.Job.CompletionTime
			}
			if _, duplicate := seenRaw[execution.RawMeasurement.Location()]; duplicate {
				return Trials{}, errors.New("completed trials reuse one raw measurement object")
			}
			seenRaw[execution.RawMeasurement.Location()] = struct{}{}
			if _, duplicate := seenPods[execution.Terminal.Pod.UID]; duplicate {
				return Trials{}, errors.New("completed trials reuse one execution Pod")
			}
			seenPods[execution.Terminal.Pod.UID] = struct{}{}
			evidenceReferences = append(evidenceReferences, identity, execution.RawMeasurement)
			if reference.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
				idleSources = append(idleSources, identity, execution.RawMeasurement)
			} else {
				batch := reference.Measurement.Batch
				activeSources[batch] = append(activeSources[batch], identity, execution.RawMeasurement)
			}
		}
	}
	return Trials{Active: active, Idle: idle, MeasuredAt: measuredAt, Evidence: evidenceReferences, ActiveSources: activeSources, IdleSources: idleSources}, nil
}

type completedTrial struct {
	active    profilerevidence.VerifiedEvidence
	idle      profilerevidence.VerifiedIdleEvidence
	execution profilerevidence.ExecutionEvidence
	err       error
}

// Each worker owns one slot; ordered reduction above preserves diagnostics,
// duplicate detection and profile lineage regardless of completion order.
func (r *Reader) readCompletedTrials(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization,
	references []batchenergyoptimization.CompletedTrialReference,
) []completedTrial {
	trials := make([]completedTrial, len(references))
	var group errgroup.Group
	group.SetLimit(trialConcurrency)
	for index, reference := range references {
		group.Go(func() error {
			trials[index] = r.readCompletedTrial(ctx, campaign, reference)
			return nil // Keep errors in input order, not goroutine completion order.
		})
	}
	_ = group.Wait()
	return trials
}

func (r *Reader) readCompletedTrial(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization,
	reference batchenergyoptimization.CompletedTrialReference,
) (trial completedTrial) {
	runKey := types.NamespacedName{Namespace: reference.Run.Namespace, Name: reference.Run.Name}
	run := &edgev1alpha1.ProfilingRun{}
	if err := r.Runs.Get(ctx, runKey, run); err != nil {
		trial.err = fmt.Errorf("get completed Run %s: %w", runKey, err)
		return trial
	}
	if err := validateRun(campaign, run, reference); err != nil {
		trial.err = fmt.Errorf("completed Run %s: %w", runKey, err)
		return trial
	}
	identity := storedObjectIdentity(reference.Evidence)
	if reference.Measurement.WorkloadMode == batchenergyoptimization.WorkloadModeIdle {
		trial.idle, trial.err = r.LoadIdle(ctx, run, identity)
		if trial.err == nil {
			trial.execution, _, trial.err = trial.idle.Records()
		}
	} else {
		trial.active, trial.err = r.LoadActive(ctx, run, identity)
		if trial.err == nil {
			trial.execution, _, trial.err = trial.active.Records()
		}
	}
	if trial.err != nil {
		trial.err = fmt.Errorf("load %s Run %s: %w", reference.Measurement.WorkloadMode, runKey, trial.err)
		return trial
	}
	if err := validateExecution(run, trial.execution); err != nil {
		trial.err = fmt.Errorf("completed Run %s: %w", runKey, err)
	}
	return trial
}

func validateRun(campaign *edgev1alpha1.BatchEnergyOptimization, run *edgev1alpha1.ProfilingRun, reference batchenergyoptimization.CompletedTrialReference) error {
	if run.Namespace != campaign.Namespace || string(run.UID) != reference.Run.UID || !metav1.IsControlledBy(run, campaign) || run.DeletionTimestamp != nil {
		return errors.New("Run identity or campaign ownership mismatch")
	}
	if run.Spec.MeasurementStateSpec.Candidate != campaign.Spec.State.Candidate || run.Spec.TargetNode != campaign.Spec.State.TargetNode ||
		int(run.Spec.Measurement.Batch) != reference.Measurement.Batch {
		return errors.New("Run measurement state or batch contradicts completed trial reference")
	}
	mode := batchenergyoptimization.WorkloadModeActive
	if run.Spec.Measurement.WorkloadMode == "idle-model-loaded" {
		mode = batchenergyoptimization.WorkloadModeIdle
	}
	if mode != reference.Measurement.WorkloadMode {
		return errors.New("Run workload mode contradicts completed trial reference")
	}
	for _, conditionType := range []string{edgev1alpha1.ProfilingRunConditionExecutionComplete, edgev1alpha1.ProfilingRunConditionEvidenceVerified} {
		condition := meta.FindStatusCondition(run.Status.Conditions, conditionType)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != run.Generation {
			return errors.New("Run has no current completed execution and verified evidence")
		}
	}
	if run.Status.Evidence == nil {
		return ErrRunEvidenceAbsent
	}
	if apiObjectIdentity(run.Status.Evidence) != storedObjectIdentity(reference.Evidence) || run.Status.Evidence.SchemaVersion != reference.Evidence.SchemaVersion {
		return ErrRunEvidenceMismatch
	}
	return validateRunProtocol(campaign, run)
}

func validateRunProtocol(campaign *edgev1alpha1.BatchEnergyOptimization, run *edgev1alpha1.ProfilingRun) error {
	measurement := run.Spec.Measurement
	if measurement.WorkloadMode == "idle-model-loaded" {
		protocol := campaign.Spec.IdleProtocol
		if measurement.WarmupIterations != 0 || measurement.SettlingDurationSeconds == nil ||
			*measurement.SettlingDurationSeconds != protocol.SettlingDurationSeconds ||
			measurement.DurationSeconds != protocol.DurationSeconds || measurement.Dataset != "" ||
			ptr.Deref(measurement.DurationToleranceSeconds, .5) != ptr.Deref(protocol.DurationToleranceSeconds, .5) ||
			run.Spec.MetricObservation != protocol.MetricObservation {
			return errors.New("idle Run protocol contradicts campaign")
		}
	} else {
		protocol := campaign.Spec.ActiveProtocol
		if measurement.WorkloadMode != "saturated-serial-fixed-batch" {
			return ErrUnsupportedWorkloadMode
		}
		if measurement.SettlingDurationSeconds != nil ||
			measurement.WarmupIterations != protocol.WarmupIterations || measurement.DurationSeconds != protocol.DurationSeconds ||
			ptr.Deref(measurement.DurationToleranceSeconds, .5) != ptr.Deref(protocol.DurationToleranceSeconds, .5) ||
			measurement.Dataset != protocol.Dataset ||
			run.Spec.MetricObservation != protocol.MetricObservation {
			return ErrActiveProtocolMismatch
		}
	}
	return nil
}

func validateExecution(run *edgev1alpha1.ProfilingRun, execution profilerevidence.ExecutionEvidence) error {
	if run.Status.Evidence == nil || run.Status.Job == nil || run.Status.ObservedExecution == nil || run.Status.Evidence.SchemaVersion != execution.SchemaVersion ||
		execution.Terminal.Job.Namespace != run.Namespace || execution.Terminal.Job.Name != run.Status.Job.Name || execution.Terminal.Job.UID != run.Status.Job.UID ||
		execution.Terminal.Pod.Namespace != run.Namespace || execution.Terminal.Pod.Name != run.Status.ObservedExecution.Pod.Name || execution.Terminal.Pod.UID != run.Status.ObservedExecution.Pod.UID ||
		execution.Terminal.Node.Name != run.Status.ObservedExecution.Node.Name || execution.Terminal.Node.UID != run.Status.ObservedExecution.Node.UID {
		return errors.New("stored evidence contradicts recorded Run execution identities")
	}
	if err := validateRunConfig(run, execution.RunConfig); err != nil {
		return err
	}
	if execution.RunConfig.Actuation == nil {
		if execution.Actuation != nil {
			return errors.New("fixed execution has unexpected actuation evidence")
		}
	} else if execution.Actuation == nil ||
		execution.Actuation.Run.Namespace != run.Namespace || execution.Actuation.Run.Name != run.Name || execution.Actuation.Run.UID != string(run.UID) {
		return errors.New("stored actuation evidence contradicts completed Run")
	}
	return nil
}

func validateRunConfig(run *edgev1alpha1.ProfilingRun, config profilingrun.RunConfig) error {
	if config.Candidate.Inputs.ModelSpec.Name != run.Spec.Candidate.ModelSpec || config.State.DeviceClass != run.Spec.Candidate.DeviceClass ||
		config.Candidate.ExecutionPath != run.Spec.Candidate.ExecutionPath || config.State.PowerMode != run.Spec.Candidate.PowerMode ||
		config.TargetNode.Name != run.Spec.TargetNode || config.Measurement.Batch != int(run.Spec.Measurement.Batch) ||
		config.Measurement.WorkloadMode != run.Spec.Measurement.WorkloadMode {
		return errors.New("stored RunConfig contradicts completed Run spec")
	}
	measurement := run.Spec.Measurement
	settling := 0
	if measurement.SettlingDurationSeconds != nil {
		settling = int(*measurement.SettlingDurationSeconds)
	}
	if config.Measurement.WarmupIterations != int(measurement.WarmupIterations) ||
		config.Measurement.DurationSeconds != int(measurement.DurationSeconds) || config.Measurement.SettlingDurationSeconds != settling ||
		// Stored RunConfig objects written before the field existed carry 0; the CRD now defaults the spec to .5 on read (ADR 0019).
		config.Measurement.DurationToleranceSeconds != 0 && config.Measurement.DurationToleranceSeconds != ptr.Deref(measurement.DurationToleranceSeconds, .5) ||
		(config.Measurement.Dataset == nil) != (measurement.Dataset == "") || (config.Measurement.Dataset != nil && config.Measurement.Dataset.Name != measurement.Dataset) ||
		config.Metrics.IntervalMilliseconds != int(run.Spec.MetricObservation.IntervalMilliseconds) ||
		config.Metrics.TimeoutMilliseconds != int(run.Spec.MetricObservation.TimeoutMilliseconds) ||
		config.Metrics.DurationSeconds != int(run.Spec.MetricObservation.DurationSeconds) {
		return errors.New("stored RunConfig contradicts completed Run protocol")
	}
	return nil
}

func apiObjectIdentity(reference *edgev1alpha1.ProfilingRunEvidenceReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
}

func storedObjectIdentity(reference batchenergyoptimization.StoredObjectReference) evidencestore.ObjectIdentity {
	return evidencestore.ObjectIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}
}
