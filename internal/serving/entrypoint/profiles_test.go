package entrypoint

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

func TestProfilePostRequiresNamedConfigMapUpdateBeforeSelection(t *testing.T) {
	const actorUsername = "operator"
	for _, allowed := range []bool{false, true} {
		kube := fake.NewSimpleClientset()
		kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
				Authenticated: true,
				User:          authentication.UserInfo{Username: actorUsername, UID: "actor", Groups: []string{"editors"}},
			}}, nil
		})
		resourceReviews := 0
		kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
			review := action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview)
			if resource := review.Spec.ResourceAttributes; resource != nil {
				resourceReviews++
				if resource.Namespace != "serving" || resource.Name != "scheduler" ||
					resource.Resource != "configmaps" || resource.Verb != "update" || review.Spec.User != actorUsername {
					t.Fatalf("incorrect resource grant: %+v", review.Spec)
				}
				return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: allowed}}, nil
			}
			return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
		})
		selections := 0
		handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{
			SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler", MaxRequestBytes: 1 << 20,
			ProfileSelections: func(context.Context) (ProfileSelectionsView, error) { return ProfileSelectionsView{}, nil },
			SelectProfile: func(_ context.Context, request ProfileSelectionRequest, principal servingkube.Principal) (ProfileSelectionResult, error) {
				selections++
				username, uid := principal.Actor()
				if username != actorUsername || uid != "actor" || request.Campaign.Name != "pending" {
					t.Fatal("lost authenticated actor or campaign")
				}
				return ProfileSelectionResult{Campaign: request.Campaign, Status: "Pending", Code: "MeasurementPending", Reason: "actual measuring phase"}, nil
			},
		}}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/scheduler/profiles", strings.NewReader(`{"campaign":{"namespace":"measurements","name":"pending"}}`))
		request.Header.Set("Authorization", "Bearer reviewed-token")
		response := httptest.NewRecorder()
		handler.schedulerProfiles(response, request)
		want := http.StatusForbidden
		if allowed {
			want = http.StatusOK
		}
		if response.Code != want || resourceReviews != 1 || (selections == 1) != allowed {
			t.Fatalf("allowed=%v code=%d reviews=%d selections=%d body=%s", allowed, response.Code, resourceReviews, selections, response.Body)
		}
		if allowed && (!strings.Contains(response.Body.String(), `"persisted":false`) || !strings.Contains(response.Body.String(), `actual measuring phase`)) {
			t.Fatal("pending response claimed selection success or lost reason")
		}
	}
}
