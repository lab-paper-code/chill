package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestEvaluationMutationRequiresAuthorizationAndDurableAudit(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		kube := fake.NewSimpleClientset()
		kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
				Authenticated: true, User: authentication.UserInfo{Username: "operator"},
			}}, nil
		})
		kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
			review := action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview)
			if review.Spec.NonResourceAttributes.Path != "/chill/api/v1/evaluations" ||
				review.Spec.NonResourceAttributes.Verb != "post" {
				return true, &authorization.SubjectAccessReview{}, nil
			}
			status := authorization.SubjectAccessReviewStatus{Allowed: allowed}
			return true, &authorization.SubjectAccessReview{Status: status}, nil
		})
		calls := 0
		h := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{MaxRequestBytes: 1024,
			CreateEvaluation: func(context.Context, EvaluationRequest, string) (EvaluationIdentity, error) {
				calls++
				return EvaluationIdentity{}, nil
			},
		}}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/evaluations", strings.NewReader(`{"name":"run","spec":{}}`))
		request.Header.Set("Authorization", "Bearer caller")
		response := httptest.NewRecorder()
		h.evaluations(response, request)
		if calls != 0 {
			t.Fatal("mutation executed before authorization/audit")
		}
		if allowed {
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("audit failure status=%d body=%s", response.Code, response.Body)
			}
		} else if response.Code != http.StatusForbidden {
			t.Fatalf("authorization status=%d", response.Code)
		}
	}
}
