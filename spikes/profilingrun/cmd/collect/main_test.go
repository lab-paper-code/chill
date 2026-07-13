package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestDecodePrefixedRequiresExactlyOneStrictRecord(t *testing.T) {
	var result struct {
		Status string `json:"status"`
	}
	prefix := "EXPERIMENT_RESULT_JSON "
	if err := decodePrefixed([]byte("noise\n"+prefix+"{\"status\":\"Succeeded\"}\n"), prefix, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "Succeeded" {
		t.Fatal(result.Status)
	}
	if err := decodePrefixed([]byte(prefix+"{}\n"+prefix+"{}\n"), prefix, &result); err == nil {
		t.Fatal("expected duplicate rejection")
	}
	if err := decodePrefixed([]byte(prefix+"{\"unknown\":1}\n"), prefix, &result); err == nil {
		t.Fatal("expected unknown field rejection")
	}
}

func TestValidateCandidateBindsExactBytesAndSelection(t *testing.T) {
	intent := testCollectorIntent()
	payload, err := os.ReadFile(filepath.Join("..", "..", "fixtures", "candidate-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	intent.Candidate.ReportDigest = "sha256:" + hex.EncodeToString(sum[:])
	if err := profilingrun.ValidateCandidateReport(payload, intent); err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := profilingrun.ValidateCandidateReport(payload, intent); err == nil {
		t.Fatal("expected exact-byte digest rejection")
	}
}

func TestValidateKubernetesBindsOwnershipSuccessAndImages(t *testing.T) {
	intent := testCollectorIntent()
	digest, _ := intent.Digest()
	truth := true
	uid := types.UID("job-uid")
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			UID:         uid,
			Annotations: map[string]string{"chill.dacs.io/run-intent-digest": digest},
		},
		Status: batchv1.JobStatus{
			Succeeded:  1,
			Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations:     map[string]string{"chill.dacs.io/run-intent-digest": digest},
			OwnerReferences: []metav1.OwnerReference{{Kind: "Job", UID: uid, Controller: &truth}},
		},
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "artifact", Image: intent.Images.Artifact}},
			Containers: []corev1.Container{
				{Name: "runtime", Image: intent.Images.Runtime},
				{Name: "power-observer", Image: intent.Images.PowerObserver},
			},
		},
	}
	if err := validateKubernetes(job, pod, intent, digest); err != nil {
		t.Fatal(err)
	}
	pod.OwnerReferences[0].UID = "other"
	if err := validateKubernetes(job, pod, intent, digest); err == nil {
		t.Fatal("expected owner rejection")
	}
}

func testCollectorIntent() profilingrun.Intent {
	payload, _ := os.ReadFile(filepath.Join("..", "..", "fixtures", "lattepanda-ort-cpu-bs1.json"))
	var intent profilingrun.Intent
	_ = json.Unmarshal(payload, &intent)
	return intent
}

func TestValidateStatusesPreservesArtifactRuntimeIdentityBoundary(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	image := "registry/image@" + digest
	intent := profilingrun.Intent{Images: profilingrun.Images{Artifact: image, Runtime: image, PowerObserver: image}}
	done := func(name, imageID string) corev1.ContainerStatus {
		return corev1.ContainerStatus{
			Name:    name,
			ImageID: imageID,
			State:   corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}},
		}
	}
	init := []corev1.ContainerStatus{done("artifact", "registry/image@sha256:"+strings.Repeat("b", 64))}
	containers := []corev1.ContainerStatus{
		done("runtime", "registry/image@"+digest),
		done("power-observer", "registry/image@"+digest),
	}
	if err := validateStatuses(intent, init, containers); err != nil {
		t.Fatal(err)
	}
	containers[0].ImageID = "registry/image@sha256:" + strings.Repeat("c", 64)
	if err := validateStatuses(intent, init, containers); err == nil {
		t.Fatal("expected runtime image mismatch")
	}
}
