package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const measurementReadNullJSON = "null"

func TestMeasurementPassportPendingIsNotAnEvidenceConnectionFailure(t *testing.T) {
	for _, state := range []string{
		"running", "pending", operatorconsole.MeasurementFailed, "previous generation", "invalid evidence", "published",
	} {
		t.Run(state, func(t *testing.T) {
			run := &api.ProfilingRun{
				ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "run", UID: "run-uid", Generation: 2},
				Status: api.ProfilingRunStatus{ObservedGeneration: 2, Conditions: []meta.Condition{{
					Type: api.ProfilingRunConditionExecutionComplete, Status: meta.ConditionUnknown,
					ObservedGeneration: 2, Reason: "JobRunning", Message: "measurement Job has not completed",
				}}},
			}
			switch state {
			case "pending":
				run.Status.Conditions[0].Reason = "JobPending"
			case operatorconsole.MeasurementFailed:
				run.Status.Conditions[0].Status = meta.ConditionFalse
				run.Status.Conditions[0].Reason = "JobFailed"
			case "previous generation":
				run.Status.ObservedGeneration = 1
			case "invalid evidence":
				run.Status.Conditions = append(run.Status.Conditions, meta.Condition{
					Type: api.ProfilingRunConditionEvidenceVerified, Status: meta.ConditionFalse,
					ObservedGeneration: 2, Reason: "EvidenceInvalid",
				})
			case "published":
				run.Status.Evidence = &api.ProfilingRunEvidenceReference{Store: "store", Key: "execution.json", Size: 8}
			}
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				consoleError(w, http.StatusBadGateway, "EvidencePassportUnavailable")
			}))
			defer upstream.Close()
			handler := measurementReadHandler(t, upstream.URL, run)
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/namespaces/allowed/profiling-runs/run/evidence-passport", nil)
			request.Header.Set("Authorization", "Bearer token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if state != "running" && state != "pending" {
				if response.Code != http.StatusBadGateway || calls != 1 {
					t.Fatalf("actual evidence failure hidden: status=%d calls=%d body=%s",
						response.Code, calls, response.Body.String())
				}
				return
			}
			var body struct {
				Code        string                                `json:"code"`
				Run         operatorconsole.EvidencePassportRun   `json:"run"`
				Assessment  operatorconsole.MeasurementAssessment `json:"assessment"`
				Measurement json.RawMessage                       `json:"measurement"`
				Evidence    json.RawMessage                       `json:"evidence"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusAccepted || calls != 0 || body.Code != "EvidencePassportPending" ||
				body.Run.UID != "run-uid" || body.Run.Conditions[0].Reason != run.Status.Conditions[0].Reason ||
				body.Assessment.DisplayStatus != operatorconsole.MeasurementRunning || body.Assessment.Evidence.Accepted ||
				string(body.Measurement) != measurementReadNullJSON || string(body.Evidence) != measurementReadNullJSON {
				t.Fatalf("pending evidence misrepresented: status=%d calls=%d body=%s",
					response.Code, calls, response.Body.String())
			}
		})
	}
}

func TestMeasurementResultFailureRetainsNativeRequestWithoutInventingEvidence(t *testing.T) {
	const missingOrigin = "missing Console origin"
	for _, state := range []string{
		operatorconsole.MeasurementFailed, liveOperationComplete, "transport unavailable", missingOrigin,
	} {
		t.Run(state, func(t *testing.T) {
			task := &api.BatchEnergyOptimization{
				ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "request", UID: "request-uid", Generation: 1},
				Status: api.BatchEnergyOptimizationStatus{
					ObservedGeneration: 1, Phase: "Failed", Outcome: "RetryLimitExhausted", CompletedTrials: 3,
					Conditions: []meta.Condition{{Type: "MeasurementComplete", Status: meta.ConditionFalse,
						ObservedGeneration: 1, Reason: "RetryLimitExhausted", Message: "runtime failed repeatedly"}},
				},
			}
			if state == liveOperationComplete {
				task.Status.Phase = "Complete"
				task.Status.Result = &api.ProfilingRunEvidenceReference{Store: "store", Key: "result.json", Size: 100}
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadGateway)
				_ = json.NewEncoder(w).Encode(map[string]string{
					"code":   "BatchEnergyOptimizationResultUnavailable",
					"reason": "completed Run measurement contradicts result",
				})
			}))
			defer upstream.Close()
			if state == "transport unavailable" {
				upstream.Close()
			}
			origin := upstream.URL
			if state == missingOrigin {
				origin = ""
			}
			handler := measurementReadHandler(t, origin, task)
			request := httptest.NewRequest(http.MethodGet,
				"/api/v1/namespaces/allowed/batch-energy-optimizations/request/result", nil)
			request.Header.Set("Authorization", "Bearer token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			var body struct {
				Code          string                                                `json:"code"`
				Reason        string                                                `json:"reason"`
				Campaign      operatorconsole.BatchEnergyOptimizationResultCampaign `json:"campaign"`
				Assessment    operatorconsole.MeasurementAssessment                 `json:"assessment"`
				Result        json.RawMessage                                       `json:"result"`
				EvidenceError struct {
					Code   string `json:"code"`
					Status int    `json:"status"`
				} `json:"evidenceError"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantStatus, wantCode := http.StatusBadGateway, "BatchEnergyOptimizationResultUnavailable"
			if state == "transport unavailable" {
				wantCode = consoleUnavailableCode
			} else if state == missingOrigin {
				wantStatus, wantCode = http.StatusServiceUnavailable, consoleUnavailableCode
			}
			if response.Code != wantStatus || body.Code != wantCode || body.EvidenceError.Code != wantCode ||
				body.EvidenceError.Status != wantStatus || body.Campaign.UID != string(task.UID) ||
				body.Campaign.Phase != task.Status.Phase || body.Campaign.Outcome != task.Status.Outcome ||
				body.Campaign.CompletedTrials != 3 || body.Campaign.Conditions[0].Message != "runtime failed repeatedly" ||
				string(body.Result) != measurementReadNullJSON || body.Assessment.Evidence.Accepted {
				t.Fatalf("request lost or evidence failure suppressed: status=%d body=%s", response.Code, response.Body.String())
			}
			if (state == operatorconsole.MeasurementFailed || state == liveOperationComplete) &&
				body.Reason != "completed Run measurement contradicts result" {
				t.Fatalf("upstream reason lost: %s", response.Body.String())
			}
			if state == operatorconsole.MeasurementFailed &&
				(body.Assessment.DisplayStatus != operatorconsole.MeasurementProblem ||
					len(body.Assessment.FailureSummary) == 0) {
				t.Fatalf("native failure cause missing: %+v", body.Assessment)
			}
		})
	}
}

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
	opts := options{servingNamespace: "allowed", consoleURL: origin, requestTimeout: time.Second, maxRequestBytes: 1 << 20}
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
