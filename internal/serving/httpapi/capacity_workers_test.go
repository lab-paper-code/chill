package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestCapacityEndpointShowsAcceptedLiveWorkerLimitAndIdleCount(t *testing.T) {
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: actorUsername, UID: "actor-uid"},
		}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	history, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = history.Close(context.Background()) })
	settings := schedulerSettingsForTest()
	settings.CapacityWorkers = 3
	service, execution := settingsServiceForTest(t, kube, history, settings, false, false)
	handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, runtime: execution,
		config: HandlerConfig{Scheduler: service.Controller}}
	for _, workers := range []int{3, 1} {
		if workers != 3 {
			settings.CapacityWorkers = workers
			if err := service.Controller.AcceptSettings(context.Background(), settings, nil); err != nil {
				t.Fatal(err)
			}
		}
		request := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/capacity", nil)
		request.Header.Set("Authorization", "Bearer reviewed-token")
		response := httptest.NewRecorder()
		handler.schedulerCapacity(response, request)
		var shown struct {
			Rows    []serving.CapacityRow        `json:"rows"`
			Workers serving.CapacityWorkerStatus `json:"workers"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &shown); err != nil || response.Code != http.StatusOK {
			t.Fatalf("capacity response: status=%d body=%s error=%v", response.Code, response.Body, err)
		}
		if shown.Rows == nil || len(shown.Rows) != 0 || shown.Workers.Limit != workers || shown.Workers.Active != 0 {
			t.Fatalf("capacity endpoint hid current admission or idle state: %s", response.Body)
		}
	}
}
