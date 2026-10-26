package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const measurementRequiredStateNotConfirmed = "RequiredStateNotConfirmed"
const measurementPhaseFailed = "Failed"

func measurementTerminal(task *api.BatchEnergyOptimization) bool {
	if task.Generation < 1 || task.Status.ObservedGeneration != task.Generation {
		return false
	}
	condition := apimeta.FindStatusCondition(task.Status.Conditions, "MeasurementComplete")
	if condition == nil || condition.ObservedGeneration != task.Generation {
		return false
	}
	if task.Status.Phase == "Complete" {
		return task.Status.Result != nil && condition.Status == meta.ConditionTrue && condition.Reason == "ResultWritten"
	}
	return task.Status.Phase == measurementPhaseFailed && condition.Status == meta.ConditionFalse &&
		condition.Reason == task.Status.Outcome &&
		(condition.Reason == "TransitionFailed" || condition.Reason == "RetryLimitExhausted" ||
			condition.Reason == "EnergyObjectivePolicyMismatch")
}

func measurementInUse(reason string) error {
	return &serving.Error{Code: "MeasurementInUse", Message: reason}
}

// Only production writer locations grant deletion authority. Native objects
// retain every UID needed to reconstruct this set after partial cleanup.
func (o *measurementService) measurementEvidence(
	ctx context.Context, task *api.BatchEnergyOptimization,
) ([]evidencestore.ObjectLocation, error) {
	if o.objects == nil || o.opts.storeName == "" || o.opts.maxEvidenceBytes < 1 {
		return nil, &serving.Error{
			Code:    "MeasurementEvidenceUnavailable",
			Message: "bounded measurement Evidence Store access is required for deletion",
		}
	}
	if !measurementTerminal(task) {
		return nil, measurementInUse("only a current terminal measurement can be deleted; " +
			"cancellation and recoverable failure interruption are unsupported")
	}
	if strings.ContainsAny(string(task.UID), "/\\") {
		return nil, measurementInUse("native measurement UID is not an owned evidence-key component")
	}
	base := "batch-energy-optimizations/" + string(task.UID) + "/"
	resultLocation := o.measurementLocation(base + "result.json")
	if task.Status.Result != nil {
		reference := task.Status.Result
		if reference.Store != resultLocation.Store || reference.Key != resultLocation.Key {
			return nil, measurementInUse("measurement result is not at its source-proven owned location")
		}
	}
	runs := &api.ProfilingRunList{}
	if err := o.client.List(ctx, runs, client.InNamespace(task.Namespace)); err != nil {
		return nil, err
	}
	owned := map[string]*api.ProfilingRun{}
	raw, executions := []evidencestore.ObjectLocation{}, []evidencestore.ObjectLocation{}
	for i := range runs.Items {
		run := &runs.Items[i]
		if !meta.IsControlledBy(run, task) {
			continue
		}
		rawLocation, executionLocation, err := o.ownedRunEvidence(ctx, task, run)
		if err != nil {
			return nil, err
		}
		owned[string(run.UID)] = run
		raw = append(raw, rawLocation)
		executions = append(executions, executionLocation)
	}
	if err := o.checkOwnedResult(ctx, resultLocation, base, owned); err != nil {
		return nil, err
	}
	slices.SortFunc(raw, func(a, b evidencestore.ObjectLocation) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(executions, func(a, b evidencestore.ObjectLocation) int { return strings.Compare(a.Key, b.Key) })
	locations := append(raw, executions...)
	bundle := "serving/profiles/" + task.Namespace + "/" + task.Name + "/" + string(task.UID) + "/bundle.json"
	return append(locations, o.measurementLocation(bundle),
		o.measurementLocation(base+"curve-plan.json"), resultLocation), nil
}

func (o *measurementService) measurementLocation(key string) evidencestore.ObjectLocation {
	return evidencestore.ObjectLocation{Store: o.opts.storeName, Key: key}
}

func (o *measurementService) ownedRunEvidence(
	ctx context.Context, task *api.BatchEnergyOptimization, run *api.ProfilingRun,
) (raw, executionLocation evidencestore.ObjectLocation, err error) {
	owner := meta.GetControllerOf(run)
	if run.UID == "" || strings.ContainsAny(string(run.UID), "/\\") ||
		owner.APIVersion != api.GroupVersion.String() || owner.Kind != "BatchEnergyOptimization" || owner.Name != task.Name {
		return raw, executionLocation, measurementInUse("owned Run has an inconsistent native controller identity")
	}
	if err := o.checkRunStopped(ctx, run); err != nil {
		return raw, executionLocation, err
	}
	raw = o.measurementLocation("profiling-runs/" + string(run.UID) + "/raw.json")
	executionLocation = o.measurementLocation("profiling-runs/" + string(run.UID) + "/execution.json")
	if reference := run.Status.Evidence; reference != nil {
		if reference.Store != executionLocation.Store || reference.Key != executionLocation.Key {
			return raw, executionLocation, measurementInUse("owned Run evidence points outside its source-proven location")
		}
	}
	payload, err := o.readDeletionEvidence(ctx, executionLocation, true)
	if err != nil || payload == nil {
		return raw, executionLocation, err
	}
	var execution profilerevidence.ExecutionEvidence
	if json.Unmarshal(payload, &execution) != nil ||
		!profilerevidence.IsSupportedExecutionEvidenceSchemaVersion(execution.SchemaVersion) ||
		execution.RawMeasurement.Location() != raw {
		return raw, executionLocation,
			measurementInUse("owned execution evidence links an unsupported or foreign raw measurement")
	}
	return raw, executionLocation, nil
}

func (o *measurementService) checkOwnedResult(
	ctx context.Context, location evidencestore.ObjectLocation, base string, owned map[string]*api.ProfilingRun,
) error {
	payload, err := o.readDeletionEvidence(ctx, location, true)
	if err != nil || payload == nil {
		return err
	}
	record, err := campaign.DecodeResultRecord(payload)
	if err != nil {
		return err
	}
	spec := record.Spec()
	if spec.CurvePlan.Store != o.opts.storeName || spec.CurvePlan.Key != base+"curve-plan.json" {
		return measurementInUse("measurement result links a foreign curve plan")
	}
	for _, trial := range spec.CompletedTrials {
		run := owned[trial.Run.UID]
		if run == nil || trial.Run.Namespace != run.Namespace || trial.Run.Name != run.Name ||
			trial.Evidence.Store != o.opts.storeName ||
			trial.Evidence.Key != "profiling-runs/"+trial.Run.UID+"/execution.json" {
			return measurementInUse("measurement result links a missing or foreign Run/evidence")
		}
	}
	for _, trial := range spec.ExcludedTrials {
		run := owned[trial.Run.UID]
		if run == nil || trial.Run.Namespace != run.Namespace || trial.Run.Name != run.Name {
			return measurementInUse("measurement result links a missing or foreign excluded Run")
		}
	}
	return nil
}

func terminalRunFailure(run *api.ProfilingRun) bool {
	condition := apimeta.FindStatusCondition(run.Status.Conditions, api.ProfilingRunConditionExecutionComplete)
	if condition == nil || condition.Status != meta.ConditionFalse || condition.ObservedGeneration != run.Generation {
		return false
	}
	switch condition.Reason {
	case profiler.ResolutionReasonInvalidRunSpec, profiler.ResolutionReasonExecutionPolicyInvalid,
		profiler.ResolutionReasonActuationPolicyInvalid, measurementRequiredStateNotConfirmed, "JobFailed", "ExecutionLost":
		return true
	}
	return false
}

func (o *measurementService) checkRunStopped(ctx context.Context, run *api.ProfilingRun) error {
	if run.Status.Evidence == nil && !terminalRunFailure(run) {
		condition := apimeta.FindStatusCondition(run.Status.Conditions, api.ProfilingRunConditionEvidenceVerified)
		if condition == nil || condition.Status != meta.ConditionFalse || condition.Reason != "EvidenceInvalid" {
			return measurementInUse("an owned Run can still execute or finalize evidence")
		}
	}
	if run.Status.Job == nil {
		return o.checkPreJobRunStopped(ctx, run)
	}
	if run.Status.Job.Name == "" || run.Status.Job.UID == "" {
		return measurementInUse("owned Run lacks a frozen terminal Job identity")
	}
	job := &batch.Job{}
	err := o.client.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Status.Job.Name}, job)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if string(job.UID) != run.Status.Job.UID || !meta.IsControlledBy(job, run) {
			return measurementInUse("owned Run Job identity changed")
		}
		if !measurementJobTerminal(job) {
			return measurementInUse("an owned measurement Job remains active")
		}
	}
	return o.checkRunPodsStopped(ctx, run, run.Status.Job.Name, run.Status.Job.UID)
}

func measurementJobTerminal(job *batch.Job) bool {
	if job.Status.Active != 0 {
		return false
	}
	for _, condition := range job.Status.Conditions {
		if condition.Status == core.ConditionTrue &&
			(condition.Type == batch.JobComplete || condition.Type == batch.JobFailed) {
			return true
		}
	}
	return false
}

func (o *measurementService) checkPreJobRunStopped(ctx context.Context, run *api.ProfilingRun) error {
	condition := apimeta.FindStatusCondition(run.Status.Conditions, api.ProfilingRunConditionExecutionComplete)
	if run.Generation < 1 || condition == nil || condition.Status != meta.ConditionFalse ||
		condition.ObservedGeneration != run.Generation {
		return measurementInUse("owned Run has no current terminal pre-Job outcome")
	}
	switch condition.Reason {
	case profiler.ResolutionReasonInvalidRunSpec, profiler.ResolutionReasonExecutionPolicyInvalid,
		profiler.ResolutionReasonActuationPolicyInvalid, measurementRequiredStateNotConfirmed:
	default:
		return measurementInUse("owned Run has no permanent pre-Job failure")
	}
	name, err := profilercontroller.ExecutionJobName(run)
	if err != nil {
		return err
	}
	jobs := &batch.JobList{}
	if err := o.client.List(ctx, jobs, client.InNamespace(run.Namespace)); err != nil {
		return err
	}
	for i := range jobs.Items {
		if jobs.Items[i].Name == name || meta.IsControlledBy(&jobs.Items[i], run) {
			return measurementInUse("pre-Job failure still has a native measurement execution Job")
		}
	}
	return o.checkRunPodsStopped(ctx, run, name, "")
}

func (o *measurementService) checkRunPodsStopped(
	ctx context.Context, run *api.ProfilingRun, jobName, jobUID string,
) error {
	pods, err := o.kube.CoreV1().Pods(run.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	for _, pod := range pods.Items {
		owner := meta.GetControllerOf(&pod)
		recordedJob := owner != nil && owner.Kind == "Job" && owner.APIVersion == batch.SchemeGroupVersion.String() &&
			((jobUID != "" && string(owner.UID) == jobUID) || (jobUID == "" && owner.Name == jobName))
		recordedPod := run.Status.ObservedExecution != nil && string(pod.UID) == run.Status.ObservedExecution.Pod.UID
		if (recordedJob || recordedPod) && pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			return measurementInUse("an owned measurement Pod remains active even if its Job is missing")
		}
	}
	return nil
}

func (o *measurementService) readDeletionEvidence(
	ctx context.Context, location evidencestore.ObjectLocation, missingOK bool,
) ([]byte, error) {
	body, err := o.objects.OpenLocation(ctx, location)
	if errors.Is(err, evidencestore.ErrNotFound) && missingOK {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(body, o.opts.maxEvidenceBytes+1))
	closeErr := body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(payload)) > o.opts.maxEvidenceBytes {
		return nil, errors.New("referenced evidence exceeds configured bound")
	}
	return payload, nil
}

type measurementReferenceGuard struct {
	locations map[evidencestore.ObjectLocation]bool
	ownedUIDs map[string]bool
}

func sharedMeasurementEvidence() error {
	return measurementInUse("another native measurement or baseline references the owned evidence")
}

// Only fields defined as evidence/profile references participate. Metadata,
// annotations, diagnostic strings and arbitrary JSON detail cannot veto cleanup.
func (g *measurementReferenceGuard) payload(payload []byte) error {
	var links struct {
		CurvePlan       evidencestore.ObjectLocation            `json:"curvePlan"`
		RawMeasurement  evidencestore.ObjectLocation            `json:"rawMeasurement"`
		CompletedTrials []campaign.CompletedTrialReference      `json:"completedTrials"`
		ExcludedTrials  []campaign.ExcludedTrialReference       `json:"excludedTrials"`
		FinalEvidence   map[string]evidencestore.ObjectLocation `json:"finalEvidence"`
		IdleBaseline    struct {
			Trials []struct {
				RawEvidence evidencestore.ObjectLocation `json:"rawEvidence"`
			} `json:"trials"`
		} `json:"idleBaseline"`
		Directions []struct {
			FinalEvidence []evidencestore.ObjectLocation `json:"finalEvidence"`
		} `json:"directions"`
	}
	if err := json.Unmarshal(payload, &links); err != nil {
		return err
	}
	if g.locations[links.CurvePlan] || g.locations[links.RawMeasurement] {
		return sharedMeasurementEvidence()
	}
	for _, trial := range links.CompletedTrials {
		location := evidencestore.ObjectLocation{Store: trial.Evidence.Store, Key: trial.Evidence.Key}
		if g.ownedUIDs[trial.Run.UID] || g.locations[location] {
			return sharedMeasurementEvidence()
		}
	}
	for _, trial := range links.ExcludedTrials {
		if g.ownedUIDs[trial.Run.UID] {
			return sharedMeasurementEvidence()
		}
	}
	for _, location := range links.FinalEvidence {
		if g.locations[location] {
			return sharedMeasurementEvidence()
		}
	}
	for _, trial := range links.IdleBaseline.Trials {
		if g.locations[trial.RawEvidence] {
			return sharedMeasurementEvidence()
		}
	}
	for _, direction := range links.Directions {
		for _, location := range direction.FinalEvidence {
			if g.locations[location] {
				return sharedMeasurementEvidence()
			}
		}
	}
	return nil
}

func (o *measurementService) sharedReference(
	ctx context.Context, reference *api.ProfilingRunEvidenceReference, guard *measurementReferenceGuard,
) error {
	if reference == nil {
		return nil
	}
	location := evidencestore.ObjectLocation{Store: reference.Store, Key: reference.Key}
	if guard.locations[location] {
		return sharedMeasurementEvidence()
	}
	payload, err := o.readDeletionEvidence(ctx, location, false)
	if err != nil {
		return err
	}
	return guard.payload(payload)
}

func (o *measurementService) checkSharedEvidence(
	ctx context.Context, task *api.BatchEnergyOptimization, owned []evidencestore.ObjectLocation,
) error {
	guard := &measurementReferenceGuard{
		locations: make(map[evidencestore.ObjectLocation]bool, len(owned)),
		ownedUIDs: map[string]bool{string(task.UID): true},
	}
	for _, location := range owned {
		guard.locations[location] = true
	}
	runs := &api.ProfilingRunList{}
	if err := o.client.List(ctx, runs); err != nil {
		return err
	}
	for i := range runs.Items {
		if meta.IsControlledBy(&runs.Items[i], task) {
			guard.ownedUIDs[string(runs.Items[i].UID)] = true
		}
	}
	for i := range runs.Items {
		if !meta.IsControlledBy(&runs.Items[i], task) {
			if err := o.sharedReference(ctx, runs.Items[i].Status.Evidence, guard); err != nil {
				return err
			}
		}
	}
	beos := &api.BatchEnergyOptimizationList{}
	if err := o.client.List(ctx, beos); err != nil {
		return err
	}
	for i := range beos.Items {
		if beos.Items[i].UID != task.UID {
			if err := o.sharedReference(ctx, beos.Items[i].Status.Result, guard); err != nil {
				return err
			}
		}
	}
	baselines := &api.ModelLoadedIdleBaselineList{}
	if err := o.client.List(ctx, baselines); err != nil {
		return err
	}
	for i := range baselines.Items {
		if err := o.sharedReference(ctx, baselines.Items[i].Status.Result, guard); err != nil {
			return err
		}
	}
	power := &api.PowerModeTransitionCostList{}
	if err := o.client.List(ctx, power); err != nil {
		return err
	}
	for i := range power.Items {
		if err := o.sharedReference(ctx, power.Items[i].Status.Result, guard); err != nil {
			return err
		}
	}
	return nil
}
