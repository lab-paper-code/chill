package measurement

import (
	"context"
	"errors"
	"io"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/serving"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const measurementPhaseFailed = "Failed"

// measurementTerminal mirrors the campaign reconciler's stop condition
// (terminalCampaignStatusCurrent): only then does it stop writing the plan,
// result and Runs under this measurement's prefixes.
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
			condition.Reason == "EnergyObjectivePolicyMismatch" || condition.Reason == "InvalidCampaignRun" ||
			condition.Reason == profiler.ResolutionReasonInvalidRunSpec ||
			condition.Reason == profiler.ResolutionReasonExecutionPolicyInvalid ||
			condition.Reason == profiler.ResolutionReasonActuationPolicyInvalid ||
			condition.Reason == string(campaign.FailureClassFatalFailure) ||
			condition.Reason == string(campaign.FailureClassUnknownFailure))
}

func measurementInUse(reason string) error {
	return &serving.Error{Code: "MeasurementInUse", Message: reason}
}

// measurementPrefixes returns every Evidence Store prefix the measurement owns:
// its own UID and the UID of each Run it controls. Every writer keys objects
// by its own owner UID, and the trailing separator stops one UID from matching
// another. Ownership comes from native controller UIDs; stored payloads are
// never read, so objects an older reader wrote stay deletable.
func (o *Service) measurementPrefixes(ctx context.Context, task *api.BatchEnergyOptimization) ([]string, error) {
	uid := string(task.UID)
	prefixes := []string{
		"batch-energy-optimizations/" + uid + "/",
		"serving/profiles/" + task.Namespace + "/" + task.Name + "/" + uid + "/",
	}
	runs := &api.ProfilingRunList{}
	if err := o.client.List(ctx, runs, client.InNamespace(task.Namespace)); err != nil {
		return nil, err
	}
	owned, jobs, pods := map[types.UID]bool{}, map[types.UID]bool{}, map[types.UID]bool{}
	for i := range runs.Items {
		run := &runs.Items[i]
		if !meta.IsControlledBy(run, task) {
			continue
		}
		owned[run.UID] = true
		prefixes = append(prefixes, "profiling-runs/"+string(run.UID)+"/")
		if run.Status.Job != nil {
			jobs[types.UID(run.Status.Job.UID)] = true
		}
		if run.Status.ObservedExecution != nil {
			pods[types.UID(run.Status.ObservedExecution.Pod.UID)] = true
		}
	}
	if len(owned) == 0 {
		return prefixes, nil
	}
	// An active worker or load Job can still write raw evidence after cleanup.
	jobList := &batch.JobList{}
	if err := o.client.List(ctx, jobList, client.InNamespace(task.Namespace)); err != nil {
		return nil, err
	}
	for i := range jobList.Items {
		owner := meta.GetControllerOf(&jobList.Items[i])
		if owner == nil || !owned[owner.UID] {
			continue
		}
		if !measurementJobTerminal(&jobList.Items[i]) {
			return nil, measurementInUse("an owned measurement Job remains active")
		}
		jobs[jobList.Items[i].UID] = true
	}
	// A Pod outlives a deleted Job and can still write raw evidence.
	podList, err := o.kube.CoreV1().Pods(task.Namespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return nil, err
	}
	for _, pod := range podList.Items {
		owner := meta.GetControllerOf(&pod)
		if ((owner != nil && jobs[owner.UID]) || pods[pod.UID]) &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			return nil, measurementInUse("an owned measurement Pod remains active even if its Job is missing")
		}
	}
	return prefixes, nil
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

func (o *Service) measurementLocation(key string) evidencestore.ObjectLocation {
	return evidencestore.ObjectLocation{Store: o.opts.StoreName, Key: key}
}

func (o *Service) readDeletionEvidence(
	ctx context.Context, location evidencestore.ObjectLocation, missingOK bool,
) ([]byte, error) {
	body, err := o.objects.OpenLocation(ctx, location)
	if errors.Is(err, evidencestore.ErrNotFound) && missingOK {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(body, o.opts.MaxEvidenceBytes+1))
	closeErr := body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, err
	}
	if int64(len(payload)) > o.opts.MaxEvidenceBytes {
		return nil, errors.New("referenced evidence exceeds configured bound")
	}
	return payload, nil
}
