package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nativefake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authn "k8s.io/api/authentication/v1"
	authz "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func queryTestAuthorizer(t *testing.T) *servingkube.Authorizer {
	t.Helper()
	client := kubefake.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		token := action.(ktesting.CreateAction).GetObject().(*authn.TokenReview).Spec.Token
		user := "reviewed-user"
		if token == "denied" {
			user = "denied-user"
		}
		return true, &authn.TokenReview{
			Status: authn.TokenReviewStatus{Authenticated: token != "invalid", User: authn.UserInfo{
				Username: user, UID: "caller-uid", Groups: []string{"operators"},
				Extra: map[string]authn.ExtraValue{"tenant": {"reviewed"}},
			}},
		}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		spec := action.(ktesting.CreateAction).GetObject().(*authz.SubjectAccessReview).Spec
		if spec.UID != "caller-uid" || len(spec.Groups) != 1 || spec.Groups[0] != "operators" ||
			spec.Extra["tenant"][0] != "reviewed" {
			t.Error("query replaced reviewed identity with service identity or untrusted headers")
		}
		return true, &authz.SubjectAccessReview{
			Status: authz.SubjectAccessReviewStatus{Allowed: spec.User == "reviewed-user"},
		}, nil
	})
	return &servingkube.Authorizer{Client: client}
}

func TestEveryQueryRouteAuthenticatesDirectServiceRequests(t *testing.T) {
	routes := []struct{ method, path string }{
		{"GET", "/api/v1/systems"}, {"GET", "/api/v1/inventory"},
		{"GET", "/api/v1/model-compatibility"}, {"GET", "/api/v1/model-compatibility-matrix"},
		{"GET", "/api/v1/scheduler/profiles"},
		{"GET", "/api/v1/namespaces/allowed/profiling-runs"},
		{"GET", "/api/v1/namespaces/allowed/profiling-runs/run/evidence-passport"},
		{"GET", "/api/v1/namespaces/allowed/batch-energy-optimizations"},
		{"GET", "/api/v1/namespaces/allowed/batch-energy-optimizations/request/result"},
		{"POST", "/api/v1/namespaces/allowed/batch-energy-optimizations/preflight"},
		{"GET", "/api/v1/namespaces/allowed/measurement-coverage"},
		{"GET", "/api/v1/namespaces/allowed/operation-cost-measurements"},
		{"GET", "/api/v1/namespaces/allowed/operation-cost-measurements/item"},
		{"GET", "/api/v1/namespaces/allowed/dispatch-occupancy-measurements"},
		{"GET", "/api/v1/namespaces/allowed/dispatch-occupancy-measurements/item"},
	}
	for _, route := range routes {
		for _, state := range []struct {
			token  string
			status int
		}{
			{"", http.StatusUnauthorized}, {"invalid", http.StatusUnauthorized},
			{"denied", http.StatusForbidden}, {"allowed", http.StatusOK},
		} {
			t.Run(route.path+"/"+state.token, func(t *testing.T) {
				calls := 0
				backend := http.HandlerFunc(
					func(w http.ResponseWriter, _ *http.Request) { calls++; w.WriteHeader(http.StatusOK) },
				)
				handler := authenticatedQueryHandler(backend, queryTestAuthorizer(t), options{
					servingNamespace: queryAllowedNamespace, requestTimeout: time.Second,
				})
				request := httptest.NewRequest(route.method, route.path, nil)
				request.Header.Set("X-Remote-User", "cluster-admin")
				request.Header.Set("Impersonate-User", "system:admin")
				if state.token != "" {
					request.Header.Set("Authorization", "Bearer "+state.token)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != state.status || (calls == 1) != (state.status == http.StatusOK) {
					t.Fatalf(
						"direct query authorization: status=%d calls=%d body=%s",
						response.Code,
						calls,
						response.Body.String(),
					)
				}
			})
		}
	}
}

func TestDirectQueryPreservesNamespaceAndNativeGrants(t *testing.T) {
	for _, mode := range []string{"namespace", "resource"} {
		t.Run(mode, func(t *testing.T) {
			authorizer := queryTestAuthorizer(t)
			if mode == "resource" {
				authorizer.Client.(*kubefake.Clientset).PrependReactor("create", "subjectaccessreviews",
					func(action ktesting.Action) (bool, runtime.Object, error) {
						spec := action.(ktesting.CreateAction).GetObject().(*authz.SubjectAccessReview).Spec
						return true, &authz.SubjectAccessReview{
							Status: authz.SubjectAccessReviewStatus{Allowed: spec.ResourceAttributes == nil},
						}, nil
					})
			}
			calls := 0
			handler := authenticatedQueryHandler(
				http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }),
				authorizer,
				options{servingNamespace: queryAllowedNamespace, requestTimeout: time.Second},
			)
			namespace := queryAllowedNamespace
			if mode == "namespace" {
				namespace = "outside"
			}
			request := httptest.NewRequest("GET", "/api/v1/namespaces/"+namespace+"/measurement-coverage", nil)
			request.Header.Set("Authorization", "Bearer allowed")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || calls != 0 {
				t.Fatalf("query leaked namespace/resource: %d calls=%d", response.Code, calls)
			}
		})
	}
}

func TestQueryCompositionRejectsDirectAnonymousAccess(t *testing.T) {
	scheme := measurementScheme(t)
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := nativefake.NewClientBuilder().WithScheme(scheme).Build()
	handler, err := buildHandler(reader, validOptions(), queryTestAuthorizer(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []struct {
		token  string
		status int
	}{
		{"", http.StatusUnauthorized}, {"denied", http.StatusForbidden}, {"allowed", http.StatusOK},
	} {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/inventory", nil)
		request.Header.Set("X-Remote-User", "admin")
		if state.token != "" {
			request.Header.Set("Authorization", "Bearer "+state.token)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != state.status {
			t.Fatalf("root query status=%d want=%d body=%s", response.Code, state.status, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusOK {
		t.Fatal("health probe requires user credentials")
	}
}

const (
	resourceNamedPreflight = "preflight"
	queryAllowedNamespace  = "allowed"
)

func TestPreflightNamedDetailsRequireGetSAR(t *testing.T) {
	for _, route := range []struct{ method, path, resource, verb, name string }{
		{http.MethodGet, "profiling-runs/preflight/evidence-passport", "profilingruns", "get", resourceNamedPreflight},
		{http.MethodGet, "batch-energy-optimizations/preflight/result",
			"batchenergyoptimizations", "get", resourceNamedPreflight},
		{http.MethodGet, "operation-cost-measurements/preflight", "operationcostmeasurements", "get", resourceNamedPreflight},
		{http.MethodGet, "dispatch-occupancy-measurements/preflight",
			"dispatchoccupancymeasurements", "get", resourceNamedPreflight},
		{http.MethodGet, "batch-energy-optimizations/preflight", "batchenergyoptimizations", "get", resourceNamedPreflight},
		{http.MethodPost, "batch-energy-optimizations/preflight", "batchenergyoptimizations", "list", ""},
	} {
		t.Run(route.method+"/"+route.path, func(t *testing.T) {
			authorizer := queryTestAuthorizer(t)
			reviews, calls := 0, 0
			authorizer.Client.(*kubefake.Clientset).PrependReactor("create", "subjectaccessreviews",
				func(action ktesting.Action) (bool, runtime.Object, error) {
					spec := action.(ktesting.CreateAction).GetObject().(*authz.SubjectAccessReview).Spec
					if attrs := spec.ResourceAttributes; attrs != nil {
						reviews++
						if attrs.Verb != route.verb || attrs.Name != route.name || attrs.Resource != route.resource ||
							attrs.Namespace != queryAllowedNamespace {
							t.Errorf("incorrect detail/preflight SAR: %+v", attrs)
						}
					}
					return true, &authz.SubjectAccessReview{Status: authz.SubjectAccessReviewStatus{Allowed: true}}, nil
				})
			handler := authenticatedQueryHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls++
				w.WriteHeader(http.StatusOK)
			}), authorizer, options{servingNamespace: queryAllowedNamespace, requestTimeout: time.Second})
			request := httptest.NewRequest(route.method, "/api/v1/namespaces/allowed/"+route.path, nil)
			request.Header.Set("Authorization", "Bearer allowed")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK || reviews != 1 || calls != 1 {
				t.Fatalf("status=%d native reviews=%d backend calls=%d", response.Code, reviews, calls)
			}
		})
	}
}

func TestListOnlyCallerCannotReadPreflightNamedDetails(t *testing.T) {
	for _, route := range []string{
		"profiling-runs/preflight/evidence-passport",
		"batch-energy-optimizations/preflight/result",
		"operation-cost-measurements/preflight",
		"dispatch-occupancy-measurements/preflight",
	} {
		t.Run(route, func(t *testing.T) {
			authorizer := queryTestAuthorizer(t)
			authorizer.Client.(*kubefake.Clientset).PrependReactor("create", "subjectaccessreviews",
				func(action ktesting.Action) (bool, runtime.Object, error) {
					spec := action.(ktesting.CreateAction).GetObject().(*authz.SubjectAccessReview).Spec
					allowed := spec.ResourceAttributes == nil || spec.ResourceAttributes.Verb == "list"
					return true, &authz.SubjectAccessReview{
						Status: authz.SubjectAccessReviewStatus{Allowed: allowed},
					}, nil
				})
			calls := 0
			handler := authenticatedQueryHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }),
				authorizer, options{servingNamespace: queryAllowedNamespace, requestTimeout: time.Second})
			request := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/allowed/"+route, nil)
			request.Header.Set("Authorization", "Bearer allowed")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusForbidden || calls != 0 {
				t.Fatalf("list-only caller read named detail: status=%d backend calls=%d", response.Code, calls)
			}
		})
	}
}
