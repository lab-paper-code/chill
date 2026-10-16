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

const (
	actorUsername   = "operator"
	profileActorUID = "actor"
)

func TestProfilePostRequiresNamedConfigMapUpdateBeforeSelection(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		kube := fake.NewSimpleClientset()
		kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
				Authenticated: true,
				User:          authentication.UserInfo{Username: actorUsername, UID: profileActorUID, Groups: []string{"editors"}},
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
				return true, &authorization.SubjectAccessReview{
					Status: authorization.SubjectAccessReviewStatus{Allowed: allowed},
				}, nil
			}
			return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
		})
		selections := 0
		handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{
			SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler", MaxRequestBytes: 1 << 20,
			ProfileSelections: func(context.Context) (ProfileSelectionsView, error) { return ProfileSelectionsView{}, nil },
			SelectProfile: func(
				_ context.Context, request ProfileSelectionRequest, principal servingkube.Principal,
			) (ProfileSelectionResult, error) {
				selections++
				username, uid := principal.Actor()
				if username != actorUsername || uid != profileActorUID || request.Campaign.Name != "pending" {
					t.Fatal("lost authenticated actor or campaign")
				}
				return ProfileSelectionResult{
					Campaign: request.Campaign, Status: "Pending", Code: "MeasurementPending", Reason: "actual measuring phase",
				}, nil
			},
		}}
		request := httptest.NewRequest(http.MethodPost, "/api/v1/scheduler/profiles",
			strings.NewReader(`{"campaign":{"namespace":"measurements","name":"pending"}}`))
		request.Header.Set("Authorization", "Bearer reviewed-token")
		response := httptest.NewRecorder()
		handler.schedulerProfiles(response, request)
		want := http.StatusForbidden
		if allowed {
			want = http.StatusOK
		}
		if response.Code != want || resourceReviews != 1 || (selections == 1) != allowed {
			t.Fatalf("allowed=%v code=%d reviews=%d selections=%d body=%s",
				allowed, response.Code, resourceReviews, selections, response.Body)
		}
		if allowed && (!strings.Contains(response.Body.String(), `"persisted":false`) ||
			!strings.Contains(response.Body.String(), `actual measuring phase`)) {
			t.Fatal("pending response claimed selection success or lost reason")
		}
	}
}

func TestProfileDeleteRequiresCallerNamedGrantAndQueriedConfigVersion(t *testing.T) {
	for _, test := range []struct {
		name, version                  string
		authenticated, resourceAllowed bool
		want                           int
	}{
		{name: "no caller", version: "7", want: http.StatusUnauthorized},
		{name: "resource denied", authenticated: true, version: "7", want: http.StatusForbidden},
		{name: "missing version", authenticated: true, resourceAllowed: true, want: http.StatusBadRequest},
		{name: "wildcard version", authenticated: true, resourceAllowed: true, version: "*", want: http.StatusBadRequest},
		{
			name: "multiple versions", authenticated: true, resourceAllowed: true,
			version: `"7", "8"`, want: http.StatusBadRequest,
		},
		{
			name: "reviewed caller and version", authenticated: true, resourceAllowed: true,
			version: `"7"`, want: http.StatusOK,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			kube := fake.NewSimpleClientset()
			kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
					Authenticated: test.authenticated,
					User:          authentication.UserInfo{Username: actorUsername, UID: profileActorUID, Groups: []string{"editors"}},
				}}, nil
			})
			resourceReviews := 0
			kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview)
				if resource := review.Spec.ResourceAttributes; resource != nil {
					resourceReviews++
					if resource.Namespace != "serving" || resource.Name != "scheduler" || resource.Resource != "configmaps" ||
						resource.Verb != "update" || review.Spec.User != actorUsername || review.Spec.UID != profileActorUID {
						t.Fatalf("profile removal requested another actor or resource grant: %+v", review.Spec)
					}
					return true, &authorization.SubjectAccessReview{
						Status: authorization.SubjectAccessReviewStatus{Allowed: test.resourceAllowed},
					}, nil
				}
				return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
			})
			removals := 0
			handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{
				SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler",
				RemoveProfile: func(
					_ context.Context, campaign ProfileCampaign, version string, principal servingkube.Principal,
				) (ProfileSelectionResult, error) {
					removals++
					username, uid := principal.Actor()
					if username != actorUsername || uid != profileActorUID || version != "7" ||
						campaign.Namespace != "measurements" || campaign.Name != "unused-gpu" {
						t.Fatalf("authorized removal lost actor, native version, or exact campaign: %+v %q %s/%s",
							campaign, version, username, uid)
					}
					return ProfileSelectionResult{Status: "Removed"}, nil
				},
			}}
			mux := http.NewServeMux()
			mux.HandleFunc("DELETE /api/v1/scheduler/profiles/{namespace}/{name}", handler.removeSchedulerProfile)
			request := httptest.NewRequest(http.MethodDelete, "/api/v1/scheduler/profiles/measurements/unused-gpu", nil)
			if test.authenticated {
				request.Header.Set("Authorization", "Bearer reviewed-token")
			}
			if test.version != "" {
				request.Header.Set("If-Match", test.version)
			}
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.want || (removals == 1) != (test.want == http.StatusOK) {
				t.Fatalf("status=%d want=%d removals=%d body=%s", response.Code, test.want, removals, response.Body)
			}
			if test.authenticated && resourceReviews != 1 {
				t.Fatalf("named ConfigMap authorization checks=%d", resourceReviews)
			}
		})
	}
}

func TestProfileDeleteReportsReferenceAndControlsConflicts(t *testing.T) {
	for _, code := range []string{"ProfileInUse", "ProfileControlsEnabled"} {
		kube := fake.NewSimpleClientset()
		kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
				Authenticated: true, User: authentication.UserInfo{Username: actorUsername, UID: profileActorUID},
			}}, nil
		})
		kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
		})
		handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{
			SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler",
			RemoveProfile: func(
				context.Context, ProfileCampaign, string, servingkube.Principal,
			) (ProfileSelectionResult, error) {
				return ProfileSelectionResult{Status: "Rejected", Code: code, Persisted: true, Loaded: true}, nil
			},
		}}
		request := httptest.NewRequest(http.MethodDelete, "/api/v1/scheduler/profiles/measurements/current", nil)
		request.SetPathValue("namespace", "measurements")
		request.SetPathValue("name", "current")
		request.Header.Set("Authorization", "Bearer reviewed-token")
		request.Header.Set("If-Match", "7")
		response := httptest.NewRecorder()
		handler.removeSchedulerProfile(response, request)
		if response.Code != http.StatusConflict {
			t.Fatalf("%s status=%d body=%s", code, response.Code, response.Body)
		}
	}
}
