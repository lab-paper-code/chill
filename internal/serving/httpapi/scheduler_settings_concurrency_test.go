package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSchedulerSettingsGETDoesNotWaitForServiceLock(t *testing.T) {
	settings := schedulerSettingsForTest()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "17"},
		Data: map[string]string{"scheduler.json": `{"settings":` + string(raw) +
			`,"staticRouting":{"stored-uid":{"physical":1}},"powerObservation":{"coverage":{"MinimumCoverage":0.99}}}`},
	})
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: actorUsername, UID: "actor-uid"},
		}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	service, execution := settingsServiceForTest(t, kube, observationLog, settings, false, false)
	meters := []servingops.MeterDeclaration{{ID: "accepted-meter", Node: serving.NodeReference{Name: "accepted-node", UID: "accepted-uid"}, Scope: "node-exclusive-wall"}}
	handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, runtime: execution, config: HandlerConfig{
		Scheduler: service.Controller, SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler", SchedulerConfigKey: "scheduler.json",
		SchedulerMeters: func() []servingops.MeterDeclaration { return meters },
		RequestTimeout:  time.Second,
	}, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /api/v1/scheduler/settings", handler.scheduler)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/settings", nil).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer reviewed-token")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	service.Lock.Lock()
	// Release before helper teardown even when the old locking implementation fails.
	defer func() {
		service.Lock.Unlock()
		<-done
	}()
	go func() {
		defer close(done)
		handler.ServeHTTP(response, request)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("settings GET waited for the held service lock")
	}
	// The exact service lock remains held through all response assertions.
	var shown struct {
		serving.SchedulerSettings
		StaticRouting    map[string]map[string]int     `json:"staticRouting"`
		PowerObservation map[string]map[string]float64 `json:"powerObservation"`
		Meters           []servingops.MeterDeclaration `json:"meters"`
		RequestTimeout   time.Duration                 `json:"requestTimeout"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &shown); err != nil || response.Code != http.StatusOK || response.Header().Get("ETag") != `"17"` {
		t.Fatalf("settings GET: status=%d ETag=%q body=%s error=%v", response.Code, response.Header().Get("ETag"), response.Body, err)
	}
	if !reflect.DeepEqual(shown.SchedulerSettings, settings) ||
		!reflect.DeepEqual(shown.StaticRouting, map[string]map[string]int{"stored-uid": {"physical": 1}}) ||
		shown.PowerObservation["coverage"]["MinimumCoverage"] != 0.99 ||
		!reflect.DeepEqual(shown.Meters, meters) || shown.RequestTimeout != handler.config.RequestTimeout {
		t.Fatalf("settings GET lost settings or read-only declarations: %s", response.Body)
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource == "configmaps" && action.GetVerb() != "get" {
			t.Fatalf("settings GET mutated ConfigMaps: %s", action.GetVerb())
		}
	}
}
