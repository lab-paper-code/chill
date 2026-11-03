package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	batch "k8s.io/api/batch/v1"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMeasurementEvidenceReadsPreserveSuccessAndAuthentication(t *testing.T) {
	const published = `{"kind":"BatchEnergyOptimizationResult","result":{"outcome":"MinimumFound"}}`
	calls := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(published))
	}))
	defer upstream.Close()
	handler := measurementReadHandler(t, upstream.URL)
	for _, path := range []string{
		"/api/v1/namespaces/allowed/profiling-runs/run/evidence-passport",
		"/api/v1/namespaces/allowed/batch-energy-optimizations/request/result",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized || calls != 0 {
			t.Fatalf("metadata bypassed authentication: status=%d calls=%d", response.Code, calls)
		}
	}
	request := httptest.NewRequest(http.MethodGet,
		"/api/v1/namespaces/allowed/batch-energy-optimizations/request/result", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != published || calls != 1 {
		t.Fatalf("published result replaced: status=%d calls=%d body=%s", response.Code, calls, response.Body.String())
	}
}

func measurementReadHandler(t *testing.T, origin string, objects ...client.Object) http.Handler {
	t.Helper()
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(objects...).Build()
	opts := options{
		servingNamespace: "allowed",
		queryURL:         origin,
		requestTimeout:   time.Second,
		maxRequestBytes:  1 << 20,
	}
	service, err := newMeasurementService(native, nil, nil, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	reviews := kubefake.NewSimpleClientset()
	reviews.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: "reviewed-user"},
		}}, nil
	})
	reviews.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
		}, nil
	})
	handler, err := composeChillServer(http.NotFoundHandler(), &servingkube.Authorizer{Client: reviews}, opts, service)
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestQueryBackendDownDoesNotReadNativeMeasurementMetadata(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	origin := upstream.URL
	upstream.Close()
	handler := measurementReadHandler(t, origin)
	request := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/namespaces/allowed/batch-energy-optimizations/request/result",
		nil,
	)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusBadGateway || string(body["code"]) != `"ConsoleUnavailable"` ||
		body["campaign"] != nil ||
		body["assessment"] != nil ||
		body["evidenceError"] != nil {
		t.Fatalf("backend-down was disguised as native evidence failure: %d %s", response.Code, response.Body.String())
	}
	t.Logf("backend-down: status=%d body=%s", response.Code, response.Body.String())
}

func measurementScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batch.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}
