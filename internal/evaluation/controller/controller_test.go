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
	start := api.EvaluationEvidenceReference{Store: "evidence", Key: "start", Size: 1}
	return &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "system", UID: types.UID(name + "-uid"), Generation: 1, CreationTimestamp: metav1.NewTime(fixtureTime), Finalizers: []string{api.EvaluationRunFinalizer}}, Spec: api.EvaluationRunSpec{Question: "question", EvidenceStore: "evidence", Harness: image, Target: api.TargetIdentity{Components: []api.EvaluationTargetComponent{{Name: "server", Resource: api.EvaluationResourceReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "system", Name: "server"}, EvaluationImage: image}}}, LoadGenerator: api.Generator{EvaluationImage: image, Shards: map[string]int32{"w": 1}, Barrier: api.GeneratorBarrier{MinimumReadySamples: 1}}, PowerMeasurement: api.PowerMeasurement{Meters: []api.EvaluationMeter{{ID: "m", SourceIdentity: "source"}}, Nodes: []api.EvaluationObjectReference{{Name: "node", UID: "node-uid"}}, Series: "power", Sampling: api.PowerSampling{IntervalNanos: 1}}, Judgment: api.Judgment{EnergyCoverage: api.EnergyCoverage{MaxSingleGapSeconds: 1}}, Stages: []api.StageTemplate{{ID: "first", Row: "E5-3m", Mode: api.EvaluationModeAutonomous, LoadScenarioRef: api.EvaluationObjectReference{Name: "load", UID: "load-uid"}, StartState: api.EvaluationStartState{EvidenceRef: &start}}}}, Status: api.EvaluationRunStatus{ObservedGeneration: 1}}
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
	child, e := (&RunReconciler{Runtime: *r}).ensureChild(context.Background(), parent, 0, nil)
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
	if _, e := reconciler.ensureChild(context.Background(), parent, 0, nil); e == nil {
		t.Fatal("expected response loss")
	}
	child, e := reconciler.ensureChild(context.Background(), parent, 0, nil)
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
	r, _ := fixtureRuntime(t, parent)
	allocationPresent := true
	r.HTTP = func(_ context.Context, _ string, _ map[string]string, _ string, value any) error {
		if allocationPresent {
			return json.Unmarshal([]byte(`{"at":"2026-11-01T12:00:00Z","instances":[{"id":"owned","ownerWorkloadUID":"workload-uid"}],"routing":{"workload-uid":{"owned":1}},"poolReturn":{}}`), value)
		}
		return json.Unmarshal([]byte(`{"at":"2026-11-01T12:00:00Z","poolReturn":{}}`), value)
	}
	if !occupied(parent) {
		t.Fatal("cleanup failure released pool")
	}
	annotation := `{"reason":"operator confirmed writers stopped and pool returned","at":"2026-11-01T12:00:00Z","by":"operator"}`
	if result, err := (&RunReconciler{Runtime: r}).release(context.Background(), parent, annotation); err != nil || result.RequeueAfter == 0 || !occupied(parent) || len(parent.Status.ReleaseRecords) != 0 {
		t.Fatalf("release must wait for owned allocation/route return: result=%v err=%v", result, err)
	}
	allocationPresent = false
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
	r.HTTP = func(context.Context, string, map[string]string, string, any) error {
		t.Fatal("running release must not inspect or release the pool")
		return nil
	}
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
			return
		}
	}
	t.Fatalf("workflow did not publish; created %d objects", ordinal)
}

func TestInjectionCordonBeforeUIDDeleteAndNoReplay(t *testing.T) {
	for _, loseResponse := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "response-lost"}[loseResponse], func(t *testing.T) {
			ctx := context.Background()
			parent := fixtureRun("injection")
			startRun(parent)
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node", UID: "node-uid"}}
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "serving", Namespace: parent.Namespace, UID: "pod-uid"}, Spec: corev1.PodSpec{NodeName: node.Name}}
			r, store := fixtureRuntime(t, parent, node, pod)
			stage := fixtureStage(t, &r, parent)
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
			store.put(evidenceKey(parent.UID, scope(stage.Spec.StageID)+"/go.json"), map[string]any{"runUID": parent.UID, "stageUID": stage.UID, "stageID": stage.Spec.StageID, "goUTC": fixtureTime})
			r.HTTP = func(_ context.Context, _ string, _ map[string]string, path string, value any) error {
				var body string
				switch {
				case strings.Contains(path, "/state"):
					body = `{"at":"2026-11-01T12:00:00Z","nodes":[{"node":{"name":"node","uid":"node-uid"},"ready":true}],"instances":[{"id":"instance","workloadUID":"workload-uid","ownerWorkloadUID":"workload-uid","ready":true,"podName":"serving","podUID":"pod-uid","node":{"name":"node","uid":"node-uid"}}],"routing":{"workload-uid":{"instance":1}}}`
				case strings.Contains(path, "/settings"):
					body = `{"observationWindowNanos":1000000000}`
				default:
					body = `{"from":"2026-11-01T11:59:59Z","to":"2026-11-01T12:00:00Z","workloads":[{"uid":"workload-uid","instanceAssignments":{"instance":1}}]}`
				}
				return json.Unmarshal([]byte(body), value)
			}
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
