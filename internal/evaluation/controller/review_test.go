package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/yaml"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func TestReviewMetadataPatchRBAC(t *testing.T) {
	source, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`\+kubebuilder:rbac:groups=edge.dacs.io,resources=([^,\n]+),verbs=([^\n]+)`).FindAllStringSubmatch(string(source), -1)
	markerRules := make([]rbacv1.PolicyRule, 0, len(matches))
	for _, match := range matches {
		markerRules = append(markerRules, rbacv1.PolicyRule{APIGroups: []string{"edge.dacs.io"}, Resources: strings.Split(match[1], ";"), Verbs: strings.Split(match[2], ";")})
	}
	chart, err := os.ReadFile("../../../charts/chill/templates/evaluation_rbac.yaml")
	if err != nil {
		t.Fatal(err)
	}
	profiler := strings.SplitN(string(chart), "{{- if .Values.profiler.enabled }}", 2)
	if len(profiler) != 2 {
		t.Fatal("missing profiler Role")
	}
	roleYAML := strings.Split(profiler[1], "\n---\n")[1]
	roleYAML = regexp.MustCompile(`{{[^}]+}}`).ReplaceAllString(roleYAML, "profiler")
	var role rbacv1.Role
	if err := yaml.Unmarshal([]byte(roleYAML), &role); err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"evaluationruns", "evaluationstages"} {
		for name, rules := range map[string][]rbacv1.PolicyRule{"marker": markerRules, "chart": role.Rules} {
			allowed := false
			for _, rule := range rules {
				if slices.Contains(rule.APIGroups, "edge.dacs.io") && slices.Contains(rule.Resources, resource) && slices.Contains(rule.Verbs, "patch") {
					allowed = true
				}
			}
			if !allowed {
				t.Errorf("%s lacks metadata patch on %s", name, resource)
			}
		}
	}
}

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
	stage.Spec.FailureInjection = []api.FaultInjectionPlan{{ID: "fault", Action: actionCordonDelete, Selection: &api.FaultInjectionSelection{WorkloadUIDs: []types.UID{"workload-uid"}, OrderBy: []api.FaultInjectionOrderKey{"node", "pod", "instance"}}}}
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
			body = `{"snapshot":{"at":"2026-11-01T12:00:00Z","nodes":[{"node":{"name":"node","uid":"node-uid"},"ready":true}],"instances":[{"id":"instance","workloadUID":"workload-uid","ownerWorkloadUID":"workload-uid","ready":true,"podName":"serving","podUID":"pod-uid","node":{"name":"node","uid":"node-uid"}}],"routing":{"workload-uid":{"instance":1}}}}`
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
