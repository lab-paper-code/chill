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
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func experimentHandler(t *testing.T, allowed bool, config HandlerConfig) *Handler {
	t.Helper()
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{Authenticated: true, User: authentication.UserInfo{Username: "experiment-reader"}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview)
		if review.Spec.ResourceAttributes != nil || review.Spec.User != "experiment-reader" ||
			review.Spec.NonResourceAttributes.Verb != measurementGetVerb ||
			(review.Spec.NonResourceAttributes.Path != "/chill/api/v1/experiments" && review.Spec.NonResourceAttributes.Path != "/chill/api/v1/experiments/run-1") {
			t.Fatalf("incorrect experiment non-resource authorization: %+v", review.Spec)
		}
		return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: allowed}}, nil
	})
	return &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: config}
}

func TestExperimentsAuthorizationAndContext(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, allowed := range []bool{false, true} {
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			deadlineCtx, deadlineCancel := context.WithTimeout(ctx, time.Minute)
			defer deadlineCancel()
			check := func(got context.Context) {
				calls++
				if _, ok := got.Deadline(); !ok {
					t.Fatal("request deadline was lost")
				}
				cancel()
				if got.Err() != context.Canceled {
					t.Fatal("request cancellation was lost")
				}
			}
			h := experimentHandler(t, allowed, HandlerConfig{
				ListExperiments: func(ctx context.Context) (ExperimentList, error) {
					check(ctx)
					return ExperimentList{Items: []ExperimentSummary{}}, nil
				},
				GetExperiment: func(ctx context.Context, id string) (ExperimentSummary, error) {
					check(ctx)
					if id != "run-1" {
						t.Fatalf("id=%q", id)
					}
					return ExperimentSummary{ID: id, Status: "NOT_READY"}, nil
				},
			})
			r := httptest.NewRequest(http.MethodGet, "/api/v1/experiments", nil).WithContext(deadlineCtx)
			if detail {
				r = httptest.NewRequest(http.MethodGet, "/api/v1/experiments/run-1", nil).WithContext(deadlineCtx)
				r.SetPathValue("id", "run-1")
			}
			r.Header.Set("Authorization", "Bearer caller")
			w := httptest.NewRecorder()
			h.experiments(w, r)
			if !allowed {
				if w.Code != http.StatusForbidden || calls != 0 {
					t.Fatalf("denied read: code=%d calls=%d", w.Code, calls)
				}
				continue
			}
			if w.Code != http.StatusOK || calls != 1 {
				t.Fatalf("authorized read: code=%d calls=%d body=%s", w.Code, calls, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if detail {
				if body["id"] != "run-1" || body["status"] != "NOT_READY" {
					t.Fatalf("detail=%v", body)
				}
			} else if items, ok := body["items"].([]any); !ok || len(items) != 0 {
				t.Fatalf("empty list must be []: %v", body)
			}
		}
	}
}

func TestExperimentsRejectUnsafeIDsBeforeService(t *testing.T) {
	for _, id := range []string{".", "..", "../outside", "a/b", "a\\b", "%2f", ".hidden", "https:host", "white space"} {
		h := experimentHandler(t, true, HandlerConfig{GetExperiment: func(context.Context, string) (ExperimentSummary, error) {
			t.Fatal("unsafe id reached store callback")
			return ExperimentSummary{}, nil
		}})
		r := httptest.NewRequest(http.MethodGet, "/api/v1/experiments/run-1", nil)
		r.SetPathValue("id", id)
		r.Header.Set("Authorization", "Bearer caller")
		w := httptest.NewRecorder()
		h.experiments(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("id=%q code=%d", id, w.Code)
		}
	}
}

func TestExperimentsMissingUnavailableAndUnauthenticated(t *testing.T) {
	for _, tc := range []struct {
		name string
		auth bool
		err  error
		code int
	}{
		{"missing", true, &serving.Error{Code: "NotFound", Message: "experiment summary not found"}, http.StatusNotFound},
		{"store failure", true, &serving.Error{Code: "ExperimentUnavailable"}, http.StatusServiceUnavailable},
		{"unauthenticated", false, nil, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := experimentHandler(t, true, HandlerConfig{GetExperiment: func(context.Context, string) (ExperimentSummary, error) {
				calls++
				return ExperimentSummary{}, tc.err
			}})
			r := httptest.NewRequest(http.MethodGet, "/api/v1/experiments/run-1", nil)
			r.SetPathValue("id", "run-1")
			if tc.auth {
				r.Header.Set("Authorization", "Bearer caller")
			}
			w := httptest.NewRecorder()
			h.experiments(w, r)
			if w.Code != tc.code || !tc.auth && calls != 0 {
				t.Fatalf("code=%d calls=%d", w.Code, calls)
			}
		})
	}
}
