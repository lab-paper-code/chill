package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestAsyncWriterFailureKeepsInferenceHTTP503(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	directory := cleanObservationDirectory(t)
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: directory, Store: "evidence", Retention: time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := serving.NewRuntime(ctx, serving.RuntimeConfig{ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	definition := &serving.Definition{Name: "workload", UID: "uid", Generation: 1}
	if err := runtime.PublishClosed(definition, "test"); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	// A real mkdir conflict fails the asynchronous worker after admission.
	blocked := filepath.Join(directory, "v1alpha5", time.Now().UTC().Format("20060102T15"))
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(blocked)
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err := log.ManagementEvent(serving.ManagementEvent{WorkloadUID: definition.UID, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-log.Failure():
	case <-time.After(time.Second):
		t.Fatal("worker failure was not published")
	}
	if log.Health().Healthy {
		t.Fatal("readiness trusted earlier enqueue after writer failure")
	}
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, k8sruntime.Object, error) {
		return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{Authenticated: true,
			User: authentication.UserInfo{Username: "inference-client"}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, k8sruntime.Object, error) {
		return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	h := &Handler{runtime: runtime, authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{RequestTimeout: time.Second}, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /api/workloads/{id}/infer", h.infer)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/workloads/workload/infer", nil)
	request.Header.Set("Authorization", "Bearer inference-client")
	h.ServeHTTP(response, request)
	var failure serving.Error
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || failure.Code != serving.ObservationUnavailableCode || response.Header().Get("Retry-After") != "" {
		t.Fatalf("worker failure changed inference rejection: status=%d failure=%+v headers=%v", response.Code, failure, response.Header())
	}
}
