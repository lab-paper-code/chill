package controller

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/aws/smithy-go"
	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilerprojection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PrejobObjectIdentity binds an observation to the exact API object revision.
type PrejobObjectIdentity struct {
	Namespace       string    `json:"namespace"`
	Name            string    `json:"name"`
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion"`
	Generation      int64     `json:"generation"`
}

// PrejobIdleIdentity retains the existing verified evidence reference unchanged.
type PrejobIdleIdentity struct {
	PrejobObjectIdentity
	Evidence *edge.ProfilingRunEvidenceReference `json:"evidence"`
}

// PrejobFailureInspection is an observation receipt, never mutation authority.
// Only the frozen-plan, verified idle-three, unstarted K1/a1 boundary qualifies.
// Every backend error and every other execution state fails closed.
type PrejobFailureInspection struct {
	Version  int                  `json:"version"`
	Eligible bool                 `json:"eligible"`
	Reason   string               `json:"reason"`
	Request  PrejobObjectIdentity `json:"request"`
	Child    PrejobObjectIdentity `json:"child"`
	Plan     struct {
		Store         string `json:"store"`
		Key           string `json:"key"`
		SchemaVersion string `json:"schemaVersion"`
	} `json:"plan"`
	OwnedRunCount         int                  `json:"ownedRunCount"`
	OwnedJobCount         int                  `json:"ownedJobCount"`
	IdleRuns              []PrejobIdleIdentity `json:"idleRuns"`
	PlanFound             bool                 `json:"planFound"`
	PublishedResultAbsent bool                 `json:"publishedResultAbsent"`
	Admission             struct {
		Granted bool      `json:"granted"`
		At      time.Time `json:"at"`
	} `json:"admission"`
	Node struct {
		Name            string    `json:"name"`
		UID             types.UID `json:"uid"`
		ResourceVersion string    `json:"resourceVersion"`
		Holder          string    `json:"holder"`
	} `json:"node"`
	CheckedAt time.Time `json:"checkedAt"`
}

func prejobIdentity(object client.Object) PrejobObjectIdentity {
	return PrejobObjectIdentity{object.GetNamespace(), object.GetName(), object.GetUID(), object.GetResourceVersion(), object.GetGeneration()}
}

// InspectPrejobFailure calls only read operations. verifyIdle must use the
// existing retained-evidence boundary, including its frozen Store/Secret binding.
func InspectPrejobFailure(ctx context.Context, reader client.Reader, objects evidencestore.LocationReader,
	plans *CurvePlanSource, resolver *profilercontroller.RunConfigResolver,
	verifyIdle func(context.Context, *edge.ProfilingRun, evidencestore.ObjectIdentity) (profilerevidence.VerifiedIdleEvidence, error),
	logicalStore string, key types.NamespacedName, uid types.UID,
) (PrejobFailureInspection, error) {
	proof := PrejobFailureInspection{Version: 1, CheckedAt: time.Now().UTC(), IdleRuns: []PrejobIdleIdentity{}}
	stop := func(err error) (PrejobFailureInspection, error) { proof.Reason = err.Error(); return proof, err }
	if reader == nil || objects == nil || plans == nil || resolver == nil || verifyIdle == nil || logicalStore == "" || key.Namespace == "" || key.Name == "" || uid == "" {
		return stop(errors.New("InvalidInspectionInputs"))
	}
	parent, err := inspectPrejobRequest(ctx, reader, key, uid, &proof)
	if err != nil {
		return stop(err)
	}
	plan, reference, err := plans.readStored(ctx, plans.location(parent.UID))
	if err != nil {
		return stop(errors.New("RecordedPlanReadFailed"))
	}
	proof.Plan.Store, proof.Plan.Key, proof.Plan.SchemaVersion = reference.Store, reference.Key, reference.SchemaVersion
	proof.PlanFound = true
	if reference.Store != logicalStore || len(plan.PlannedBatchSizes()) == 0 || plan.PlannedBatchSizes()[0] != 1 {
		return stop(errors.New("RecordedPlanMismatch"))
	}
	node, err := inspectPrejobNode(ctx, reader, parent, plan.Shared().NodeUID, &proof)
	if err != nil {
		return stop(err)
	}
	runs, err := inspectPrejobOwnedRuns(ctx, reader, parent, &proof)
	if err != nil {
		return stop(err)
	}
	active, err := inspectPrejobTrials(ctx, runs, parent, node, plan, verifyIdle, &proof)
	if err != nil {
		return stop(err)
	}
	if err := inspectPrejobExecutions(ctx, reader, runs, active, &proof); err != nil {
		return stop(err)
	}
	for _, key := range profilercontroller.ExecutionEvidenceObjectKeys(active.UID) {
		if err := prejobRequireAbsent(ctx, objects, evidencestore.ObjectLocation{Store: logicalStore, Key: key}); err != nil {
			return stop(errors.New("ActiveEvidenceNotAbsent"))
		}
	}
	if err := prejobRequireAbsent(ctx, objects, evidencestore.ObjectLocation{Store: logicalStore, Key: fmt.Sprintf("batch-energy-optimizations/%s/result.json", uid)}); err != nil {
		return stop(errors.New("PublishedResultNotAbsent"))
	}
	proof.PublishedResultAbsent = true
	_, err = resolver.Resolve(ctx, active)
	var capacity *profiler.MetricCapacityError
	reason, terminal := profiler.TerminalResolutionReason(err)
	if !terminal || reason != profiler.ResolutionReasonExecutionPolicyInvalid || !errors.As(err, &capacity) {
		return stop(errors.New("NotDeterministicCapacityFailure"))
	}
	if ctx.Err() != nil {
		return stop(errors.New("InspectionCanceled"))
	}
	proof.Eligible, proof.Reason = true, reason
	return proof, nil
}

func inspectPrejobRequest(ctx context.Context, reader client.Reader, key types.NamespacedName, uid types.UID, proof *PrejobFailureInspection) (*edge.BatchEnergyOptimization, error) {
	parent := &edge.BatchEnergyOptimization{}
	if err := reader.Get(ctx, key, parent); err != nil {
		return nil, errors.New("RequestReadFailed")
	}
	proof.Request = prejobIdentity(parent)
	if parent.UID != uid || parent.Generation < 1 || parent.ResourceVersion == "" || !parent.DeletionTimestamp.IsZero() || parent.Spec.TrialsPerBatch != 3 {
		return nil, errors.New("RequestIdentityMismatch")
	}
	if parent.Status.Result != nil || parent.Status.ObservedGeneration != parent.Generation || parent.Status.Phase != batchEnergyOptimizationPhaseMeasuring || parent.Status.CompletedTrials != 3 || parent.Status.Outcome != "" {
		return nil, errors.New("RequestStateMismatch")
	}
	condition := apimeta.FindStatusCondition(parent.Status.Conditions, batchEnergyOptimizationConditionDone)
	if len(parent.Status.Conditions) != 1 || condition == nil || condition.ObservedGeneration != parent.Generation || condition.Status != metav1.ConditionUnknown || condition.Reason != "TrialInFlight" {
		return nil, errors.New("RequestConditionMismatch")
	}
	granted, at, _ := profilingrun.Admission(parent.Annotations)
	proof.Admission.Granted, proof.Admission.At = granted, at
	if !granted || at.IsZero() || at.After(proof.CheckedAt) {
		return nil, errors.New("AdmissionMismatch")
	}
	return parent, nil
}

func inspectPrejobNode(ctx context.Context, reader client.Reader, parent *edge.BatchEnergyOptimization, uid string, proof *PrejobFailureInspection) (*corev1.Node, error) {
	node, err := profilingrun.GrantedNode(ctx, reader, parent.UID, parent.Spec.State.TargetNode, uid)
	if err != nil {
		return nil, errors.New("NodeGrantReadFailed")
	}
	holder, held := profilingrun.MeasurementHolder(node)
	proof.Node.Name, proof.Node.UID, proof.Node.ResourceVersion, proof.Node.Holder = node.Name, node.UID, node.ResourceVersion, holder
	holders := 0
	for _, taint := range node.Spec.Taints {
		if taint.Key == profilingrun.MeasurementTaintKey {
			if taint.Effect != corev1.TaintEffectNoSchedule {
				return nil, errors.New("NodeHolderMismatch")
			}
			holders++
		}
	}
	if !held || holder != string(parent.UID) || holders != 1 || node.UID == "" || node.ResourceVersion == "" || !node.DeletionTimestamp.IsZero() {
		return nil, errors.New("NodeHolderMismatch")
	}
	return node, nil
}

func inspectPrejobOwnedRuns(ctx context.Context, reader client.Reader, parent *edge.BatchEnergyOptimization, proof *PrejobFailureInspection) ([]edge.ProfilingRun, error) {
	all := &edge.ProfilingRunList{}
	if err := reader.List(ctx, all, client.InNamespace(parent.Namespace)); err != nil {
		return nil, errors.New("RunListFailed")
	}
	var runs []edge.ProfilingRun
	for i := range all.Items {
		run := &all.Items[i]
		owner := metav1.GetControllerOf(run)
		if owner != nil && (owner.UID == parent.UID || (owner.Kind == "BatchEnergyOptimization" && owner.Name == parent.Name)) {
			if !controlledByParent(parent, run) || owner.Name != parent.Name {
				return nil, errors.New("RunOwnerMismatch")
			}
			runs = append(runs, *run)
		}
	}
	proof.OwnedRunCount = len(runs)
	if len(runs) != 4 {
		return nil, errors.New("RunCountMismatch")
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Name < runs[j].Name })
	return runs, nil
}

func inspectPrejobTrials(ctx context.Context, runs []edge.ProfilingRun, parent *edge.BatchEnergyOptimization, node *corev1.Node, plan profilederivation.CurvePlan,
	verifyIdle func(context.Context, *edge.ProfilingRun, evidencestore.ObjectIdentity) (profilerevidence.VerifiedIdleEvidence, error), proof *PrejobFailureInspection,
) (*edge.ProfilingRun, error) {
	var active *edge.ProfilingRun
	for i := range runs {
		run := &runs[i]
		trial, err := projectOwnedTrial(parent, run)
		if err != nil || run.Generation < 1 || run.UID == "" || run.ResourceVersion == "" || !run.DeletionTimestamp.IsZero() || run.Spec.MeasurementStateSpec != parent.Spec.State {
			return nil, errors.New("RunIdentityMismatch")
		}
		if trial.Measurement.Batch != 1 {
			return nil, errors.New("RunTrialMismatch")
		}
		if run.Spec.Measurement.WorkloadMode == activeWorkloadMode {
			if active != nil || trial.Attempt != 1 || !reflect.DeepEqual(run.Status, edge.ProfilingRunStatus{}) {
				return nil, errors.New("ActiveRunStarted")
			}
			active = run
			proof.Child = prejobIdentity(run)
			continue
		}
		if trial.Attempt < 1 || trial.Attempt > 3 {
			return nil, errors.New("IdleTrialMismatch")
		}
		if err := inspectPrejobIdle(ctx, run, node, plan, verifyIdle); err != nil {
			return nil, err
		}
		proof.IdleRuns = append(proof.IdleRuns, PrejobIdleIdentity{prejobIdentity(run), run.Status.Evidence})
	}
	if active == nil || len(proof.IdleRuns) != 3 {
		return nil, errors.New("TrialSetMismatch")
	}
	return active, nil
}

func inspectPrejobIdle(ctx context.Context, run *edge.ProfilingRun, node *corev1.Node, plan profilederivation.CurvePlan, verifyIdle func(context.Context, *edge.ProfilingRun, evidencestore.ObjectIdentity) (profilerevidence.VerifiedIdleEvidence, error)) error {
	if run.Status.ObservedGeneration != run.Generation || run.Status.HasTerminalFailure() {
		return errors.New("IdleRunStateMismatch")
	}
	for _, kind := range []string{edge.ProfilingRunConditionExecutionComplete, edge.ProfilingRunConditionEvidenceVerified} {
		condition := apimeta.FindStatusCondition(run.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != run.Generation {
			return errors.New("IdleRunNotVerified")
		}
	}
	if run.Status.ObservedExecution == nil || run.Status.ObservedExecution.Node.Name != node.Name || run.Status.ObservedExecution.Node.UID != string(node.UID) {
		return errors.New("IdleNodeMismatch")
	}
	identity, err := curveEvidenceIdentity(run)
	if err != nil {
		return errors.New("IdleEvidenceReadFailed")
	}
	verified, err := verifyIdle(ctx, run, identity)
	if err != nil {
		return errors.New("IdleEvidenceReadFailed")
	}
	if profilerprojection.ValidateRecordedIdleEvidence(plan, verified) != nil {
		return errors.New("IdleEvidencePlanMismatch")
	}
	return nil
}

func inspectPrejobExecutions(ctx context.Context, reader client.Reader, runs []edge.ProfilingRun, active *edge.ProfilingRun, proof *PrejobFailureInspection) error {
	jobs := &batchv1.JobList{}
	if err := reader.List(ctx, jobs, client.InNamespace(active.Namespace)); err != nil {
		return errors.New("JobListFailed")
	}
	for i := range jobs.Items {
		owner := metav1.GetControllerOf(&jobs.Items[i])
		if owner != nil && owner.UID == proof.Request.UID {
			proof.OwnedJobCount++
		}
	}
	for i := range runs {
		name, err := profilercontroller.ExecutionJobName(&runs[i])
		if err != nil {
			return errors.New("JobIdentityInvalid")
		}
		for j := range jobs.Items {
			job := &jobs.Items[j]
			owner := metav1.GetControllerOf(job)
			if job.Name == name || (owner != nil && owner.UID == runs[i].UID) {
				proof.OwnedJobCount++
			}
		}
	}
	if proof.OwnedJobCount != 0 {
		return errors.New("NativeJobExists")
	}
	pods := &corev1.PodList{}
	if err := reader.List(ctx, pods, client.InNamespace(active.Namespace)); err != nil {
		return errors.New("PodListFailed")
	}
	name, err := profilercontroller.ExecutionJobName(active)
	if err != nil {
		return errors.New("JobIdentityInvalid")
	}
	for i := range pods.Items {
		owner := metav1.GetControllerOf(&pods.Items[i])
		if owner != nil && ((owner.Kind == "Job" && owner.Name == name) || owner.UID == active.UID) {
			return errors.New("ActiveExecutionPodExists")
		}
	}
	return nil
}

func prejobRequireAbsent(ctx context.Context, objects evidencestore.LocationReader, location evidencestore.ObjectLocation) error {
	body, err := objects.OpenLocation(ctx, location)
	if body != nil {
		if body.Close() != nil {
			return errors.New("ObjectCloseFailed")
		}
		if err != nil {
			return errors.New("ContradictoryObjectRead")
		}
		return errors.New("ObjectExists")
	}
	if err == nil {
		return errors.New("ObjectAbsenceUnproven")
	}
	var backend smithy.APIError
	if errors.As(err, &backend) {
		switch backend.ErrorCode() {
		case "NoSuchKey", "NotFound":
		default:
			return errors.New("ObjectReadFailed")
		}
	}
	if !errors.Is(err, evidencestore.ErrNotFound) {
		return errors.New("ObjectReadFailed")
	}
	return nil
}
