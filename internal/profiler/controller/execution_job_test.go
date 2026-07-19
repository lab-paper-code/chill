package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type staticEvidenceStoreResolver struct {
	binding EvidenceStoreBinding
	err     error
}

func (r staticEvidenceStoreResolver) Resolve(context.Context) (EvidenceStoreBinding, error) {
	return r.binding, r.err
}

func TestBuildExecutionJobProjectsFrozenIntentAndDeliveryBinding(t *testing.T) {
	run := testRun()
	prepared := currentPreparedRun(t)
	binding := testEvidenceStoreBinding()
	scheme := executionJobTestScheme(t)

	job, err := buildExecutionJob(run, prepared, binding, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if job.Namespace != run.Namespace || job.Spec.Parallelism == nil || *job.Spec.Parallelism != 1 ||
		job.Spec.Completions == nil || *job.Spec.Completions != 1 ||
		job.Spec.BackoffLimit == nil || *job.Spec.BackoffLimit != 0 ||
		job.Spec.Template.Spec.RestartPolicy != corev1.RestartPolicyNever ||
		job.Spec.Template.Spec.AutomountServiceAccountToken == nil || *job.Spec.Template.Spec.AutomountServiceAccountToken {
		t.Fatalf("Job retry or identity boundary drifted: %#v", job.Spec)
	}
	if !metav1.IsControlledBy(job, run) || job.Annotations[intentDigestAnnotation] != prepared.IntentDigest ||
		job.Annotations[executionPolicyAnnotation] != profiler.CPUORTWorkerPolicy+"/"+profiler.CPUORTWorkerPolicyVersion {
		t.Fatalf("Job ownership or execution identity missing: %#v", job.ObjectMeta)
	}
	pod := job.Spec.Template.Spec
	if pod.NodeSelector[corev1.LabelHostname] != prepared.Intent.TargetNode.Name ||
		len(pod.InitContainers) != 1 || len(pod.Containers) != 2 || len(pod.Volumes) != 4 {
		t.Fatalf("unexpected measurement topology: %#v", pod)
	}
	artifact := pod.InitContainers[0]
	if artifact.Image != prepared.Intent.Images.Artifact ||
		len(artifact.Args) != 4 || artifact.Args[2] != prepared.Intent.State.ArtifactPath {
		t.Fatalf("artifact supply was not frozen into the Job: %#v", artifact)
	}
	runtimeContainer := findContainer(t, pod.Containers, "runtime")
	assertArgument(t, runtimeContainer.Args, "--intent-digest="+prepared.IntentDigest)
	assertArgument(t, runtimeContainer.Args, "--store-endpoint="+binding.Endpoint)
	assertArgument(t, runtimeContainer.Args, "--raw-object-key=profiling-runs/"+string(run.UID)+"/raw/"+
		strings.TrimPrefix(prepared.IntentDigest, "sha256:")+".json")
	if runtimeContainer.Resources.Limits.Cpu().String() != prepared.Intent.CPU.Limit ||
		runtimeContainer.Resources.Requests.Cpu().String() != prepared.Intent.CPU.Request {
		t.Fatalf("runtime CPU contract drifted: %#v", runtimeContainer.Resources)
	}
	if !hasSecretEnv(runtimeContainer.Env, "AWS_ACCESS_KEY_ID", binding.CredentialsSecretName, component.EvidenceStoreAccessKeyKey) ||
		!hasSecretEnv(runtimeContainer.Env, "AWS_SECRET_ACCESS_KEY", binding.CredentialsSecretName, component.EvidenceStoreSecretKeyKey) {
		t.Fatalf("Evidence Store credentials were not projected by Secret reference: %#v", runtimeContainer.Env)
	}
	observer := findContainer(t, pod.Containers, "power-observer")
	assertArgument(t, observer.Args, "--max-events=33")
}

func TestExecutionJobsRecoverExactOwnedJobAndRejectConflict(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	prepared := currentPreparedRun(t)
	binding := testEvidenceStoreBinding()
	scheme := executionJobTestScheme(t)
	desired, err := buildExecutionJob(run, prepared, binding, scheme)
	if err != nil {
		t.Fatal(err)
	}
	desired.UID = "job-uid"
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(desired).Build()
	jobs, err := NewExecutionJobs(
		kubeClient,
		scheme,
		staticEvidenceStoreResolver{err: errors.New("current delivery binding must not be read during recovery")},
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered, created, err := jobs.Ensure(ctx, run, prepared)
	if err != nil || created || recovered.UID != "job-uid" {
		t.Fatalf("expected exact Job recovery, got created=%v job=%#v err=%v", created, recovered, err)
	}

	retained := &batchv1.Job{}
	if err := kubeClient.Get(ctx, types.NamespacedName{Namespace: desired.Namespace, Name: desired.Name}, retained); err != nil {
		t.Fatal(err)
	}
	retained.Spec.Template.Spec.NodeSelector[corev1.LabelHostname] = "other-node"
	if err := kubeClient.Update(ctx, retained); err != nil {
		t.Fatal(err)
	}
	if _, _, err := jobs.Ensure(ctx, run, prepared); err == nil || !strings.Contains(err.Error(), "frozen execution contract") {
		t.Fatalf("conflicting retained Job accepted: %v", err)
	}
}

func currentPreparedRun(t *testing.T) profiler.PreparedRun {
	t.Helper()
	prepared := testPreparedRun(t)
	prepared.Intent.SchemaVersion = profilingrun.SchemaVersion
	prepared.Intent.State.ArtifactPath = "/artifact/mobilenet-v2-050.onnx"
	digest, err := prepared.Intent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	prepared.IntentDigest = digest
	return prepared
}

func testEvidenceStoreBinding() EvidenceStoreBinding {
	return EvidenceStoreBinding{
		Store: "chill-evidence-store", Endpoint: "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Bucket: "chill-profiling-evidence", Region: "us-east-1", CredentialsSecretName: "chill-evidence-store-credentials",
	}
}

func executionJobTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, batchv1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func findContainer(t *testing.T, containers []corev1.Container, name string) corev1.Container {
	t.Helper()
	for _, container := range containers {
		if container.Name == name {
			return container
		}
	}
	t.Fatalf("container %q not found", name)
	return corev1.Container{}
}

func assertArgument(t *testing.T, arguments []string, want string) {
	t.Helper()
	for _, argument := range arguments {
		if argument == want {
			return
		}
	}
	t.Fatalf("argument %q not found in %#v", want, arguments)
}

func hasSecretEnv(env []corev1.EnvVar, name, secretName, key string) bool {
	for _, variable := range env {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.SecretKeyRef != nil &&
			variable.ValueFrom.SecretKeyRef.Name == secretName && variable.ValueFrom.SecretKeyRef.Key == key {
			return true
		}
	}
	return false
}
