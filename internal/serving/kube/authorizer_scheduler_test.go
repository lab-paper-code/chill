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

func TestSchedulerUpdateRequiresExactReviewedResourceGrant(t *testing.T) {
	for _, tc := range []struct {
		name            string
		allowed, denied bool
		evaluation      string
		failure         bool
		want            error
	}{
		{name: "allowed", allowed: true}, {name: "denied", want: ErrForbidden}, {name: "explicit denial", allowed: true, denied: true, want: ErrForbidden}, {name: "partial", allowed: true, evaluation: "incomplete", want: ErrAuthorizationUnavailable}, {name: "outage", failure: true, want: ErrAuthorizationUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewSimpleClientset()
			resourceCalls := 0
			kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "operator", UID: "uid", Groups: []string{"reviewed-operators"}, Extra: map[string]authenticationv1.ExtraValue{"scope": {"scheduler"}}}}}, nil
			})
			kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				sar := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				if sar.Spec.User != "operator" || sar.Spec.UID != "uid" || len(sar.Spec.Groups) != 1 || sar.Spec.Groups[0] != "reviewed-operators" || sar.Spec.Extra["scope"][0] != "scheduler" {
					t.Fatalf("lost reviewed identity: %+v", sar.Spec)
				}
				if sar.Spec.NonResourceAttributes != nil {
					return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
				}
				resourceCalls++
				r := sar.Spec.ResourceAttributes
				if r == nil || r.Namespace != "serving" || r.Name != "scheduler" || r.Resource != "configmaps" || r.Group != "" || r.Verb != "update" {
					t.Fatalf("unexpected update grant: %+v", r)
				}
				if tc.failure {
					return true, nil, errors.New("unavailable")
				}
				return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: tc.allowed, Denied: tc.denied, EvaluationError: tc.evaluation}}, nil
			})
			a := Authorizer{Client: kube}
			principal, err := a.Authorize(context.Background(), "reviewed-token", "PUT", "/api/v1/scheduler/settings")
			if err != nil {
				t.Fatal(err)
			}
			if err = a.AuthorizeConfigMapUpdate(context.Background(), principal, "serving", "scheduler"); !errors.Is(err, tc.want) {
				t.Fatalf("update authorization=%v want %v", err, tc.want)
			}
			if resourceCalls != 1 {
				t.Fatalf("resource grants=%d", resourceCalls)
			}
			if err = a.AuthorizeConfigMapUpdate(context.Background(), Principal{}, "serving", "scheduler"); !errors.Is(err, ErrUnauthenticated) || resourceCalls != 1 {
				t.Fatal("unreviewed identity reached resource SAR")
			}
		})
	}
}
