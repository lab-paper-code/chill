package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func fixtureStageExecution(t *testing.T) (Runtime, memoryStore, *api.EvaluationRun, *api.EvaluationStage) {
	t.Helper()
	ctx := context.Background()
	parent := fixtureRun("review")
	startRun(parent)
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "serving", Namespace: parent.Namespace, UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: node.Name}}
	r, store := fixtureRuntime(t, parent, node, pod)
	stage := fixtureStage(t, &r, parent)
	stage.Finalizers = []string{api.EvaluationStageFinalizer}
	stage.Spec.FailureInjection = []api.FaultInjectionPlan{{ID: "fault", Action: api.FaultInjectionCordonDelete, Selection: &api.FaultInjectionSelection{WorkloadUIDs: []types.UID{"workload-uid"}, OrderBy: []api.FaultInjectionOrderKey{"node", "pod", "instance"}}}}
	if err := r.Client.Update(ctx, stage); err != nil {
		t.Fatal(err)
	}
	before := parent.DeepCopy()
	parent.Status.CurrentStage = stage.Spec.StageID
	parent.Status.ChildRefs = []api.EvaluationChildReference{{StageID: stage.Spec.StageID, EvaluationObjectReference: *ref(stage)}}
	if err := r.patchStatus(ctx, parent, before); err != nil {
		t.Fatal(err)
	}
	goKey := evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/go.json")
	store.put(goKey, map[string]any{"runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "goUTC": fixtureTime})
	p := api.EvaluationProcedure{Step: api.EvaluationStepStage}
	job, err := r.job(ctx, stage, parent, stage, api.EvaluationStepStage, &p)
	if err != nil {
		t.Fatal(err)
	}
	job.UID = "review-job-uid"
	if err := r.Client.Update(ctx, job); err != nil {
		t.Fatal(err)
	}
	beforeStage := stage.DeepCopy()
	now := metav1.NewTime(fixtureTime)
	stage.Status = api.EvaluationStageStatus{ObservedGeneration: stage.Generation, StartedAt: &now, CurrentProcedure: api.EvaluationStepStage, JobRef: ref(job), GoRef: &api.EvaluationEvidenceReference{Store: parent.Spec.EvidenceStore, Key: goKey, Size: int64(len(store[goKey]))}, Procedures: []api.EvaluationProcedure{{Step: api.EvaluationStepFactor, FinishedAt: &now}, {Step: api.EvaluationStepStage, StartedAt: &now, JobRef: ref(job)}}}
	condition(&stage.Status.Conditions, stage.Generation, api.EvaluationConditionExecutionComplete, metav1.ConditionUnknown, api.EvaluationReasonNotStarted, "")
	if err := r.patchStatus(ctx, stage, beforeStage); err != nil {
		t.Fatal(err)
	}
	r.HTTP = func(_ context.Context, _ string, _ map[string]string, path string, value any) error {
		body := `{"from":"2026-11-01T11:59:59Z","to":"2026-11-01T12:00:00Z","workloads":[{"uid":"workload-uid","instanceAssignments":{"instance":1}}]}`
		if strings.Contains(path, "/settings") {
			body = `{"observationWindowNanos":1000000000}`
		}
		if strings.Contains(path, "/state") {
			body = `{"at":"2026-11-01T12:00:00Z","nodes":[{"node":{"name":"node","uid":"node-uid"},"ready":true}],"instances":[{"id":"instance","workloadUID":"workload-uid","ownerWorkloadUID":"workload-uid","ready":true,"podName":"serving","podUID":"pod-uid","node":{"name":"node","uid":"node-uid"}}],"routing":{"workload-uid":{"instance":1}}}`
		}
		return json.Unmarshal([]byte(body), value)
	}
	return r, store, parent, stage
}

func TestReviewCompletedStageNeverInjects(t *testing.T) {
	r, store, parent, stage := fixtureStageExecution(t)
	resultKey := evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/stage/result.json")
	store.put(resultKey, map[string]any{"schemaVersion": "evaluation-step.v1alpha1", "runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "step": api.EvaluationStepStage, "execution": map[string]string{"status": complete}, "startedAt": fixtureTime.Add(-time.Minute), "finishedAt": fixtureTime.Add(-time.Second)})
	reads := 0
	r.HTTP = func(context.Context, string, map[string]string, string, any) error {
		reads++
		return errors.New("completed stage must not select a target")
	}
	if _, err := (&StageReconciler{Runtime: r}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stage)}); err != nil {
		t.Fatal(err)
	}
	saved := &api.EvaluationStage{}
	if err := r.APIReader.Get(context.Background(), client.ObjectKeyFromObject(stage), saved); err != nil {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Errorf("completed stage made %d target-selection reads", reads)
	}
	if p := findProcedure(saved.Status.Procedures, api.EvaluationStepStage); p == nil || p.EvidenceRef == nil {
		t.Error("completion evidence not consumed first")
	}
	if len(saved.Status.InjectionRecords) != 1 || saved.Status.InjectionRecords[0].Outcome != injectionInvalid || saved.Status.InjectionRecords[0].RequestedAt != nil || saved.Status.MeasurementVerdict != api.MeasurementINVALID {
		t.Errorf("missing cancelled-plan evidence: %+v", saved.Status)
	}
}

type readFaultStore struct {
	ObjectStore
	Key string
	Err error
}

func (s readFaultStore) OpenLocation(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	if location.Key == s.Key {
		return nil, s.Err
	}
	return s.ObjectStore.OpenLocation(ctx, location)
}

func TestReviewPreMutationErrorsRequeue(t *testing.T) {
	for _, fault := range []string{"GO-read", "observation-read", "intent-status-conflict", "pod-read"} {
		t.Run(fault, func(t *testing.T) {
			r, store, parent, stage := fixtureStageExecution(t)
			transient := errors.New("transient read failure")
			switch fault {
			case "GO-read":
				r.Store = func(context.Context, string, string, map[string]string) (ObjectStore, error) {
					return readFaultStore{ObjectStore: store, Key: stage.Status.GoRef.Key, Err: transient}, nil
				}
			case "observation-read":
				r.HTTP = func(context.Context, string, map[string]string, string, any) error { return transient }
			case "intent-status-conflict":
				r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{SubResourcePatch: func(ctx context.Context, c client.Client, subresource string, obj client.Object, patch client.Patch, options ...client.SubResourcePatchOption) error {
					child, ok := obj.(*api.EvaluationStage)
					if ok && len(child.Status.InjectionRecords) > 0 {
						return apierrors.NewConflict(schema.GroupResource{Group: "edge.dacs.io", Resource: "evaluationstages"}, child.Name, transient)
					}
					return c.SubResource(subresource).Patch(ctx, obj, patch, options...)
				}})
			case "pod-read":
				r.APIReader = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, options ...client.GetOption) error {
					if _, ok := obj.(*corev1.Pod); ok {
						return transient
					}
					return c.Get(ctx, key, obj, options...)
				}})
			}
			result, err := (&StageReconciler{Runtime: r}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stage)})
			if err == nil && result.RequeueAfter == 0 {
				t.Error("transient error must requeue")
			}
			saved := &api.EvaluationStage{}
			if getErr := r.Client.Get(context.Background(), client.ObjectKeyFromObject(stage), saved); getErr != nil {
				t.Fatal(getErr)
			}
			if meta.IsStatusConditionFalse(saved.Status.Conditions, api.EvaluationConditionExecutionComplete) || saved.Status.MeasurementVerdict == api.MeasurementINVALID || len(saved.Status.InjectionRecords) > 0 {
				t.Errorf("read/status error became a terminal effect: %+v", saved.Status)
			}
			node := &corev1.Node{}
			if getErr := r.Client.Get(context.Background(), client.ObjectKey{Name: "node"}, node); getErr != nil {
				t.Fatal(getErr)
			}
			if node.Spec.Unschedulable {
				t.Error("unexpected cordon")
			}
			if parent.Status.OccupyingRunUID != parent.UID {
				t.Fatal("fixture lost occupancy")
			}
		})
	}
}

func TestReviewLargeSnapshotStaysAReference(t *testing.T) {
	parent := fixtureRun("snapshot")
	startRun(parent)
	parent.Status.Workspace = nil
	parent.Status.InputSnapshotRef = &api.EvaluationEvidenceReference{Store: parent.Spec.EvidenceStore, Key: "large-snapshot", Size: 52 << 20}
	r, store := fixtureRuntime(t, parent)
	r.Store = func(context.Context, string, string, map[string]string) (ObjectStore, error) {
		return readFaultStore{ObjectStore: store, Key: "large-snapshot", Err: errors.New("controller must not load a large snapshot")}, nil
	}
	if err := (&RunReconciler{Runtime: r}).workspace(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	cm := &corev1.ConfigMap{}
	if err := r.APIReader.Get(context.Background(), client.ObjectKey{Namespace: parent.Namespace, Name: parent.Status.InputConfigMapRef.Name}, cm); err != nil {
		t.Fatal(err)
	}
	var reference api.EvaluationEvidenceReference
	if err := json.Unmarshal([]byte(cm.Data["snapshot-ref.json"]), &reference); err != nil {
		t.Fatal(err)
	}
	if reference != *parent.Status.InputSnapshotRef || cm.Immutable == nil || !*cm.Immutable {
		t.Fatalf("input must be an immutable reference: %+v", cm)
	}
}

func TestProjectedServiceAccountTokenBudget(t *testing.T) {
	for _, step := range []string{api.EvaluationStepPrepare, api.EvaluationStepPublish} {
		t.Run(step, func(t *testing.T) {
			parent := fixtureRun("token")
			startRun(parent)
			r, _ := fixtureRuntime(t, parent)
			job, err := r.job(context.Background(), parent, parent, nil, step, &api.EvaluationProcedure{Step: step})
			if err != nil {
				t.Fatal(err)
			}
			pod := job.Spec.Template.Spec
			if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
				t.Fatal("implicit service account token must be disabled")
			}
			var projected *corev1.ProjectedVolumeSource
			for _, volume := range pod.Volumes {
				if volume.Name == "api-access" {
					projected = volume.Projected
				}
			}
			if step == api.EvaluationStepPublish {
				if projected != nil {
					t.Fatal("publication needs no Kubernetes token")
				}
				return
			}
			if projected == nil || len(projected.Sources) != 3 {
				t.Fatal("missing token, CA and namespace projections")
			}
			token := projected.Sources[0].ServiceAccountToken
			if token == nil || token.ExpirationSeconds == nil || *token.ExpirationSeconds != *job.Spec.ActiveDeadlineSeconds+60 || token.Path != "token" || token.Audience != "" {
				t.Fatalf("incorrect bounded default-audience token: %+v", token)
			}
			assertInClusterFiles(t, projected)
			mounted := false
			for _, mount := range pod.Containers[0].VolumeMounts {
				if mount.Name == "api-access" && mount.MountPath == "/var/run/secrets/kubernetes.io/serviceaccount" && mount.ReadOnly {
					mounted = true
				}
			}
			if !mounted {
				t.Fatal("token rotation must use the standard projected mount")
			}
			token.ExpirationSeconds = ptr(*token.ExpirationSeconds + 1)
			if err := r.Client.Update(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			if _, err := r.job(context.Background(), parent, parent, nil, step, &api.EvaluationProcedure{Step: step}); err == nil {
				t.Fatal("adopted Job must retain its frozen token budget")
			}
		})
	}
	config := fixtureConfig().Data
	config["jobActiveDeadlineSeconds"] = "539"
	if err := validateConfig(config); err == nil {
		t.Fatal("projected token expiration below Kubernetes minimum must be rejected before freezing")
	}
}

func assertInClusterFiles(t *testing.T, projected *corev1.ProjectedVolumeSource) {
	t.Helper()
	ca := projected.Sources[1].ConfigMap
	if ca == nil || ca.Name != "kube-root-ca.crt" || ca.Items[0].Key != "ca.crt" || ca.Items[0].Path != "ca.crt" {
		t.Fatal("missing in-cluster CA projection")
	}
	namespace := projected.Sources[2].DownwardAPI
	if namespace == nil || namespace.Items[0].Path != "namespace" || namespace.Items[0].FieldRef.FieldPath != "metadata.namespace" {
		t.Fatal("missing namespace projection")
	}
}

func TestCompletedStageRecoversRequestedInjection(t *testing.T) {
	for _, test := range []struct {
		name, outcome          string
		missingGO, readFailure bool
	}{
		{name: "stored-complete", outcome: complete},
		{name: "stored-unknown", outcome: injectionUnknown},
		{name: "missing-result", outcome: injectionUnknown},
		{name: "missing-GO-stored-complete", outcome: complete, missingGO: true},
		{name: "store-read-requeues", readFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			r, store, parent, stage := fixtureStageExecution(t)
			goAt := metav1.NewTime(fixtureTime.Add(-2 * time.Minute))
			requestedAt := metav1.NewTime(fixtureTime.Add(-time.Minute))
			before := stage.DeepCopy()
			stage.Status.InjectionRecords = []api.FaultInjectionRecord{{ID: "fault", ScheduledAt: goAt, RequestedAt: &requestedAt, Outcome: injectionRequested, PodRef: &api.EvaluationObjectReference{Name: "serving", UID: "pod-uid"}, NodeRef: &api.EvaluationObjectReference{Name: "node", UID: "node-uid"}}}
			store.put(stage.Status.GoRef.Key, map[string]any{"runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "goUTC": goAt})
			if test.missingGO {
				stage.Status.GoRef = nil
			}
			if err := r.patchStatus(ctx, stage, before); err != nil {
				t.Fatal(err)
			}
			key := evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/injections/fault.json")
			if strings.Contains(test.name, "stored") {
				record := stage.Status.InjectionRecords[0]
				record.Outcome = test.outcome
				if test.outcome == injectionUnknown {
					record.Reason = api.EvaluationReasonRemoteEffectUnknown
				}
				store.put(key, injectionEvidence{RunUID: parent.UID, StageUID: stage.UID, StageID: stage.Spec.StageID, Record: record})
			}
			if test.readFailure {
				r.Store = func(context.Context, string, string, map[string]string) (ObjectStore, error) {
					return readFaultStore{ObjectStore: store, Key: key, Err: errors.New("temporary result read failure")}, nil
				}
			}
			store.put(evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/stage/result.json"), map[string]any{"schemaVersion": "evaluation-step.v1alpha1", "runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "step": api.EvaluationStepStage, "execution": map[string]string{"status": complete}, "startedAt": goAt, "finishedAt": fixtureTime.Add(-time.Second)})
			r.HTTP = func(context.Context, string, map[string]string, string, any) error {
				t.Fatal("completion recovery must not select or inject")
				return nil
			}
			_, err := (&StageReconciler{Runtime: r}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stage)})
			if test.readFailure {
				if err == nil {
					t.Fatal("unreadable requested-result evidence must requeue")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			saved := &api.EvaluationStage{}
			if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(stage), saved); err != nil {
				t.Fatal(err)
			}
			record := saved.Status.InjectionRecords[0]
			if record.Outcome != test.outcome || record.EvidenceRef == nil || record.EvidenceRef.Key != key {
				t.Fatalf("requested effect not resolved: %+v", record)
			}
			if uncertainStage(saved) != (test.outcome == injectionUnknown) {
				t.Fatalf("incorrect occupancy uncertainty: %+v", saved.Status)
			}
			if test.outcome == injectionUnknown && !meta.IsStatusConditionFalse(saved.Status.Conditions, api.EvaluationConditionExecutionComplete) {
				t.Fatal("unknown request must stop execution and retain occupancy")
			}
		})
	}
}

func TestParentJobsReceivePrepareOutcome(t *testing.T) {
	for _, test := range []struct {
		name, status string
		snapshot     bool
	}{
		{name: "prepare-not-completed"},
		{name: "prepare-failed", status: failed},
		{name: "prepare-complete-snapshot-lost", status: complete},
		{name: "prepare-complete", status: complete, snapshot: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			parent := fixtureRun("context")
			startRun(parent)
			snapshot := parent.Status.InputSnapshotRef
			parent.Status.InputSnapshotRef = nil
			r, store := fixtureRuntime(t, parent)
			prepareJob, err := r.job(ctx, parent, parent, nil, api.EvaluationStepPrepare, &api.EvaluationProcedure{Step: api.EvaluationStepPrepare})
			if err != nil {
				t.Fatal(err)
			}
			prepareCM := prepareJob.Spec.Template.Spec.Volumes[1].ConfigMap.Name
			if test.status != "" {
				key := evidenceKey(parent.UID, "run/prepare/result.json")
				store.put(key, map[string]any{"schemaVersion": "evaluation-step.v1alpha1", "runUID": parent.UID, "step": api.EvaluationStepPrepare, "execution": map[string]string{"status": test.status}, "startedAt": fixtureTime, "finishedAt": fixtureTime})
				at := metav1.NewTime(fixtureTime)
				parent.Status.Procedures = []api.EvaluationProcedure{{Step: api.EvaluationStepPrepare, FinishedAt: &at, EvidenceRef: &api.EvaluationEvidenceReference{Store: parent.Spec.EvidenceStore, Key: key, Size: int64(len(store[key]))}}}
			}
			if test.snapshot {
				parent.Status.InputSnapshotRef = snapshot
			}
			for _, step := range []string{api.EvaluationStepFinish, api.EvaluationStepPublish} {
				job, err := r.job(ctx, parent, parent, nil, step, &api.EvaluationProcedure{Step: step})
				if err != nil {
					t.Fatal(err)
				}
				name := job.Spec.Template.Spec.Volumes[1].ConfigMap.Name
				if name == prepareCM {
					t.Fatal("parent outcome must not mutate prepare's immutable ConfigMap")
				}
				cm := &corev1.ConfigMap{}
				if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: parent.Namespace, Name: name}, cm); err != nil {
					t.Fatal(err)
				}
				assertParentExecutionContext(t, cm.Data["execution.json"], test.status == complete, parent.Status.InputSnapshotRef)
			}
		})
	}
}

func assertParentExecutionContext(t *testing.T, body string, completed bool, reference *api.EvaluationEvidenceReference) {
	t.Helper()
	var execution struct {
		PrepareCompleted *bool                            `json:"prepareCompleted"`
		InputSnapshotRef *api.EvaluationEvidenceReference `json:"inputSnapshotRef"`
	}
	if err := json.Unmarshal([]byte(body), &execution); err != nil {
		t.Fatal(err)
	}
	if execution.PrepareCompleted == nil || *execution.PrepareCompleted != completed {
		t.Fatalf("prepare completion is missing or incorrect: %s", body)
	}
	if (reference == nil) != (execution.InputSnapshotRef == nil) {
		t.Fatalf("snapshot omission is incorrect: %s", body)
	}
	if reference != nil && *reference != *execution.InputSnapshotRef {
		t.Fatalf("snapshot identity differs: %s", body)
	}
}

func TestParentJobsReceiveOnlyCreatedChildOutcomes(t *testing.T) {
	for _, scenario := range []string{"complete", "cleanup-failed", "remote-effect", "execution-lost", "injection-unknown", "missing", "replacement", "empty", "read-error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			parent := fixtureRun("children")
			startRun(parent)
			uncreated := parent.Spec.Stages[0]
			uncreated.ID = "not-created"
			parent.Spec.Stages = append(parent.Spec.Stages, uncreated)
			r, _ := fixtureRuntime(t, parent)
			child := fixtureStage(t, &r, parent)
			executionStatus, executionReason, cleanupStatus, cleanupReason, uncertain := complete, complete, complete, complete, false
			if scenario == "cleanup-failed" {
				cleanupStatus, cleanupReason = failed, api.EvaluationReasonResidualState
			}
			if scenario == "remote-effect" {
				executionStatus, executionReason, uncertain = failed, api.EvaluationReasonRemoteEffectUnknown, true
			}
			if scenario == "execution-lost" {
				executionStatus, executionReason, uncertain = failed, api.EvaluationReasonExecutionLost, true
			}
			before := child.DeepCopy()
			execCondition, cleanupCondition := metav1.ConditionTrue, metav1.ConditionTrue
			if executionStatus != complete {
				execCondition = metav1.ConditionFalse
			}
			if cleanupStatus != complete {
				cleanupCondition = metav1.ConditionFalse
			}
			condition(&child.Status.Conditions, child.Generation, api.EvaluationConditionExecutionComplete, execCondition, executionReason, "")
			condition(&child.Status.Conditions, child.Generation, api.EvaluationConditionStageCleanupComplete, cleanupCondition, cleanupReason, "")
			if scenario == "injection-unknown" {
				child.Status.InjectionRecords = []api.FaultInjectionRecord{{ID: "fault", Outcome: injectionUnknown, Reason: api.EvaluationReasonRemoteEffectUnknown}}
				uncertain = true
			}
			if err := r.patchStatus(ctx, child, before); err != nil {
				t.Fatal(err)
			}
			parent.Status.ChildRefs = []api.EvaluationChildReference{{StageID: child.Spec.StageID, EvaluationObjectReference: *ref(child)}}
			if scenario == "empty" {
				parent.Status.ChildRefs = nil
			}
			if scenario == "missing" || scenario == "replacement" {
				if err := r.Client.Delete(ctx, child); err != nil {
					t.Fatal(err)
				}
				if scenario == "replacement" {
					child.UID = "replacement-uid"
					child.ResourceVersion = ""
					if err := r.Client.Create(ctx, child); err != nil {
						t.Fatal(err)
					}
				}
				executionStatus, executionReason, cleanupStatus, cleanupReason, uncertain = failed, api.EvaluationReasonExecutionLost, failed, api.EvaluationReasonExecutionLost, true
			}
			if scenario == "read-error" {
				r.APIReader = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, options ...client.GetOption) error {
					if _, ok := obj.(*api.EvaluationStage); ok {
						return errors.New("temporary child observation failure")
					}
					return c.Get(ctx, key, obj, options...)
				}})
			}
			for _, step := range []string{api.EvaluationStepFinish, api.EvaluationStepPublish} {
				job, err := r.job(ctx, parent, parent, nil, step, &api.EvaluationProcedure{Step: step})
				if scenario == "read-error" {
					if err == nil {
						t.Fatal("child observation failure must requeue parent Job creation")
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				cm := &corev1.ConfigMap{}
				name := job.Spec.Template.Spec.Volumes[1].ConfigMap.Name
				if err := r.APIReader.Get(ctx, client.ObjectKey{Namespace: parent.Namespace, Name: name}, cm); err != nil {
					t.Fatal(err)
				}
				assertCreatedChildContext(t, cm.Data["execution.json"], parent.Status.ChildRefs, executionStatus, executionReason, cleanupStatus, cleanupReason, uncertain)
			}
		})
	}
}

func assertCreatedChildContext(t *testing.T, body string, expected []api.EvaluationChildReference, executionStatus, executionReason, cleanupStatus, cleanupReason string, uncertain bool) {
	t.Helper()
	var execution struct {
		ChildRefs []struct {
			api.EvaluationChildReference
			Execution struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"execution"`
			Cleanup struct {
				Status string `json:"status"`
				Reason string `json:"reason"`
			} `json:"cleanup"`
			Uncertain bool `json:"uncertain"`
		} `json:"childRefs"`
	}
	if err := json.Unmarshal([]byte(body), &execution); err != nil {
		t.Fatal(err)
	}
	if execution.ChildRefs == nil || len(execution.ChildRefs) != len(expected) {
		t.Fatalf("childRefs must list only recorded created children (including explicit []): %s", body)
	}
	if len(expected) == 0 {
		return
	}
	child := execution.ChildRefs[0]
	if child.EvaluationChildReference != expected[0] || child.Execution.Status != executionStatus || child.Execution.Reason != executionReason || child.Cleanup.Status != cleanupStatus || child.Cleanup.Reason != cleanupReason || child.Uncertain != uncertain {
		t.Fatalf("incorrect child cleanup/uncertainty context: %s", body)
	}
}
