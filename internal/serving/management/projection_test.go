package management

import (
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSnapshotFreezesAdmission(t *testing.T) {
	workload := &api.Workload{ObjectMeta: metav1.ObjectMeta{Name: "workload", UID: "workload-uid", Generation: 1}, Spec: api.WorkloadSpec{
		SLO:       api.WorkloadSLO{Percentile: 95, TargetLatency: metav1.Duration{Duration: time.Second}},
		Admission: api.DefaultWorkloadAdmission(), RequestLifetime: &metav1.Duration{Duration: 2 * time.Second},
	}}
	model := &api.ModelSpec{ObjectMeta: metav1.ObjectMeta{Name: "model", UID: "model-uid", Generation: 1}, Spec: api.ModelSpecSpec{
		Artifacts: []api.ModelArtifact{{Name: "onnx", Format: "onnx", Source: api.ModelArtifactSource{Image: "model:current", Architecture: "amd64", Files: []string{"/model.onnx"}}}},
		ExecutionPaths: []api.ModelExecutionPath{{Name: "native", Artifact: "onnx", Runtime: api.ModelRuntimeRequirement{Family: "firstfloor", Backend: "ort-cpu",
			Settings: &api.ModelRuntimeSettings{Firstfloor: &api.FirstfloorRuntimeSettings{MaxBatchSize: 4, MaxQueueSize: 8}}}}},
	}}
	preprocessor := &api.Preprocessor{ObjectMeta: metav1.ObjectMeta{Name: "pre", UID: "pre-uid", Generation: 1}}
	first, err := Snapshot(workload, model, preprocessor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Admission != (serving.Admission{Policy: serving.AdmissionPolicyReject, MaxQueuedRequests: 1024}) {
		t.Fatalf("projected admission = %+v", first.Admission)
	}
	for _, change := range []func(){
		func() { workload.Spec.Admission.Policy = api.AdmissionPolicyDelay },
		func() { workload.Spec.Admission.MaxQueuedRequests = 17 },
	} {
		previous := first
		change()
		workload.Generation++
		first, err = Snapshot(workload, model, preprocessor, nil)
		if err != nil {
			t.Fatal(err)
		}
		if first.Identity() == previous.Identity() || !first.SameExecution(previous) {
			t.Fatal("admission change must advance identity without changing execution")
		}
		if first.Admission == previous.Admission {
			t.Fatal("admission change was not projected")
		}
		status := appliedStatus(first)
		if status.Admission != workload.Spec.Admission || status.RequestLifetime.Duration != 2*time.Second {
			t.Fatalf("applied status = %+v", status)
		}
	}
	if workload.Spec.Admission.Policy != api.AdmissionPolicyDelay || first.Admission.MaxQueuedRequests != 17 {
		t.Fatal("changed policy or queue bound lost")
	}
}
