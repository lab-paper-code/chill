package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type memoryStore map[string][]byte

func (s memoryStore) OpenLocation(_ context.Context, l evidencestore.ObjectLocation) (io.ReadCloser, error) {
	b, ok := s[l.Key]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (s memoryStore) PutImmutable(_ context.Context, id evidencestore.ObjectIdentity, r io.Reader) (evidencestore.Receipt, error) {
	if _, ok := s[id.Key]; ok {
		return evidencestore.Receipt{}, errors.New("immutable conflict")
	}
	b, e := io.ReadAll(r)
	if e != nil {
		return evidencestore.Receipt{}, e
	}
	s[id.Key] = b
	return evidencestore.Receipt{Object: id}, nil
}
func (s memoryStore) put(key string, v any) { s[key], _ = json.Marshal(v) }

var fixtureTime = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func fixtureRun(name string) *api.EvaluationRun {
	image := api.EvaluationImage{Image: "harness@sha256:identity", SourceSHA: "source"}
	return &api.EvaluationRun{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "system", UID: types.UID(name + "-uid"), Generation: 1, CreationTimestamp: metav1.NewTime(fixtureTime), Finalizers: []string{api.EvaluationRunFinalizer}},
		Spec: api.EvaluationRunSpec{
			Question: "question", EvidenceStore: "evidence", Harness: image,
			Target:           api.TargetIdentity{Components: []api.EvaluationTargetComponent{{Name: "server", Resource: api.EvaluationResourceReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "system", Name: "server"}}}},
			LoadGenerator:    api.Generator{EvaluationImage: image, NodeName: "genesis1", Shards: map[string]int32{"w": 1}, Barrier: api.GeneratorBarrier{MinimumReadySamples: 1}},
			PowerMeasurement: api.PowerMeasurement{Meters: []api.EvaluationMeter{{ID: "m", SourceIdentity: "source"}}, Nodes: []api.EvaluationObjectReference{{Name: "node", UID: "node-uid"}}, Series: "power", Sampling: api.PowerSampling{IntervalNanos: 1}},
			Judgment:         api.Judgment{EnergyCoverage: api.EnergyCoverage{MaxSingleGapSeconds: 1}},
			Stages:           []api.StageTemplate{{ID: "first", Row: "E5-3m", LoadScenarioRef: api.EvaluationObjectReference{Name: "load", UID: "load-uid"}}},
		},
		Status: api.EvaluationRunStatus{ObservedGeneration: 1},
	}
}
func fixtureConfig() *corev1.ConfigMap {
	return &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: "system", UID: "config-uid"}, Data: map[string]string{"actionServiceAccount": "action", "publishServiceAccount": "publish", "caSecret": "ca", "evidenceCredentialsSecret": "credentials", "evidenceEndpoint": "http://store", "evidenceBucket": "bucket", "evidenceRegion": "region", "storageClass": "rwx", "workspaceSize": "400Gi", "chillAPIURL": "https://chill", "jobActiveDeadlineSeconds": "3600", "injectionWindowSeconds": "5", "injectionSnapshotTTLSeconds": "15"}}
}
func fixtureRuntime(t *testing.T, objects ...client.Object) (Runtime, memoryStore) {
	t.Helper()
	scheme := runtime.NewScheme()
	if e := api.AddToScheme(scheme); e != nil {
		t.Fatal(e)
	}
	if e := corev1.AddToScheme(scheme); e != nil {
		t.Fatal(e)
	}
	if e := batchv1.AddToScheme(scheme); e != nil {
		t.Fatal(e)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&api.EvaluationRun{}, &api.EvaluationStage{}).WithObjects(objects...).Build()
	store := memoryStore{}
	return Runtime{Client: c, APIReader: c, Scheme: scheme, Now: func() time.Time { return fixtureTime }, Store: func(context.Context, string, string, map[string]string) (ObjectStore, error) { return store, nil }}, store
}
func startRun(run *api.EvaluationRun) {
	now := metav1.NewTime(fixtureTime)
	run.Status.StartedAt = &now
	run.Status.OccupyingRunUID = run.UID
	run.Status.FrozenConfig = &api.EvaluationFrozenConfig{Values: fixtureConfig().Data}
	run.Status.Workspace = &api.EvaluationObjectReference{Name: "work", UID: "work-uid"}
	run.Status.FrozenConfigMapRef = &api.EvaluationObjectReference{Name: "config", UID: "config-uid"}
	run.Status.InputConfigMapRef = &api.EvaluationObjectReference{Name: "input", UID: "input-uid"}
	run.Status.InputSnapshotRef = &api.EvaluationEvidenceReference{Store: "evidence", Key: "snapshot", Size: 1}
}
func fixtureStage(t *testing.T, r *Runtime, parent *api.EvaluationRun) *api.EvaluationStage {
	t.Helper()
	child, e := (&RunReconciler{Runtime: *r}).ensureChild(context.Background(), parent, 0)
	if e != nil {
		t.Fatal(e)
	}
	child.UID = "child-uid"
	if e = r.Client.Update(context.Background(), child); e != nil {
		t.Fatal(e)
	}
	return child
}
func TestPoolExclusionAndFrozenConfig(t *testing.T) {
	ctx := context.Background()
	first, second := fixtureRun("a"), fixtureRun("b")
	cm := fixtureConfig()
	r, _ := fixtureRuntime(t, first, second, cm)
	reconciler := RunReconciler{Runtime: r}
	if _, e := reconciler.acquire(ctx, second); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(second.Status.Conditions[0].Reason, "PoolBusy") {
		t.Fatal("second must wait")
	}
	if _, e := reconciler.acquire(ctx, first); e != nil {
		t.Fatal(e)
	}
	current := &corev1.ConfigMap{}
	if e := r.Client.Get(ctx, client.ObjectKeyFromObject(cm), current); e != nil {
		t.Fatal(e)
	}
	current.Data["jobActiveDeadlineSeconds"] = "999"
	if e := r.Client.Update(ctx, current); e != nil {
		t.Fatal(e)
	}
	saved := &api.EvaluationRun{}
	if e := r.APIReader.Get(ctx, client.ObjectKeyFromObject(first), saved); e != nil {
		t.Fatal(e)
	}
	if saved.Status.FrozenConfig.Values["jobActiveDeadlineSeconds"] != "3600" {
		t.Fatal("frozen config changed")
	}
	before := saved.DeepCopy()
	now := metav1.NewTime(fixtureTime)
	saved.Status.FinishedAt = &now
	condition(&saved.Status.Conditions, 1, api.EvaluationConditionCleanupComplete, metav1.ConditionFalse, api.EvaluationReasonReturnTimeout, "")
	if e := r.patchStatus(ctx, saved, before); e != nil {
		t.Fatal(e)
	}
	if _, e := reconciler.acquire(ctx, second); e != nil {
		t.Fatal(e)
	}
	if second.Status.StartedAt != nil || second.Status.OccupyingRunUID != first.UID {
		t.Fatal("return-unconfirmed run must retain occupancy")
	}
}
func TestChildCreationResponseLossAdoption(t *testing.T) {
	parent := fixtureRun("a")
	startRun(parent)
	r, _ := fixtureRuntime(t, parent)
	created := false
	base := r.Client
	r.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, o client.Object, options ...client.CreateOption) error {
		if _, ok := o.(*api.EvaluationStage); ok && !created {
			created = true
			o.SetUID("child-uid")
			if e := c.Create(ctx, o, options...); e != nil {
				return e
			}
			return errors.New("response lost")
		}
		return c.Create(ctx, o, options...)
	}})
	reconciler := RunReconciler{Runtime: r}
	if _, e := reconciler.ensureChild(context.Background(), parent, 0); e == nil {
		t.Fatal("expected response loss")
	}
	child, e := reconciler.ensureChild(context.Background(), parent, 0)
	if e != nil || child.UID != "child-uid" {
		t.Fatalf("adoption=%v %v", child, e)
	}
}
func TestStageParentBoundaries(t *testing.T) {
	for _, recorded := range []bool{false, true} {
		t.Run(map[bool]string{false: "standalone", true: "parent-not-recorded"}[recorded], func(t *testing.T) {
			parent := fixtureRun("a")
			startRun(parent)
			stage := &api.EvaluationStage{ObjectMeta: metav1.ObjectMeta{Name: "child", Namespace: "system", UID: "child-uid", Generation: 1}, Spec: api.EvaluationStageSpec{StageID: "first"}}
			r, _ := fixtureRuntime(t, parent)
			if recorded {
				if e := controllerutil.SetControllerReference(parent, stage, r.Scheme); e != nil {
					t.Fatal(e)
				}
			}
			if e := r.Client.Create(context.Background(), stage); e != nil {
				t.Fatal(e)
			}
			result, e := (&StageReconciler{Runtime: r}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(stage)})
			if e != nil {
				t.Fatal(e)
			}
			saved := &api.EvaluationStage{}
			if e = r.APIReader.Get(context.Background(), client.ObjectKeyFromObject(stage), saved); e != nil {
				t.Fatal(e)
			}
			if recorded {
				if result.RequeueAfter == 0 || saved.Status.FinishedAt != nil {
					t.Fatal("unrecorded child must wait")
				}
			} else if saved.Status.FinishedAt == nil || saved.Status.Conditions[0].Reason != api.EvaluationReasonParentRequired {
				t.Fatal("standalone stage must terminate ParentRequired")
			}
		})
	}
}
func TestRecordedJobLossNeverRecreates(t *testing.T) {
	parent := fixtureRun("a")
	startRun(parent)
	r, _ := fixtureRuntime(t, parent)
	p := api.EvaluationProcedure{Step: api.EvaluationStepPrepare, JobRef: &api.EvaluationObjectReference{Name: uidName(parent.UID) + "-prepare", UID: "gone"}}
	_, _, e := r.advance(context.Background(), parent, nil, api.EvaluationStepPrepare, &p)
	if !errors.Is(e, errExecutionLost) {
		t.Fatalf("got %v", e)
	}
	jobs := &batchv1.JobList{}
	if e = r.APIReader.List(context.Background(), jobs); e != nil {
		t.Fatal(e)
	}
	if len(jobs.Items) != 0 {
		t.Fatal("lost Job recreated")
	}
}
func TestMissedInjectionWindowInvalid(t *testing.T) {
	parent := fixtureRun("a")
	startRun(parent)
	r, store := fixtureRuntime(t, parent)
	stage := fixtureStage(t, &r, parent)
	stage.Spec.FailureInjection = []api.FaultInjectionPlan{{ID: "fault", OffsetSeconds: 0, Action: "delete-serving-pod"}}
	if err := r.Client.Update(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	store.put(evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/go.json"), map[string]any{"runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "goUTC": fixtureTime.Add(-time.Minute)})
	e := r.injections(context.Background(), parent, stage)
	if !errors.Is(e, errInjectionUpdated) {
		t.Fatal(e)
	}
	e = r.injections(context.Background(), parent, stage)
	if !errors.Is(e, errInjectionUpdated) {
		t.Fatal(e)
	}
	if stage.Status.MeasurementVerdict != api.MeasurementINVALID || stage.Status.InjectionRecords[0].Reason != api.EvaluationReasonInjectionInvalid {
		t.Fatal("late injection must be invalid")
	}
	if stage.Status.InjectionRecords[0].RequestedAt != nil {
		t.Fatal("late effect issued")
	}
}

func TestGOTimeRecordedOnce(t *testing.T) {
	parent := fixtureRun("a")
	startRun(parent)
	r, store := fixtureRuntime(t, parent)
	stage := fixtureStage(t, &r, parent)
	key := evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/go.json")
	store.put(key, map[string]any{
		"runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "goUTC": fixtureTime,
	})
	if err := r.injections(context.Background(), parent, stage); !errors.Is(err, errInjectionUpdated) {
		t.Fatal(err)
	}
	if stage.Status.GoAt == nil || !stage.Status.GoAt.Time.Equal(fixtureTime) {
		t.Fatalf("GO time not recorded: %v", stage.Status.GoAt)
	}
	store.put(key, map[string]any{"goUTC": fixtureTime.Add(time.Minute)})
	if err := r.injections(context.Background(), parent, stage); err != nil {
		t.Fatal(err)
	}
	if !stage.Status.GoAt.Time.Equal(fixtureTime) {
		t.Fatal("GO time overwritten")
	}
}
func TestCleanupFailureReleasePreservesFailure(t *testing.T) {
	parent := fixtureRun("a")
	startRun(parent)
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionPoolAcquired, metav1.ConditionTrue, "Acquired", "")
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionExecutionComplete, metav1.ConditionTrue, complete, "")
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionCleanupComplete, metav1.ConditionFalse, api.EvaluationReasonReturnTimeout, "")
	writer := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: parent.Namespace, Labels: map[string]string{api.EvaluationLabelRunUID: string(parent.UID)}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}}
	r, _ := fixtureRuntime(t, parent, writer)
	if !occupied(parent) {
		t.Fatal("cleanup failure released pool")
	}
	annotation := `{"reason":"operator confirmed controls reverted and writers stopped","at":"2026-11-01T12:00:00Z","by":"operator"}`
	if result, err := (&RunReconciler{Runtime: r}).release(context.Background(), parent, annotation); err != nil || result.RequeueAfter == 0 || !occupied(parent) || len(parent.Status.ReleaseRecords) != 0 {
		t.Fatalf("release must wait for evaluation writer termination: result=%v err=%v", result, err)
	}
	if err := r.Client.Delete(context.Background(), writer); err != nil {
		t.Fatal(err)
	}
	if _, e := (&RunReconciler{Runtime: r}).release(context.Background(), parent, annotation); e != nil {
		t.Fatal(e)
	}
	if occupied(parent) || isTrue(parent.Status.Conditions, api.EvaluationConditionCleanupComplete) || len(parent.Status.ReleaseRecords) != 1 {
		t.Fatal("release must preserve cleanup failure")
	}
}

func TestReleaseIgnoredWhileExecutionContinues(t *testing.T) {
	parent := fixtureRun("running")
	startRun(parent)
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionPoolAcquired, metav1.ConditionTrue, "Acquired", "")
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionExecutionComplete, metav1.ConditionUnknown, "Running", "")
	condition(&parent.Status.Conditions, 1, api.EvaluationConditionCleanupComplete, metav1.ConditionFalse, "NotReturned", "")
	parent.Status.Workspace = nil
	parent.Annotations = map[string]string{api.EvaluationReleaseAnnotation: `{"reason":"early"}`}
	r, store := fixtureRuntime(t, parent)
	if _, err := (&RunReconciler{Runtime: r}).Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}); err != nil {
		t.Fatal(err)
	}
	saved := &api.EvaluationRun{}
	if err := r.APIReader.Get(context.Background(), client.ObjectKeyFromObject(parent), saved); err != nil {
		t.Fatal(err)
	}
	if !occupied(saved) || len(saved.Status.ReleaseRecords) != 0 || len(store) != 0 {
		t.Fatal("premature release changed occupancy or evidence")
	}
}

func TestRunStageStoredResultWorkflow(t *testing.T) {
	ctx := context.Background()
	parent := fixtureRun("workflow")
	parent.Status = api.EvaluationRunStatus{}
	r, store := fixtureRuntime(t, parent, fixtureConfig())
	snapshotKey := evidenceKey(parent.UID, "run/prepare/input-snapshot.json")
	store.put(snapshotKey, map[string]any{"schema": "evaluation-inputs.v1alpha1", "runUID": parent.UID})
	base := r.Client
	ordinal := 0
	r.Client = interceptor.NewClient(base.(client.WithWatch), interceptor.Funcs{Create: func(ctx context.Context, c client.WithWatch, object client.Object, options ...client.CreateOption) error {
		ordinal++
		object.SetUID(types.UID(object.GetName() + "-uid"))
		if err := c.Create(ctx, object, options...); err != nil {
			return err
		}
		job, ok := object.(*batchv1.Job)
		if !ok {
			return nil
		}
		if job.Spec.Template.Spec.SecurityContext == nil || job.Spec.Template.Spec.SecurityContext.FSGroup == nil || *job.Spec.Template.Spec.SecurityContext.FSGroup != 65532 {
			t.Fatal("non-root harness requires workspace FSGroup 65532")
		}
		podSpec := job.Spec.Template.Spec
		if len(podSpec.Volumes) < 4 || podSpec.Volumes[3].EmptyDir == nil || podSpec.Volumes[3].EmptyDir.Medium != corev1.StorageMediumMemory {
			t.Fatal("private credentials require memory emptyDir separate from evidence staging")
		}
		if podSpec.Containers[0].VolumeMounts[3].MountPath != "/private" {
			t.Fatal("private credential volume must mount at /private")
		}
		assertHarnessWritesOnlyMounts(t, podSpec)
		step := job.Labels[api.EvaluationLabelStep]
		stageID := job.Labels[api.EvaluationLabelStage]
		result := map[string]any{"schemaVersion": "evaluation-step.v1alpha1", "runUID": parent.UID, "step": step, "execution": map[string]string{"status": complete}, "startedAt": fixtureTime, "finishedAt": fixtureTime}
		if stageID == scopeRun {
			stageID = ""
		} else {
			owner := metav1.GetControllerOf(job)
			result["stageUID"] = owner.UID
			result["stageID"] = stageID
		}
		switch step {
		case api.EvaluationStepPrepare:
			result["outputs"] = map[string]any{"inputSnapshot": api.EvaluationEvidenceReference{Store: parent.Spec.EvidenceStore, Key: snapshotKey, Size: int64(len(store[snapshotKey]))}}
		case api.EvaluationStepJudge:
			result["measurement"] = map[string]string{"verdict": string(api.MeasurementPASS)}
		case api.EvaluationStepCleanupJobs, api.EvaluationStepFinish:
			result["cleanup"] = map[string]string{"status": complete}
		case api.EvaluationStepPublish:
			result["outputs"] = map[string]any{"summary": api.EvaluationEvidenceReference{Store: parent.Spec.EvidenceStore, Key: "serving/experiments.v1alpha1/" + string(parent.UID) + "/summary.json", Size: 10}}
		}
		store.put(evidenceKey(parent.UID, scope(stageID)+"/"+step+"/result.json"), result)
		return nil
	}})
	runController, stageController := RunReconciler{Runtime: r}, StageReconciler{Runtime: r}
	for range 60 {
		if _, err := runController.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(parent)}); err != nil {
			t.Fatal(err)
		}
		children := &api.EvaluationStageList{}
		if err := r.APIReader.List(ctx, children); err != nil {
			t.Fatal(err)
		}
		for _, child := range children.Items {
			if _, err := stageController.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(&child)}); err != nil {
				t.Fatal(err)
			}
		}
		saved := &api.EvaluationRun{}
		if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(parent), saved); err != nil {
			t.Fatal(err)
		}
		if isTrue(saved.Status.Conditions, api.EvaluationConditionPublished) {
			if occupied(saved) || saved.Status.MeasurementVerdict != api.MeasurementPASS || !isTrue(saved.Status.Conditions, api.EvaluationConditionExecutionComplete) || len(saved.Status.ChildRefs) != 1 {
				t.Fatalf("incorrect final status: %+v", saved.Status)
			}
			if len(children.Items) != 1 || children.Items[0].Status.ResultRef == nil {
				t.Fatal("missing child judgment evidence")
			}
			assertPublishedWorkspaceDeleted(ctx, t, r, &runController, saved)
			return
		}
	}
	t.Fatalf("workflow did not publish; created %d objects", ordinal)
}

func TestExecutionLostReleasesPoolOnlyAfterObservedCleanup(t *testing.T) {
	for _, residual := range []bool{false, true} {
		t.Run(map[bool]string{false: "clean", true: "residual"}[residual], func(t *testing.T) {
			ctx := context.Background()
			parent := fixtureRun("lost")
			startRun(parent)
			condition(&parent.Status.Conditions, 1, api.EvaluationConditionPoolAcquired, metav1.ConditionTrue, "Acquired", "")
			r, store := fixtureRuntime(t, parent)
			child := fixtureStage(t, &r, parent)
			before := child.DeepCopy()
			now := metav1.NewTime(fixtureTime)
			child.Status.FinishedAt = &now
			condition(&child.Status.Conditions, 1, api.EvaluationConditionExecutionComplete, metav1.ConditionFalse, api.EvaluationReasonExecutionLost, "stage Pod disappeared")
			condition(&child.Status.Conditions, 1, api.EvaluationConditionStageCleanupComplete, metav1.ConditionTrue, complete, "")
			if err := r.patchStatus(ctx, child, before); err != nil {
				t.Fatal(err)
			}
			parent.Status.ChildRefs = []api.EvaluationChildReference{{StageID: child.Spec.StageID, EvaluationObjectReference: *ref(child)}}
			parent.Status.CurrentStage = child.Spec.StageID
			if err := r.Client.Status().Update(ctx, parent); err != nil {
				t.Fatal(err)
			}
			cleanup := map[string]string{"status": complete, "reason": complete}
			result := map[string]any{"schemaVersion": "evaluation-step.v1alpha1", "runUID": parent.UID, "step": api.EvaluationStepFinish,
				"execution": map[string]string{"status": complete, "reason": complete}, "cleanup": cleanup, "startedAt": fixtureTime, "finishedAt": fixtureTime}
			if residual {
				cleanup["status"], cleanup["reason"] = failed, api.EvaluationReasonResidualState
				result["residuals"] = []map[string]string{{"kind": "Pod", "name": "remaining-producer", "reason": api.EvaluationReasonResidualState}}
			}
			store.put(evidenceKey(parent.UID, "run/finish/result.json"), result)
			controller := RunReconciler{Runtime: r}
			for range 3 {
				if _, err := controller.reconcileChildren(ctx, parent, parent.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			if isTrue(parent.Status.Conditions, api.EvaluationConditionCleanupComplete) == residual ||
				isTrue(parent.Status.Conditions, api.EvaluationConditionPoolAcquired) != residual || occupied(parent) != residual {
				t.Fatalf("incorrect cleanup/pool state: %+v", parent.Status)
			}
			if isTrue(parent.Status.Conditions, api.EvaluationConditionExecutionComplete) {
				t.Fatal("cleanup must not turn lost execution into success")
			}
			if residual {
				if parent.Status.ResidualStateRef == nil || !bytes.Contains(store[parent.Status.ResidualStateRef.Key], []byte("remaining-producer")) {
					t.Fatal("retained pool must expose observed residual evidence")
				}
			}
		})
	}
}

func TestInjectionCordonBeforeUIDDeleteAndNoReplay(t *testing.T) {
	for _, loseResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "response-lost"}[loseResponse], func(t *testing.T) {
			ctx := context.Background()
			parent := fixtureRun("injection")
			startRun(parent)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}}
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "serving", Namespace: parent.Namespace, UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: node.Name}}
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "Workload", Name: "workload", UID: "workload-uid"}}
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			r, store := fixtureRuntime(t, parent, node, pod)
			stage := fixtureStage(t, &r, parent)
			stage.Spec.FailureInjection = []api.FaultInjectionPlan{{ID: "fault", Action: api.FaultInjectionCordonDelete, Selection: &api.FaultInjectionSelection{WorkloadUIDs: []types.UID{"workload-uid"}, OrderBy: []api.FaultInjectionOrderKey{"node", "pod", "podUID"}}}}
			if err := r.Client.Update(ctx, stage); err != nil {
				t.Fatal(err)
			}
			before := parent.DeepCopy()
			parent.Status.CurrentStage = stage.Spec.StageID
			parent.Status.ChildRefs = []api.EvaluationChildReference{{StageID: stage.Spec.StageID, EvaluationObjectReference: *ref(stage)}}
			if err := r.patchStatus(ctx, parent, before); err != nil {
				t.Fatal(err)
			}
			store.put(evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/go.json"), map[string]any{"runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "goUTC": fixtureTime})
			var effects []string
			r.Client = interceptor.NewClient(r.Client.(client.WithWatch), interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, o client.Object, patch client.Patch, options ...client.PatchOption) error {
					if _, ok := o.(*corev1.Node); ok {
						effects = append(effects, "cordon")
					}
					return c.Patch(ctx, o, patch, options...)
				},
				Delete: func(ctx context.Context, c client.WithWatch, o client.Object, options ...client.DeleteOption) error {
					applied := &client.DeleteOptions{}
					for _, option := range options {
						option.ApplyToDelete(applied)
					}
					if applied.Preconditions == nil || applied.Preconditions.UID == nil || *applied.Preconditions.UID != pod.UID {
						t.Fatal("missing Pod UID precondition")
					}
					effects = append(effects, "delete")
					if err := c.Delete(ctx, o, options...); err != nil {
						return err
					}
					if loseResponse {
						return errors.New("delete response lost")
					}
					return nil
				},
			})
			for range 3 {
				if err := r.injections(ctx, parent, stage); err != nil && !errors.Is(err, errInjectionUpdated) {
					t.Fatal(err)
				}
			}
			if len(effects) != 2 || effects[0] != "cordon" || effects[1] != "delete" {
				t.Fatalf("effects=%v", effects)
			}
			expected := complete
			if loseResponse {
				expected = injectionUnknown
			}
			if len(stage.Status.InjectionRecords) != 1 || stage.Status.InjectionRecords[0].Outcome != expected || stage.Status.InjectionRecords[0].EvidenceRef == nil {
				t.Fatalf("records=%+v", stage.Status.InjectionRecords)
			}
		})
	}
}

func TestChildFreezesOpaqueSettingsPatchAndGeneratorNode(t *testing.T) {
	parent := fixtureRun("patch")
	startRun(parent)
	parent.Spec.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`{"controls":{"power":false}}`)}
	r, _ := fixtureRuntime(t, parent)
	child, err := (&RunReconciler{Runtime: r}).ensureChild(context.Background(), parent, 0)
	if err != nil {
		t.Fatal(err)
	}
	if child.Spec.LoadGenerator.NodeName != "genesis1" || string(child.Spec.SettingsPatch.Raw) != string(parent.Spec.SettingsPatch.Raw) {
		t.Fatalf("child=%+v", child.Spec)
	}
	parent.Spec.SettingsPatch.Raw[0] = '['
	if child.Spec.SettingsPatch.Raw[0] != '{' {
		t.Fatal("child patch aliases parent bytes")
	}
	override := fixtureRun("override")
	startRun(override)
	override.Spec.SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`{"controls":{"power":false}}`)}
	override.Spec.Stages[0].SettingsPatch = &apiextensionsv1.JSON{Raw: []byte(`{"maxReplicas":3}`)}
	r, _ = fixtureRuntime(t, override)
	child, err = (&RunReconciler{Runtime: r}).ensureChild(context.Background(), override, 0)
	if err != nil || string(child.Spec.SettingsPatch.Raw) != `{"maxReplicas":3}` {
		t.Fatalf("stage override=%+v %v", child, err)
	}
}

// The workspace survives until publication and is removed by the next reconcile.
func assertPublishedWorkspaceDeleted(ctx context.Context, t *testing.T, r Runtime, runController *RunReconciler, run *api.EvaluationRun) {
	t.Helper()
	workspace := client.ObjectKey{Namespace: run.Namespace, Name: run.Status.Workspace.Name}
	if err := r.APIReader.Get(ctx, workspace, &corev1.PersistentVolumeClaim{}); err != nil {
		t.Fatalf("run must hold its workspace until published: %v", err)
	}
	if _, err := runController.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
		t.Fatal(err)
	}
	if err := r.APIReader.Get(ctx, workspace, &corev1.PersistentVolumeClaim{}); !apierrors.IsNotFound(err) {
		t.Fatalf("published run workspace must be deleted, got %v", err)
	}
}

func TestPublishedIncompleteRunKeepsWorkspace(t *testing.T) {
	ctx := context.Background()
	parent := fixtureRun("residual")
	startRun(parent)
	workspace := &corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "residual-work", Namespace: parent.Namespace, UID: "work-uid"}}
	parent.Status.Workspace = ref(workspace)
	for name, conditions := range map[string][2]metav1.ConditionStatus{
		"residual cleanup":             {metav1.ConditionTrue, metav1.ConditionFalse},
		"failed execution (no result)": {metav1.ConditionFalse, metav1.ConditionTrue},
	} {
		t.Run(name, func(t *testing.T) {
			run := parent.DeepCopy()
			condition(&run.Status.Conditions, 1, api.EvaluationConditionExecutionComplete, conditions[0], api.EvaluationReasonProcedureFailed, "")
			condition(&run.Status.Conditions, 1, api.EvaluationConditionCleanupComplete, conditions[1], api.EvaluationReasonResidualState, "")
			condition(&run.Status.Conditions, 1, api.EvaluationConditionPublished, metav1.ConditionTrue, complete, "")
			r, _ := fixtureRuntime(t, run, workspace.DeepCopy())
			if _, err := (&RunReconciler{Runtime: r}).Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}); err != nil {
				t.Fatal(err)
			}
			if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(workspace), &corev1.PersistentVolumeClaim{}); err != nil {
				t.Fatalf("published run without complete execution and cleanup must keep its workspace: %v", err)
			}
		})
	}
}

// The harness may write only to mounted volumes: the uploaded workspace and in-memory /private.
func assertHarnessWritesOnlyMounts(t *testing.T, pod corev1.PodSpec) {
	t.Helper()
	security := pod.Containers[0].SecurityContext
	if security == nil || security.ReadOnlyRootFilesystem == nil || !*security.ReadOnlyRootFilesystem {
		t.Fatal("harness container root filesystem must be read-only")
	}
	for _, volume := range pod.Volumes {
		if volume.HostPath != nil {
			t.Fatalf("harness must not mount host paths: %s", volume.Name)
		}
	}
	for _, env := range pod.Containers[0].Env {
		if env.Name == "HOME" && env.Value != "/private" {
			t.Fatalf("HOME must be on a writable mount, got %s", env.Value)
		}
	}
}
