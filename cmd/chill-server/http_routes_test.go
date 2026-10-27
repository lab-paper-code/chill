package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestConsoleChillServerAuthenticatesBeforePrivateLeg(t *testing.T) {
	for _, tc := range []struct {
		name                                                                       string
		tokenFailure, invalid, denied, sarFailure, evaluationFailure, unconfigured bool
		want                                                                       int
	}{
		{name: "allowed original conflict", want: http.StatusConflict},
		{name: "TokenReview outage", tokenFailure: true, want: http.StatusServiceUnavailable},
		{name: "invalid identity", invalid: true, want: http.StatusUnauthorized},
		{name: "real denial", denied: true, want: http.StatusForbidden},
		{name: "SAR outage", sarFailure: true, want: http.StatusServiceUnavailable},
		{name: "evaluation outage", evaluationFailure: true, want: http.StatusServiceUnavailable},
		{name: "missing query dependency", unconfigured: true, want: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var privateCalls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				privateCalls.Add(1)
				for _, key := range []string{
					"Authorization", "Cookie", "Impersonate-User", "Impersonate-Uid",
					"X-Chill-Actor", "X-Remote-User", "X-Forwarded-User", "Forwarded",
				} {
					if r.Header.Get(key) != "" {
						t.Errorf("private leg leaked %s", key)
					}
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte("original conflict body"))
			}))
			defer upstream.Close()
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				if action.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview).Spec.Token != "private-token" {
					t.Error("wrong review token")
				}
				if tc.tokenFailure {
					return true, nil, errors.New("private-token credential outage")
				}
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
					Authenticated: !tc.invalid,
					User:          authenticationv1.UserInfo{Username: "reviewed-user", UID: "reviewed-uid"},
				}}, nil
			})
			sarCalls := 0
			client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				sarCalls++
				spec := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview).Spec
				if spec.User != "reviewed-user" || spec.UID != "reviewed-uid" ||
					spec.NonResourceAttributes.Path != "/chill/api/v1/inventory" || spec.NonResourceAttributes.Verb != "get" {
					t.Errorf("unverified or wrong authorization target: %+v", spec)
				}
				if tc.sarFailure {
					return true, nil, errors.New("private-token credential outage")
				}
				status := authorizationv1.SubjectAccessReviewStatus{Allowed: !tc.denied, Denied: tc.denied}
				if tc.evaluationFailure {
					status.EvaluationError = "private-token evaluation unavailable"
				}
				return true, &authorizationv1.SubjectAccessReview{Status: status}, nil
			})
			origin := upstream.URL
			if tc.unconfigured {
				origin = ""
			}
			chillServer, err := composeChillServer(http.NotFoundHandler(), &servingkube.Authorizer{Client: client},
				options{consoleURL: origin, requestTimeout: time.Second, maxRequestBytes: 1024})
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/inventory?limit=1&q=node", nil)
			request.Header.Set("Authorization", "Bearer private-token")
			for _, key := range []string{
				"Cookie", "Impersonate-User", "Impersonate-Uid", "X-Chill-Actor",
				"X-Remote-User", "X-Forwarded-User", "Forwarded",
			} {
				request.Header.Set(key, "forged-admin")
			}
			response := httptest.NewRecorder()
			chillServer.ServeHTTP(response, request)
			if response.Code != tc.want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private-token") ||
				strings.Contains(response.Body.String(), "forged-admin") {
				t.Fatal("credential or forged actor leaked")
			}
			if tc.want == http.StatusConflict {
				if privateCalls.Load() != 1 {
					t.Fatal("authorized query did not reach private Console")
				}
			} else if privateCalls.Load() != 0 {
				t.Fatal("failed authorization reached private Console")
			}
			if (tc.invalid || tc.tokenFailure) && sarCalls != 0 {
				t.Fatal("invalid identity reached authorization")
			}
		})
	}
}

func TestConsoleChillServerDoesNotFollowRedirectsOrArbitraryOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://user:secret@console", "http://console/path", "http://console?upstream=other",
		"http://console#fragment", "file:///secret",
	} {
		if _, err := privateOrigin(origin); err == nil {
			t.Fatalf("accepted arbitrary origin %q", origin)
		}
	}
	var redirectTargetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirectTargetCalls.Add(1) }))
	defer target.Close()
	console := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = w.Write([]byte("original redirect body"))
	}))
	defer console.Close()
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: "user"},
		}}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{
			Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true},
		}, nil
	})
	chillServer, err := composeChillServer(http.NotFoundHandler(), &servingkube.Authorizer{Client: client},
		options{consoleURL: console.URL, requestTimeout: time.Second, maxRequestBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/systems", nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	chillServer.ServeHTTP(response, request)
	if response.Code != http.StatusTemporaryRedirect || redirectTargetCalls.Load() != 0 {
		t.Fatal("private redirect was followed")
	}
}

func TestDashboardGatewayCannotReplaceChillServerAuthorization(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	authorizer := &servingkube.Authorizer{}
	native, err := httpapi.NewHandler(&servingkube.Manager{}, &serving.Runtime{}, authorizer,
		&serving.ObservationLog{}, &servingkube.Observer{}, httpapi.HandlerConfig{
			RequestTimeout: time.Second, OperationTimeout: time.Second, MaxRequestBytes: 1024, MaxQueryEvents: 10,
		})
	if err != nil {
		t.Fatal(err)
	}
	chillServer, err := composeChillServer(native, authorizer, options{dashboardURL: web.URL, requestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/models", "/api/v1/inventory"} {
		response := httptest.NewRecorder()
		chillServer.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("public web fallback bypassed %s authorization: %d", path, response.Code)
		}
	}
	web.Close()
	response := httptest.NewRecorder()
	chillServer.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/models", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("web outage changed native owner authorization: %d", response.Code)
	}
}

func TestDashboardGatewayPreservesLiveUpgradeOriginAndHost(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/api/live" || r.Host != "public.example:30443" ||
			r.Header.Get("Origin") != "https://public.example:30443" ||
			!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			t.Errorf("live request changed: %s host=%s headers=%v", r.URL, r.Host, r.Header)
		}
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		_, _ = conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"))
	}))
	defer web.Close()
	handler, err := composeChillServer(http.NotFoundHandler(), &servingkube.Authorizer{}, options{dashboardURL: web.URL})
	if err != nil {
		t.Fatal(err)
	}
	public := httptest.NewServer(handler)
	defer public.Close()
	request, err := http.NewRequest(http.MethodGet, public.URL+"/api/live", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "public.example:30443"
	request.Header.Set("Origin", "https://public.example:30443")
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	response, err := public.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade status=%d", response.StatusCode)
	}
}
