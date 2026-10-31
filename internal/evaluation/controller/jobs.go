package controller

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func validateConfig(v map[string]string) error {
	for _, key := range []string{"actionServiceAccount", "publishServiceAccount", "caSecret", "producerSSHSecret", "evidenceCredentialsSecret", "evidenceEndpoint", "evidenceBucket", "evidenceRegion", "storageClass", "workspaceSize", "chillAPIURL"} {
		if strings.TrimSpace(v[key]) == "" {
			return fmt.Errorf("config %s is required", key)
		}
	}
	for _, key := range []string{"jobActiveDeadlineSeconds", "injectionWindowSeconds", "injectionSnapshotTTLSeconds"} {
		n, err := strconv.ParseInt(v[key], 10, 64)
		if err != nil || n <= 0 || n > 86400*30 {
			return fmt.Errorf("config %s must be positive bounded seconds", key)
		}
	}
	return nil
}

func (r *Runtime) job(ctx context.Context, owner client.Object, run *api.EvaluationRun, stage *api.EvaluationStage, step string, p *api.EvaluationProcedure) (*batchv1.Job, error) {
	v := run.Status.FrozenConfig.Values
	suffix := step
	if suffix == api.EvaluationStepCleanupJobs {
		suffix = "cleanup"
	}
	name := uidName(owner.GetUID()) + "-" + suffix
	cmName := uidName(owner.GetUID()) + "-spec"
	spec := any(run.Spec)
	stageID := ""
	execution := map[string]any{"runName": run.Name, "runUID": run.UID, "generation": owner.GetGeneration(), "namespace": run.Namespace}
	if stage != nil {
		spec = stage.Spec
		stageID = stage.Spec.StageID
		execution["stageName"] = stage.Name
		execution["stageUID"] = stage.UID
		execution["stageID"] = stageID
	}
	specJSON, err := jsonString(spec)
	if err != nil {
		return nil, err
	}
	executionJSON, err := jsonString(execution)
	if err != nil {
		return nil, err
	}
	configJSON, err := jsonString(v)
	if err != nil {
		return nil, err
	}
	if _, err = r.ensureConfigMap(ctx, owner, cmName, map[string]string{"spec.json": specJSON, "execution.json": executionJSON, "config.json": configJSON}); err != nil {
		return nil, err
	}
	deadline, err := strconv.ParseInt(v["jobActiveDeadlineSeconds"], 10, 64)
	if err != nil {
		return nil, err
	}
	sa := v["actionServiceAccount"]
	token := true
	if step == api.EvaluationStepJudge || step == api.EvaluationStepPublish {
		sa = v["publishServiceAccount"]
		token = false
	}
	labels := map[string]string{api.EvaluationLabelRunUID: string(run.UID), api.EvaluationLabelStage: stageID, api.EvaluationLabelStep: step, "edge.dacs.io/workload-role": "system"}
	if stageID == "" {
		labels[api.EvaluationLabelStage] = scopeRun
	}
	env := []corev1.EnvVar{{Name: "HOME", Value: "/tmp"}, {Name: "PYTHONDONTWRITEBYTECODE", Value: "1"}, {Name: "CHILL_API_URL", Value: v["chillAPIURL"]}, {Name: "CHILL_EVIDENCE_STORE", Value: run.Spec.EvidenceStore}, {Name: "CHILL_EVIDENCE_ENDPOINT", Value: v["evidenceEndpoint"]}, {Name: "CHILL_EVIDENCE_BUCKET", Value: v["evidenceBucket"]}, {Name: "CHILL_EVIDENCE_REGION", Value: v["evidenceRegion"]}}
	for _, item := range []struct{ name, key string }{{"AWS_ACCESS_KEY_ID", "access-key"}, {"AWS_SECRET_ACCESS_KEY", "secret-key"}} {
		env = append(env, corev1.EnvVar{Name: item.name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: v["evidenceCredentialsSecret"]}, Key: item.key}}})
	}
	pod := corev1.PodSpec{ServiceAccountName: sa, AutomountServiceAccountToken: &token, RestartPolicy: corev1.RestartPolicyNever, Containers: []corev1.Container{{Name: "evaluation", Image: run.Spec.Harness.Image, Command: []string{"python3", "/opt/e5/hack/e5/evaluation.py"}, Args: []string{step, "--run", "/work/campaign/run", "--spec", "/spec"}, Env: env, VolumeMounts: []corev1.VolumeMount{{Name: "work", MountPath: "/work"}, {Name: "spec", MountPath: "/spec", ReadOnly: true}, {Name: "ca", MountPath: "/etc/chill/ca", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "work", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: run.Status.Workspace.Name}}}, {Name: "spec", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}}}}, {Name: "ca", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: v["caSecret"], Items: []corev1.KeyToPath{{Key: "tls.crt", Path: "tls.crt"}}}}}}}
	if step == api.EvaluationStepStage {
		pod.Volumes = append(pod.Volumes, corev1.Volume{Name: "producer-ssh", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: v["producerSSHSecret"], DefaultMode: ptr(int32(0400))}}})
		pod.Containers[0].VolumeMounts = append(pod.Containers[0].VolumeMounts, corev1.VolumeMount{Name: "producer-ssh", MountPath: "/secrets/producer", ReadOnly: true})
	}
	expected := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace(), Labels: labels}, Spec: batchv1.JobSpec{BackoffLimit: ptr(int32(0)), Parallelism: ptr(int32(1)), Completions: ptr(int32(1)), ActiveDeadlineSeconds: &deadline, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: pod}}}
	if err = controllerutil.SetControllerReference(owner, expected, r.Scheme); err != nil {
		return nil, err
	}
	existing := &batchv1.Job{}
	err = r.APIReader.Get(ctx, client.ObjectKeyFromObject(expected), existing)
	if apierrors.IsNotFound(err) {
		if p.JobRef != nil {
			return nil, errExecutionLost
		}
		// Recheck the live parent immediately before the only external effect.
		if stage != nil {
			if _, err = r.activeParent(ctx, stage); err != nil {
				return nil, err
			}
		}
		err = r.Client.Create(ctx, expected)
		if err == nil {
			return expected, nil
		}
		if !apierrors.IsAlreadyExists(err) {
			return nil, err
		}
		err = r.APIReader.Get(ctx, client.ObjectKeyFromObject(expected), existing)
	}
	if err != nil {
		return nil, err
	}
	if p.JobRef != nil && existing.UID != p.JobRef.UID {
		return nil, errExecutionLost
	}
	if !metav1.IsControlledBy(existing, owner) || !equality.Semantic.DeepEqual(existing.Labels, labels) || !sameJob(existing, expected) {
		return nil, fmt.Errorf("%s: Job %s frozen contract mismatch", api.EvaluationReasonIdentityMismatch, name)
	}
	return existing, nil
}
func sameJob(a, b *batchv1.Job) bool {
	x, y := a.Spec.Template.Spec, b.Spec.Template.Spec
	if x.ServiceAccountName != y.ServiceAccountName || x.RestartPolicy != y.RestartPolicy || !equality.Semantic.DeepEqual(x.AutomountServiceAccountToken, y.AutomountServiceAccountToken) || !equality.Semantic.DeepEqual(a.Spec.ActiveDeadlineSeconds, b.Spec.ActiveDeadlineSeconds) || !equality.Semantic.DeepEqual(a.Spec.BackoffLimit, b.Spec.BackoffLimit) || !equality.Semantic.DeepEqual(a.Spec.Parallelism, b.Spec.Parallelism) || !equality.Semantic.DeepEqual(a.Spec.Completions, b.Spec.Completions) || len(x.Containers) != 1 {
		return false
	}
	// Kubernetes defaults volume modes and container fields; compare only frozen inputs.
	c, d := x.Containers[0], y.Containers[0]
	if c.Image != d.Image || !equality.Semantic.DeepEqual(c.Command, d.Command) || !equality.Semantic.DeepEqual(c.Args, d.Args) || !equality.Semantic.DeepEqual(c.Env, d.Env) || !equality.Semantic.DeepEqual(c.VolumeMounts, d.VolumeMounts) {
		return false
	}
	if len(x.Volumes) != len(y.Volumes) {
		return false
	}
	for i, v := range x.Volumes {
		w := y.Volumes[i]
		if v.Name != w.Name {
			return false
		}
		if w.PersistentVolumeClaim != nil && !equality.Semantic.DeepEqual(v.PersistentVolumeClaim, w.PersistentVolumeClaim) {
			return false
		}
		if w.ConfigMap != nil && (v.ConfigMap == nil || v.ConfigMap.Name != w.ConfigMap.Name || !equality.Semantic.DeepEqual(v.ConfigMap.Items, w.ConfigMap.Items)) {
			return false
		}
		if w.Secret != nil && (v.Secret == nil || v.Secret.SecretName != w.Secret.SecretName || !equality.Semantic.DeepEqual(v.Secret.Items, w.Secret.Items)) {
			return false
		}
	}
	return true
}

// advance records the Job UID before reading result evidence or allowing a successor.
func (r *Runtime) advance(ctx context.Context, run *api.EvaluationRun, stage *api.EvaluationStage, step string, p *api.EvaluationProcedure) (*stepResult, bool, error) {
	if p.FinishedAt != nil {
		return nil, true, nil
	}
	owner := client.Object(run)
	if stage != nil {
		owner = stage
	}
	job, err := r.job(ctx, owner, run, stage, step, p)
	if err != nil {
		return nil, false, err
	}
	if p.JobRef == nil {
		p.JobRef = ref(job)
		now := r.now()
		p.StartedAt = &now
		return nil, false, nil
	}
	pods := &corev1.PodList{}
	if err := r.APIReader.List(ctx, pods, client.InNamespace(run.Namespace), client.MatchingLabels{"batch.kubernetes.io/controller-uid": string(job.UID)}); err != nil {
		return nil, false, err
	}
	if len(pods.Items) > 1 {
		return nil, false, errExecutionLost
	}
	if len(pods.Items) == 0 && p.PodRef != nil {
		return nil, false, errExecutionLost
	}
	if len(pods.Items) == 1 {
		pod := &pods.Items[0]
		if !metav1.IsControlledBy(pod, job) || p.PodRef != nil && p.PodRef.UID != pod.UID {
			return nil, false, errExecutionLost
		}
		p.PodRef = ref(pod)
	}
	store, err := r.Store(ctx, run.Namespace, run.Spec.EvidenceStore, run.Status.FrozenConfig.Values)
	if err != nil {
		return nil, false, err
	}
	stageID := ""
	stageUID := types.UID("")
	if stage != nil {
		stageID = stage.Spec.StageID
		stageUID = stage.UID
	}
	var result stepResult
	evidence, err := readJSON(ctx, store, run.Spec.EvidenceStore, evidenceKey(run.UID, scope(stageID)+"/"+step+"/result.json"), &result)
	if errors.Is(err, evidencestore.ErrNotFound) {
		for _, c := range job.Status.Conditions {
			if c.Status == corev1.ConditionTrue && (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) {
				return nil, false, fmt.Errorf("%s: Job terminated without stored result", api.EvaluationReasonRemoteEffectUnknown)
			}
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if err := validateResult(result, run.UID, stageUID, stageID, step); err != nil {
		return nil, false, err
	}
	for _, refs := range [][]api.EvaluationEvidenceReference{result.Artifacts, mapRefs(result.Outputs)} {
		for _, e := range refs {
			if e.Store == "" || e.Key == "" || e.Size < 1 {
				return nil, false, fmt.Errorf("%s: invalid result evidence reference", api.EvaluationReasonEvidenceUnreadable)
			}
		}
	}
	if result.Measurement != nil {
		switch result.Measurement.Verdict {
		case api.MeasurementPASS, api.MeasurementFAIL, api.MeasurementINVALID, api.MeasurementNotEvaluated:
		default:
			return nil, false, fmt.Errorf("%s: invalid measurement verdict", api.EvaluationReasonEvidenceUnreadable)
		}
		p.MeasurementVerdict = result.Measurement.Verdict
	}
	p.EvidenceRef = evidence
	p.FinishedAt = &result.FinishedAt
	p.Reason = result.Execution.Reason
	return &result, true, nil
}

func validateResult(result stepResult, runUID, stageUID types.UID, stageID, step string) error {
	if result.SchemaVersion != "evaluation-step.v1alpha1" || result.RunUID != runUID || result.StageUID != stageUID || result.StageID != stageID || result.Step != step || result.StartedAt.IsZero() || result.FinishedAt.IsZero() || (result.Execution.Status != complete && result.Execution.Status != "Failed") {
		return fmt.Errorf("%s: invalid result identity/schema", api.EvaluationReasonEvidenceUnreadable)
	}
	return nil
}
func mapRefs(values map[string]api.EvaluationEvidenceReference) []api.EvaluationEvidenceReference {
	refs := make([]api.EvaluationEvidenceReference, 0, len(values))
	for _, v := range values {
		refs = append(refs, v)
	}
	return refs
}
