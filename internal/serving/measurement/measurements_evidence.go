package measurement

import (
	"context"
	"errors"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	batch "k8s.io/api/batch/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func measurementInUse(reason string) error {
	return &serving.Error{Code: "MeasurementInUse", Message: reason}
}

// deletionPrefixes applies every refusal under the scheduler lock and returns
// the owned prefixes; store I/O runs after the lock is released.
func (o *Service) deletionPrefixes(
	ctx context.Context, namespace, name, uid, version string,
) (*api.BatchEnergyOptimization, []string, error) {
	if o.scheduler != nil {
		o.scheduler.Lock.Lock()
		defer o.scheduler.Lock.Unlock()
	}
	task := &api.BatchEnergyOptimization{}
	if err := o.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, task); err != nil {
		return nil, nil, err
	}
	if string(task.UID) != uid || task.ResourceVersion != version {
		resource := schema.GroupResource{Group: api.GroupVersion.Group, Resource: "batchenergyoptimizations"}
		return task, nil, apierrors.NewConflict(resource, name, errors.New("native UID or resourceVersion changed"))
	}
	// The campaign reconciler stops writing the plan, result and Runs only once
	// it records a current terminal status; Pending and Measuring are not.
	if !campaign.TerminalStatusCurrent(task) {
		return task, nil, measurementInUse("only a current terminal measurement can be deleted; " +
			"cancellation and recoverable failure interruption are unsupported")
	}
	// The serving observer attributes a live instance by its profile annotation;
	// deleting the bundle under it breaks attribution and restart.
	pods, err := o.kube.CoreV1().Pods(o.opts.ServingNamespace).List(ctx, meta.ListOptions{})
	if err != nil {
		return task, nil, err
	}
	for _, pod := range pods.Items {
		if strings.HasPrefix(pod.Annotations[servingstate.ServingProfileAnnotation], namespace+"/"+name+"/") &&
			pod.Status.Phase != core.PodSucceeded && pod.Status.Phase != core.PodFailed {
			return task, nil, measurementInUse("a physical serving Pod still references the measurement")
		}
	}
	// A prepared profile lets the scheduler start an instance whose init
	// container fetches the bundle deleted here. The catalog skips a measurement
	// carrying the invalid annotation, so the next refresh stops preparing its
	// profiles; a superseded or replaced measurement is not prepared either.
	if o.scheduler != nil && o.scheduler.LoadProfiles != nil {
		if _, profiles, preparations, ok := o.scheduler.LoadProfiles(); ok {
			id := namespace + "/" + name + "/" + uid
			for _, profile := range profiles {
				if _, prepared := preparations[profile.ID]; prepared && derivedProfile(profile, id) {
					return task, nil, measurementInUse("the scheduler prepares serving profile " + profile.ID +
						" from this measurement; annotate the measurement with " + profilingrun.InvalidAnnotation +
						" and retry after the next profile refresh")
				}
			}
		}
	}
	prefixes, err := o.measurementPrefixes(ctx, task)
	return task, prefixes, err
}

// measurementPrefixes returns every Evidence Store prefix the measurement owns:
// its own UID, its serving profile ID and the UID of each Run it controls.
// Every writer keys objects by its own owner, and the trailing separator stops
// one UID from matching another. Ownership comes from native controller UIDs;
// stored payloads are never read, so objects an older reader wrote stay
// deletable.
func (o *Service) measurementPrefixes(ctx context.Context, task *api.BatchEnergyOptimization) ([]string, error) {
	uid := string(task.UID)
	profile := task.Namespace + "/" + task.Name + "/" + uid + "/"
	prefixes := []string{
		"batch-energy-optimizations/" + uid + "/",
		"serving/profiles/" + profile,
		"serving/profile-reuse/" + profile,
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
		// The profiler controller writes raw and execution evidence until the
		// Run records an outcome, even after its Job completed.
		if !runOutcomeRecorded(run) {
			return nil, measurementInUse("an owned Run has not recorded its outcome")
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

// runOutcomeRecorded reports a Run whose controller has stopped writing
// evidence: verified evidence, a current execution failure, or evidence it
// judged invalid. Reasons are not enumerated so older Runs stay deletable.
func runOutcomeRecorded(run *api.ProfilingRun) bool {
	if run.Status.Evidence != nil {
		return true
	}
	execution := apimeta.FindStatusCondition(run.Status.Conditions, api.ProfilingRunConditionExecutionComplete)
	if execution != nil && execution.Status == meta.ConditionFalse && execution.ObservedGeneration == run.Generation {
		return true
	}
	evidence := apimeta.FindStatusCondition(run.Status.Conditions, api.ProfilingRunConditionEvidenceVerified)
	return evidence != nil && evidence.Status == meta.ConditionFalse && evidence.Reason == "EvidenceInvalid"
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

// derivedProfile reports a serving profile built from the measurement id
// (namespace/name/UID): its own profile, a reuse profile it confirmed, or a
// reuse profile whose batches it supplied.
func derivedProfile(profile serving.Profile, id string) bool {
	return profile.ID == id || strings.HasPrefix(profile.ID, id+"/") ||
		profile.Reuse != nil && profile.Reuse.SourceProfileID == id
}

func (o *Service) measurementLocation(key string) evidencestore.ObjectLocation {
	return evidencestore.ObjectLocation{Store: o.opts.StoreName, Key: key}
}
