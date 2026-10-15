package kube

import (
	"context"
	"errors"
	"fmt"
	"strings"

	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var (
	ErrUnauthenticated          = errors.New("unauthenticated")
	ErrForbidden                = errors.New("forbidden")
	ErrAuthorizationUnavailable = errors.New("authorization service unavailable")
)

// Authorizer authenticates bearer tokens with Kubernetes and authorizes each
// HTTP path under /chill, outside Kubernetes' public /api/* discovery grants.
type Authorizer struct {
	Client kubernetes.Interface
}

// Principal contains only identity verified by TokenReview, never client headers.
type Principal struct {
	username string
	uid      string
	identity *authenticationv1.UserInfo
}

type principalKey struct{}

// WithPrincipal carries the verified actor through the management boundary.
func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

// PrincipalFromContext returns the verified actor, or zero when none is present.
func PrincipalFromContext(ctx context.Context) Principal {
	principal, _ := ctx.Value(principalKey{}).(Principal)
	return principal
}

// Authorize takes the bearer token, not the full Authorization header. Review
// failures are fail-closed; returned errors contain neither tokens nor identity.
func (a *Authorizer) Authorize(ctx context.Context, token, verb, path string) (Principal, error) {
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return Principal{}, ErrUnauthenticated
	}
	if a.Client == nil {
		return Principal{}, ErrAuthorizationUnavailable
	}
	review, err := a.Client.AuthenticationV1().TokenReviews().Create(ctx, &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{Token: token},
	}, metav1.CreateOptions{})
	if err != nil {
		return Principal{}, fmt.Errorf("%w: TokenReview unavailable", ErrAuthorizationUnavailable)
	}
	if review == nil {
		return Principal{}, ErrAuthorizationUnavailable
	}
	if !review.Status.Authenticated || review.Status.User.Username == "" {
		return Principal{}, ErrUnauthenticated
	}
	if review.Status.Error != "" {
		return Principal{}, ErrAuthorizationUnavailable
	}
	verb = strings.ToLower(verb)
	switch verb {
	case "get", "post", "put", "delete", "patch", "head", "options":
	default:
		return Principal{}, ErrForbidden
	}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#\r\n") {
		return Principal{}, ErrForbidden
	}
	extra := make(map[string]authorizationv1.ExtraValue, len(review.Status.User.Extra))
	for key, values := range review.Status.User.Extra {
		extra[key] = authorizationv1.ExtraValue(values)
	}
	access, err := a.Client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User:                  review.Status.User.Username,
			UID:                   review.Status.User.UID,
			Groups:                review.Status.User.Groups,
			Extra:                 extra,
			NonResourceAttributes: &authorizationv1.NonResourceAttributes{Verb: verb, Path: "/chill" + path},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		return Principal{}, fmt.Errorf("%w: SubjectAccessReview unavailable", ErrAuthorizationUnavailable)
	}
	if access == nil || access.Status.EvaluationError != "" {
		return Principal{}, ErrAuthorizationUnavailable
	}
	if !access.Status.Allowed || access.Status.Denied {
		return Principal{}, ErrForbidden
	}
	return Principal{username: review.Status.User.Username, uid: review.Status.User.UID, identity: review.Status.User.DeepCopy()}, nil
}

// Actor returns the authenticated identity for durable settings audit records.
func (p Principal) Actor() (string, string) { return p.username, p.uid }

// AuthorizeConfigMapUpdate requires a separate resource grant for the exact
// scheduler configuration, preserving TokenReview groups and extras.
func (a *Authorizer) AuthorizeConfigMapUpdate(ctx context.Context, principal Principal, namespace, name string) error {
	return a.AuthorizeResource(ctx, principal, authorizationv1.ResourceAttributes{
		Namespace: namespace, Verb: "update", Resource: "configmaps", Name: name,
	})
}

// AuthorizeResource checks native permissions as the verified caller, never the
// owner's service account.
func (a *Authorizer) AuthorizeResource(ctx context.Context, principal Principal, resource authorizationv1.ResourceAttributes) error {
	if principal.identity == nil || principal.username == "" {
		return ErrUnauthenticated
	}
	if a.Client == nil {
		return ErrAuthorizationUnavailable
	}
	extra := make(map[string]authorizationv1.ExtraValue, len(principal.identity.Extra))
	for key, values := range principal.identity.Extra {
		extra[key] = authorizationv1.ExtraValue(values)
	}
	access, err := a.Client.AuthorizationV1().SubjectAccessReviews().Create(ctx, &authorizationv1.SubjectAccessReview{
		Spec: authorizationv1.SubjectAccessReviewSpec{
			User: principal.username, UID: principal.uid, Groups: principal.identity.Groups, Extra: extra,
			ResourceAttributes: &resource,
		},
	}, metav1.CreateOptions{})
	if err != nil || access == nil || access.Status.EvaluationError != "" {
		return ErrAuthorizationUnavailable
	}
	if !access.Status.Allowed || access.Status.Denied {
		return ErrForbidden
	}
	return nil
}
