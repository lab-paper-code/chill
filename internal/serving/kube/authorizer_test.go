package kube

import (
	"context"
	"errors"
	"testing"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestAuthorizerUsesReviewedIdentityAndFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name            string
		authenticated   bool
		tokenFailure    bool
		allowed         bool
		sarFailure      bool
		evaluationError string
		tokenError      string
		denied          bool
		want            error
	}{
		{name: "allowed", authenticated: true, allowed: true},
		{name: "invalid identity", want: ErrUnauthenticated},
		{name: "TokenReview outage", tokenFailure: true, want: ErrAuthorizationUnavailable},
		{name: "rejected token", tokenError: "invalid bearer token", want: ErrUnauthenticated},
		{name: "inconsistent TokenReview", authenticated: true, tokenError: "service unavailable", want: ErrAuthorizationUnavailable},
		{name: "denied", authenticated: true, want: ErrForbidden},
		{name: "explicit denial", authenticated: true, allowed: true, denied: true, want: ErrForbidden},
		{name: "SAR outage", authenticated: true, sarFailure: true, want: ErrAuthorizationUnavailable},
		{name: "partial SAR evaluation", authenticated: true, allowed: true, evaluationError: "evaluation incomplete", want: ErrAuthorizationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				obj := action.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
				if obj.Spec.Token != "secret-token" {
					t.Fatal("bearer token not sent to TokenReview")
				}
				if tc.tokenFailure {
					return true, nil, errors.New("unavailable")
				}
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
					Authenticated: tc.authenticated,
					Error:         tc.tokenError,
					User:          authenticationv1.UserInfo{Username: "reviewed-user", UID: "reviewed-uid", Groups: []string{"operators"}, Extra: map[string]authenticationv1.ExtraValue{"scope": {nativeServingMode}}},
				}}, nil
			})
			sarCalls := 0
			client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				sarCalls++
				obj := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				if obj.Spec.User != "reviewed-user" || obj.Spec.UID != "reviewed-uid" || len(obj.Spec.Groups) != 1 || obj.Spec.Groups[0] != "operators" || obj.Spec.Extra["scope"][0] != nativeServingMode {
					t.Fatalf("unexpected reviewed identity: %#v", obj.Spec)
				}
				if obj.Spec.ResourceAttributes != nil || obj.Spec.NonResourceAttributes.Path != "/chill/api/v1/workloads/detector/infer" || obj.Spec.NonResourceAttributes.Verb != "post" {
					t.Fatalf("unexpected authorization attributes: %#v", obj.Spec)
				}
				if tc.sarFailure {
					return true, nil, errors.New("unavailable")
				}
				return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: tc.allowed, Denied: tc.denied, EvaluationError: tc.evaluationError}}, nil
			})
			a := Authorizer{Client: client}
			principal, err := a.Authorize(context.Background(), "secret-token", "POST", "/api/v1/workloads/detector/infer")
			if !errors.Is(err, tc.want) {
				t.Fatalf("Authorize() = %v, want %v", err, tc.want)
			}
			if tc.want == nil && (principal.username != "reviewed-user" || principal.uid != "reviewed-uid") {
				t.Fatalf("verified principal lost: %+v", principal)
			}
			if tc.want != nil && principal != (Principal{}) {
				t.Fatalf("failed review returned principal: %+v", principal)
			}
			if (!tc.authenticated || tc.tokenFailure || tc.tokenError != "") && sarCalls != 0 {
				t.Fatal("unauthenticated identity reached SAR")
			}
		})
	}
}

func TestAuthorizerDoesNotInheritKubernetesDiscoveryAccess(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true,
			User:          authenticationv1.UserInfo{Username: "read-denied", UID: "read-denied-uid", Groups: []string{"system:authenticated"}},
		}}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
		path := review.Spec.NonResourceAttributes.Path
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{
			Allowed: path == "/api/v1/models" || path == "/chill/api/v1/workloads",
		}}, nil
	})
	authorizer := Authorizer{Client: client}
	if principal, err := authorizer.Authorize(context.Background(), "verified-token", "GET", "/api/v1/models"); !errors.Is(err, ErrForbidden) || principal != (Principal{}) {
		t.Fatalf("Kubernetes discovery grant authorized CHILL models: principal=%+v err=%v", principal, err)
	}
	if _, err := authorizer.Authorize(context.Background(), "verified-token", "GET", "/api/v1/workloads"); err != nil {
		t.Fatalf("explicit CHILL grant denied: %v", err)
	}
}
