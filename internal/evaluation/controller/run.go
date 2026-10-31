package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
)

type RunReconciler struct{ Runtime }

func (r *RunReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	run := &api.EvaluationRun{}
	if err := r.APIReader.Get(ctx, req.NamespacedName, run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if run.Status.ObservedGeneration == 0 {
		before := run.DeepCopy()
		run.Status.ObservedGeneration = run.Generation
		for _, kind := range []string{api.EvaluationConditionPoolAcquired, api.EvaluationConditionExecutionComplete, api.EvaluationConditionMeasurementPassed, api.EvaluationConditionCleanupComplete, api.EvaluationConditionPublished} {
			condition(&run.Status.Conditions, run.Generation, kind, metav1.ConditionUnknown, api.EvaluationReasonNotStarted, "")
		}
		if err := run.Spec.Validate(); err != nil {
			condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, api.EvaluationReasonFrozenInputInvalid, err.Error())
			now := r.now()
			run.Status.FinishedAt = &now
		}
		return pending, r.patchStatus(ctx, run, before)
	}
	if run.Status.StartedAt == nil && run.Status.FinishedAt != nil {
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(run, api.EvaluationRunFinalizer) {
		if !run.DeletionTimestamp.IsZero() {
			return ctrl.Result{}, nil
		}
		before := run.DeepCopy()
		controllerutil.AddFinalizer(run, api.EvaluationRunFinalizer)
		return pending, r.Client.Patch(ctx, run, client.MergeFrom(before))
	}
	if run.Status.StartedAt == nil {
		if !run.DeletionTimestamp.IsZero() {
			return r.removeFinalizer(ctx, run)
		}
		return r.acquire(ctx, run)
	}
	if annotation := run.Annotations[api.EvaluationReleaseAnnotation]; annotation != "" {
		processed := false
		for _, record := range run.Status.ReleaseRecords {
			if record.OriginalAnnotation == annotation {
				processed = true
			}
		}
		if !processed {
			return r.release(ctx, run, annotation)
		}
	}
	if !run.DeletionTimestamp.IsZero() && !occupied(run) {
		return r.removeFinalizer(ctx, run)
	}
	if isTrue(run.Status.Conditions, api.EvaluationConditionPublished) {
		return ctrl.Result{}, nil
	}
	if run.Status.FinishedAt != nil {
		return r.publishRun(ctx, run, run.DeepCopy())
	}
	return r.reconcileRun(ctx, run)
}

func (r *RunReconciler) reconcileRun(ctx context.Context, run *api.EvaluationRun) (ctrl.Result, error) {
	before := run.DeepCopy()
	if err := r.workspace(ctx, run); err != nil {
		if !permanent(err) {
			return ctrl.Result{}, err
		}
		return r.runFailure(ctx, run, before, api.EvaluationReasonExecutionLost, err)
	}
	prepare := procedure(&run.Status.Procedures, api.EvaluationStepPrepare)
	if prepare.FinishedAt == nil {
		run.Status.CurrentProcedure = api.EvaluationStepPrepare
		result, done, err := r.advance(ctx, run, nil, api.EvaluationStepPrepare, prepare)
		if err != nil {
			if !permanent(err) {
				return ctrl.Result{}, err
			}
			r.failProcedure(prepare, err)
			condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, reason(err), err.Error())
		}
		if done && result != nil {
			if result.Execution.Status != complete {
				condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, result.Execution.Reason, "")
			} else if snapshot, ok := result.Outputs["inputSnapshot"]; ok {
				run.Status.InputSnapshotRef = &snapshot
			} else {
				r.failProcedure(prepare, errors.New("MissingInput: prepare did not return inputSnapshot"))
				condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, api.EvaluationReasonMissingInput, prepare.Reason)
			}
		}
		return pending, r.patchStatus(ctx, run, before)
	}
	return r.reconcileChildren(ctx, run, before)
}

func (r *RunReconciler) reconcileChildren(ctx context.Context, run, before *api.EvaluationRun) (ctrl.Result, error) {
	failed := meta.IsStatusConditionFalse(run.Status.Conditions, api.EvaluationConditionExecutionComplete)
	// Inspect every recorded child by exact UID, including the active one during deletion.
	verdicts := make([]api.MeasurementVerdict, len(run.Spec.Stages))
	children := map[string]*api.EvaluationStage{}
	for _, record := range run.Status.ChildRefs {
		child := &api.EvaluationStage{}
		err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: record.Name}, child)
		if apierrors.IsNotFound(err) || err == nil && child.UID != record.UID {
			return r.runFailure(ctx, run, before, api.EvaluationReasonExecutionLost, fmt.Errorf("recorded child %s/%s disappeared", record.Name, record.UID))
		}
		if err != nil {
			return ctrl.Result{}, err
		}
		children[record.StageID] = child
	}
	active := false
	stagesClean := !uncertainConditions(run.Status.Conditions)
	for i, t := range run.Spec.Stages {
		if child := children[t.ID]; child != nil {
			if !isTrue(child.Status.Conditions, api.EvaluationConditionStageCleanupComplete) || uncertainStage(child) {
				stagesClean = false
			}
			verdicts[i] = child.Status.MeasurementVerdict
			if child.Status.FinishedAt == nil {
				active = true
			}
			if meta.IsStatusConditionFalse(child.Status.Conditions, api.EvaluationConditionExecutionComplete) || meta.IsStatusConditionFalse(child.Status.Conditions, api.EvaluationConditionStageCleanupComplete) {
				failed = true
			}
		}
	}
	status, verdict := api.AggregateMeasurement(verdicts...)
	run.Status.MeasurementVerdict = verdict
	condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionMeasurementPassed, status, aggregateReason(run, children, verdict), "")
	if active {
		return pending, r.patchStatus(ctx, run, before)
	}
	if !failed && run.DeletionTimestamp.IsZero() {
		for i, t := range run.Spec.Stages {
			if children[t.ID] == nil {
				child, err := r.ensureChild(ctx, run, i, children)
				if err != nil {
					if !permanent(err) {
						return ctrl.Result{}, err
					}
					return r.runFailure(ctx, run, before, reason(err), err)
				}
				run.Status.ChildRefs = append(run.Status.ChildRefs, api.EvaluationChildReference{StageID: t.ID, EvaluationObjectReference: *ref(child)})
				run.Status.CurrentStage = t.ID
				run.Status.CurrentProcedure = ""
				return pending, r.patchStatus(ctx, run, before)
			}
		}
	}
	if !terminal(run.Status.Conditions, api.EvaluationConditionExecutionComplete) {
		s := metav1.ConditionTrue
		why := complete
		if failed || !run.DeletionTimestamp.IsZero() {
			s = metav1.ConditionFalse
			why = api.EvaluationReasonProcedureFailed
		}
		condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionExecutionComplete, s, why, "")
	}
	return r.finishRun(ctx, run, before, stagesClean)
}

func (r *RunReconciler) finishRun(ctx context.Context, run, before *api.EvaluationRun, stagesClean bool) (ctrl.Result, error) {
	finish := procedure(&run.Status.Procedures, api.EvaluationStepFinish)
	if finish.FinishedAt == nil {
		run.Status.CurrentProcedure = api.EvaluationStepFinish
		result, _, err := r.advance(ctx, run, nil, api.EvaluationStepFinish, finish)
		if err != nil {
			if !permanent(err) {
				return ctrl.Result{}, err
			}
			r.failProcedure(finish, err)
			condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionCleanupComplete, metav1.ConditionFalse, reason(err), err.Error())
		}
		if result != nil {
			s := metav1.ConditionFalse
			why := result.Execution.Reason
			if result.Cleanup != nil {
				why = result.Cleanup.Reason
				if result.Cleanup.Status == complete && result.Execution.Status == complete && stagesClean {
					s = metav1.ConditionTrue
				}
			}
			if !stagesClean {
				why = api.EvaluationReasonResidualState
			}
			if s == metav1.ConditionFalse && why == "" {
				why = api.EvaluationReasonResidualState
			}
			condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionCleanupComplete, s, why, "")
		}
		if finish.FinishedAt != nil {
			now := r.now()
			run.Status.FinishedAt = &now
			if isTrue(run.Status.Conditions, api.EvaluationConditionCleanupComplete) {
				condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionPoolAcquired, metav1.ConditionFalse, "Returned", "")
				run.Status.OccupyingRunUID = ""
			} else {
				run.Status.ResidualStateRef = finish.EvidenceRef
			}
		}
		return pending, r.patchStatus(ctx, run, before)
	}
	return r.publishRun(ctx, run, before)
}

func (r *RunReconciler) publishRun(ctx context.Context, run, before *api.EvaluationRun) (ctrl.Result, error) {
	publish := procedure(&run.Status.Procedures, api.EvaluationStepPublish)
	if publish.FinishedAt == nil {
		run.Status.CurrentProcedure = api.EvaluationStepPublish
		result, _, err := r.advance(ctx, run, nil, api.EvaluationStepPublish, publish)
		if err != nil {
			if !permanent(err) {
				return ctrl.Result{}, err
			}
			r.failProcedure(publish, err)
			condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionPublished, metav1.ConditionFalse, api.EvaluationReasonPublicationFailed, err.Error())
		}
		if result != nil {
			if summary, ok := result.Outputs["summary"]; result.Execution.Status == complete && ok {
				run.Status.FinalEvidenceRef = &summary
				condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionPublished, metav1.ConditionTrue, complete, "")
			} else {
				condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionPublished, metav1.ConditionFalse, api.EvaluationReasonPublicationFailed, result.Execution.Reason)
			}
		}
		return pending, r.patchStatus(ctx, run, before)
	}
	return ctrl.Result{}, nil
}

func uncertainStage(stage *api.EvaluationStage) bool {
	if uncertainConditions(stage.Status.Conditions) {
		return true
	}
	for _, record := range stage.Status.InjectionRecords {
		if record.Outcome == injectionUnknown {
			return true
		}
	}
	return false
}

func uncertainConditions(conditions []metav1.Condition) bool {
	c := meta.FindStatusCondition(conditions, api.EvaluationConditionExecutionComplete)
	return c != nil && (c.Reason == api.EvaluationReasonRemoteEffectUnknown || c.Reason == api.EvaluationReasonExecutionLost)
}
func occupied(run *api.EvaluationRun) bool {
	return run.Status.StartedAt != nil && !isTrue(run.Status.Conditions, api.EvaluationConditionCleanupComplete) && len(run.Status.ReleaseRecords) == 0
}
func (r *RunReconciler) acquire(ctx context.Context, run *api.EvaluationRun) (ctrl.Result, error) {
	list := &api.EvaluationRunList{}
	if err := r.APIReader.List(ctx, list, client.InNamespace(run.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	sort.Slice(list.Items, func(i, j int) bool {
		a, b := &list.Items[i], &list.Items[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.UID < b.UID
	})
	var winner *api.EvaluationRun
	for i := range list.Items {
		if occupied(&list.Items[i]) {
			winner = &list.Items[i]
			break
		}
	}
	if winner == nil {
		for i := range list.Items {
			c := &list.Items[i]
			if c.Status.StartedAt == nil && c.Status.FinishedAt == nil && c.DeletionTimestamp.IsZero() {
				winner = c
				break
			}
		}
	}
	before := run.DeepCopy()
	if winner != nil && winner.UID != run.UID {
		run.Status.OccupyingRunUID = winner.UID
		condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionPoolAcquired, metav1.ConditionFalse, api.EvaluationReasonPoolBusy, string(winner.UID))
		return pending, r.patchStatus(ctx, run, before)
	}
	cm := &corev1.ConfigMap{}
	if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: configName}, cm); err != nil {
		return ctrl.Result{}, err
	}
	if err := validateConfig(cm.Data); err != nil {
		return ctrl.Result{}, err
	}
	run.Status.FrozenConfig = &api.EvaluationFrozenConfig{EvaluationObjectReference: *ref(cm), ResourceVersion: cm.ResourceVersion, Values: cm.DeepCopy().Data}
	now := r.now()
	run.Status.StartedAt = &now
	run.Status.OccupyingRunUID = run.UID
	condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionPoolAcquired, metav1.ConditionTrue, "Acquired", "")
	return pending, r.patchStatus(ctx, run, before)
}
func (r *RunReconciler) workspace(ctx context.Context, run *api.EvaluationRun) error {
	name := uidName(run.UID) + "-work"
	v := run.Status.FrozenConfig.Values
	pvc := &corev1.PersistentVolumeClaim{}
	err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: name}, pvc)
	if apierrors.IsNotFound(err) {
		if run.Status.Workspace != nil {
			return errExecutionLost
		}
		size, e := resource.ParseQuantity(v["workspaceSize"])
		if e != nil {
			return e
		}
		pvc = &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Namespace: run.Namespace, Name: name}, Spec: corev1.PersistentVolumeClaimSpec{AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany}, StorageClassName: ptr(v["storageClass"]), Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: size}}}}
		if e = controllerutil.SetControllerReference(run, pvc, r.Scheme); e != nil {
			return e
		}
		err = r.Client.Create(ctx, pvc)
		if apierrors.IsAlreadyExists(err) {
			err = r.APIReader.Get(ctx, client.ObjectKeyFromObject(pvc), pvc)
		}
	}
	if err != nil {
		return err
	}
	if !metav1.IsControlledBy(pvc, run) || run.Status.Workspace != nil && run.Status.Workspace.UID != pvc.UID {
		return errExecutionLost
	}
	run.Status.Workspace = ref(pvc)
	frozen, e := jsonString(run.Status.FrozenConfig.Values)
	if e != nil {
		return e
	}
	cm, e := r.ensureConfigMap(ctx, run, uidName(run.UID)+"-config", map[string]string{"config.json": frozen})
	if e != nil {
		return e
	}
	run.Status.FrozenConfigMapRef = ref(cm)
	if run.Status.InputSnapshotRef == nil {
		return nil
	}
	snapshotRef, e := jsonString(run.Status.InputSnapshotRef)
	if e != nil {
		return e
	}
	cm, e = r.ensureConfigMap(ctx, run, uidName(run.UID)+"-input", map[string]string{"snapshot-ref.json": snapshotRef})
	if e != nil {
		return e
	}
	run.Status.InputConfigMapRef = ref(cm)
	return nil
}
func (r *RunReconciler) ensureChild(ctx context.Context, run *api.EvaluationRun, index int, children map[string]*api.EvaluationStage) (*api.EvaluationStage, error) {
	t := run.Spec.Stages[index]
	start := t.StartState.EvidenceRef
	if symbolic := t.StartState.StageResultRef; symbolic != nil {
		prior := children[symbolic.StageID]
		if prior == nil {
			return nil, fmt.Errorf("MissingInput: predecessor absent")
		}
		value, ok := prior.Status.Outputs[symbolic.Output]
		if !ok {
			return nil, fmt.Errorf("MissingInput: predecessor output %s absent", symbolic.Output)
		}
		start = &value
	}
	if start == nil || run.Status.InputSnapshotRef == nil {
		return nil, fmt.Errorf("MissingInput: snapshot/start state absent")
	}
	control := run.Spec.Control
	if t.Control != nil {
		control = *t.Control
	}
	child := &api.EvaluationStage{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("%s-s%03d", uidName(run.UID), index), Namespace: run.Namespace}, Spec: api.EvaluationStageSpec{StageID: t.ID, Row: t.Row, Mode: t.Mode, LoadScenarioRef: t.LoadScenarioRef, Target: run.Spec.Target, Harness: run.Spec.Harness, LoadGenerator: run.Spec.LoadGenerator, PowerMeasurement: run.Spec.PowerMeasurement, Control: control, Judgment: run.Spec.Judgment, EvidenceStore: run.Spec.EvidenceStore, FrozenConfigMapRef: *run.Status.FrozenConfigMapRef, InputConfigMapRef: *run.Status.InputConfigMapRef, InputSnapshotRef: *run.Status.InputSnapshotRef, Workspace: *run.Status.Workspace, StartState: api.EvaluationStageStartState{EvidenceRef: *start}, FailureInjection: t.FailureInjection}}
	if err := controllerutil.SetControllerReference(run, child, r.Scheme); err != nil {
		return nil, err
	}
	existing := &api.EvaluationStage{}
	err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(child), existing)
	if apierrors.IsNotFound(err) {
		err = r.Client.Create(ctx, child)
		if err == nil {
			return child, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		err = r.APIReader.Get(ctx, client.ObjectKeyFromObject(child), existing)
	}
	if err != nil {
		return nil, err
	}
	if !metav1.IsControlledBy(existing, run) || !equality.Semantic.DeepEqual(existing.Spec, child.Spec) {
		return nil, fmt.Errorf("IdentityMismatch: child frozen contract differs")
	}
	return existing, nil
}
func (r *Runtime) failProcedure(p *api.EvaluationProcedure, err error) {
	now := r.now()
	p.FinishedAt = &now
	p.Reason = reason(err)
}
func reason(err error) string {
	if errors.Is(err, errExecutionLost) {
		return api.EvaluationReasonExecutionLost
	}
	if apierrors.IsInvalid(err) {
		return api.EvaluationReasonFrozenInputInvalid
	}
	for _, why := range []string{api.EvaluationReasonIdentityMismatch, api.EvaluationReasonMissingInput, api.EvaluationReasonRemoteEffectUnknown, api.EvaluationReasonEvidenceUnreadable, api.EvaluationReasonFrozenInputInvalid} {
		if len(err.Error()) >= len(why) && err.Error()[:len(why)] == why {
			return why
		}
	}
	return api.EvaluationReasonProcedureFailed
}
func permanent(err error) bool { return reason(err) != api.EvaluationReasonProcedureFailed }
func (r *RunReconciler) runFailure(ctx context.Context, run, before *api.EvaluationRun, why string, err error) (ctrl.Result, error) {
	condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, why, err.Error())
	condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionCleanupComplete, metav1.ConditionFalse, api.EvaluationReasonResidualState, err.Error())
	now := r.now()
	run.Status.FinishedAt = &now
	evidence, saveErr := r.audit(ctx, run, "", "run/residual.json", "", run.Status)
	if saveErr != nil {
		return ctrl.Result{}, saveErr
	}
	run.Status.ResidualStateRef = evidence
	return ctrl.Result{}, r.patchStatus(ctx, run, before)
}
func (r *RunReconciler) removeFinalizer(ctx context.Context, run *api.EvaluationRun) (ctrl.Result, error) {
	before := run.DeepCopy()
	controllerutil.RemoveFinalizer(run, api.EvaluationRunFinalizer)
	return ctrl.Result{}, r.Client.Patch(ctx, run, client.MergeFrom(before))
}

func (r *RunReconciler) release(ctx context.Context, run *api.EvaluationRun, annotation string) (ctrl.Result, error) {
	var request struct {
		Reason      string                          `json:"reason"`
		At          metav1.Time                     `json:"at"`
		EvidenceRef api.EvaluationEvidenceReference `json:"evidenceRef"`
	}
	if err := json.Unmarshal([]byte(annotation), &request); err != nil || request.Reason == "" || request.At.IsZero() || request.EvidenceRef.Size < 1 || request.EvidenceRef.Store == "" || request.EvidenceRef.Key == "" {
		return ctrl.Result{}, fmt.Errorf("release annotation requires reason, at, evidenceRef")
	}
	// The operator attests prior writers are stopped; independently observe pool return.
	v := run.Status.FrozenConfig.Values
	var state schedulerState
	if err := r.HTTP(ctx, run.Namespace, v, "/api/v1/scheduler/state", &state); err != nil {
		return ctrl.Result{}, err
	}
	if !state.returned(r.now().Time, v) {
		return pending, nil
	}
	evidence, err := r.audit(ctx, run, "", fmt.Sprintf("run/releases/%03d.json", len(run.Status.ReleaseRecords)), annotation, map[string]any{"status": run.Status, "state": state})
	if err != nil {
		return ctrl.Result{}, err
	}
	before := run.DeepCopy()
	run.Status.ReleaseRecords = append(run.Status.ReleaseRecords, api.EvaluationReleaseRecord{OriginalAnnotation: annotation, ProcessedAt: r.now(), EvidenceRef: *evidence})
	run.Status.OccupyingRunUID = ""
	if run.Status.FinishedAt == nil {
		now := r.now()
		run.Status.FinishedAt = &now
	}
	if !terminal(run.Status.Conditions, api.EvaluationConditionExecutionComplete) {
		condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, "OperatorReleased", "")
	}
	condition(&run.Status.Conditions, run.Generation, api.EvaluationConditionPoolAcquired, metav1.ConditionFalse, "OperatorReleased", "")
	return pending, r.patchStatus(ctx, run, before)
}

func aggregateReason(run *api.EvaluationRun, children map[string]*api.EvaluationStage, verdict api.MeasurementVerdict) string {
	for _, template := range run.Spec.Stages {
		child := children[template.ID]
		if child != nil && child.Status.MeasurementVerdict == verdict {
			if c := meta.FindStatusCondition(child.Status.Conditions, api.EvaluationConditionMeasurementPassed); c != nil {
				return c.Reason
			}
		}
	}
	return string(verdict)
}
