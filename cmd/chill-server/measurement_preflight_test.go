package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const measurementPreflightTestPath = "/api/v1/namespaces/allowed/batch-energy-optimizations/preflight"

func TestPublicPreflightRequiresNamespaceListBeforePrivateRead(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		t.Run(map[bool]string{false: "denied", true: "allowed"}[allowed], func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.RawQuery != "variantId=selected" || r.Header.Get("Authorization") != "" {
					t.Error("preflight display context or private authentication boundary changed")
				}
				snapshot := queryapi.BatchEnergyOptimizationPreflightSnapshot{
					Campaign: api.BatchEnergyOptimization{
						ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "new-request"},
					},
					Reason: "actual unresolved request",
				}
				view, err := queryapi.BuildBatchEnergyOptimizationPreflight(snapshot)
				if err != nil {
					t.Fatal(err)
				}
				_ = json.NewEncoder(w).Encode(view)
			}))
			defer upstream.Close()
			native := kubefake.NewSimpleClientset()
			native.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
					Authenticated: true, User: authenticationv1.UserInfo{Username: "operator"},
				}}, nil
			})
			resourceReviews := 0
			native.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				permitted := true
				if resource := review.Spec.ResourceAttributes; resource != nil {
					resourceReviews++
					if resource.Namespace != "allowed" || resource.Group != "edge.dacs.io" ||
						resource.Resource != "batchenergyoptimizations" || resource.Verb != "list" {
						t.Errorf("wrong preflight resource authorization: %#v", resource)
					}
					permitted = allowed
				}
				return true, &authorizationv1.SubjectAccessReview{
					Status: authorizationv1.SubjectAccessReviewStatus{Allowed: permitted},
				}, nil
			})
			opts := options{
				queryURL: upstream.URL, servingNamespace: "allowed", requestTimeout: time.Second, maxRequestBytes: 1024,
			}
			handler, err := composeChillServer(
				http.NotFoundHandler(), &servingkube.Authorizer{Client: native}, opts, &measurementService{opts: opts},
			)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(
				http.MethodPost, measurementPreflightTestPath+"?variantId=selected", strings.NewReader("{}"),
			)
			request.Header.Set("Authorization", "Bearer token")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			wantStatus, wantCalls := http.StatusForbidden, 0
			if allowed {
				wantStatus, wantCalls = http.StatusOK, 1
			}
			if response.Code != wantStatus || calls != wantCalls || resourceReviews != 1 {
				t.Fatalf("status=%d calls=%d reviews=%d body=%s", response.Code, calls, resourceReviews, response.Body.String())
			}
		})
	}
}
