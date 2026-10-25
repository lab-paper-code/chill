package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestManualOperationsRejectKindFieldsBeforeExecution(t *testing.T) {
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: actorUsername, UID: "actor-uid"},
		}}, nil
	})
	configAuthorizations := 0
	kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		if resource := review.Spec.ResourceAttributes; resource != nil {
			configAuthorizations++
			if resource.Verb != "update" || resource.Resource != "configmaps" || resource.Namespace != "serving" || resource.Name != "scheduler" {
				t.Errorf("manual operation authorized against wrong resource: %+v", resource)
			}
		}
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observationLog.Close(context.Background()) })
	controller, execution := settingsControllerForTest(t, kube, observationLog, schedulerSettingsForTest())
	handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, runtime: execution, observationLog: observationLog, config: HandlerConfig{
		Scheduler: controller, SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler", MaxRequestBytes: 1 << 20,
	}}
	for _, tc := range []struct{ body, field string }{
		{`{"kind":"route","workloadUID":"w","weights":{"p/c":1},"reason":"test","node":null}`, "node"},
		{`{"kind":"unroute","workloadUID":"w","reason":"test","weights":{}}`, "weights"},
		{`{"kind":"release","workloadUID":"w","reason":"test","fixedBatchSize":0}`, "fixedBatchSize"},
		{`{"kind":"reset","workloadUIDs":["w"],"reason":"test","workloadUID":""}`, "workloadUID"},
		{`{"kind":"reset","workloadUIDs":["w"],"reason":"test","actorUID":"forged"}`, "actorUID"},
		{`{"kind":"reset","workloadUIDs":["w"],"reason":null}`, "reason"},
		{`{"kind":"power","reason":"test"}`, "kind"},
	} {
		t.Run(tc.field+tc.body, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, manualOperationsPath, strings.NewReader(tc.body))
			request.Header.Set("Authorization", "Bearer caller-token")
			response := httptest.NewRecorder()
			handler.operations(response, request)
			var refusal struct {
				Code  string `json:"code"`
				Field string `json:"field"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &refusal); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusBadRequest || refusal.Code != "InvalidBody" || refusal.Field != tc.field {
				t.Fatalf("invalid request lost refusal shape: HTTP %d %s", response.Code, response.Body.String())
			}
		})
	}
	if configAuthorizations == 0 {
		t.Fatal("strict decoding bypassed scheduler ConfigMap authorization")
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource != "tokenreviews" && action.GetResource().Resource != "subjectaccessreviews" {
			t.Errorf("malformed request reached scheduler execution: %s %s", action.GetVerb(), action.GetResource().Resource)
		}
	}
}
