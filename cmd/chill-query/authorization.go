package main

import (
	"context"
	"errors"
	"net/http"
	"path"
	"strings"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authorizationv1 "k8s.io/api/authorization/v1"
)

// Every query entry, including direct Service traffic, verifies the caller.
// Neither network location nor forwarded identity headers confer permission.
func authenticatedQueryHandler(next http.Handler, authorizer *servingkube.Authorizer, opts options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path {
			queryAuthorizationError(w, http.StatusBadRequest, "InvalidPath")
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), opts.requestTimeout)
		defer cancel()
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			queryAuthorizationError(w, http.StatusUnauthorized, "Unauthenticated")
			return
		}
		if authorizer == nil {
			queryAuthorizationError(w, http.StatusServiceUnavailable, "AuthorizationUnavailable")
			return
		}
		principal, err := authorizer.Authorize(ctx, token, r.Method, r.URL.Path)
		if err != nil {
			queryReviewError(w, err)
			return
		}
		if err := authorizeQueryRead(ctx, r, authorizer, principal, opts); err != nil {
			queryReviewError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func authorizeQueryRead(
	ctx context.Context, r *http.Request, authorizer *servingkube.Authorizer,
	principal servingkube.Principal, opts options,
) error {
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 6 || parts[3] != "namespaces" {
		return nil
	}
	namespace := parts[4]
	if err := (&measurementService{opts: opts}).namespace(namespace); err != nil {
		return err
	}
	resources := []string{}
	verb, name := "list", ""
	switch parts[5] {
	case "measurement-coverage":
		resources = []string{"batchenergyoptimizations", "profilingruns"}
	case "batch-energy-optimizations":
		resources = []string{"batchenergyoptimizations"}
	case "profiling-runs":
		resources = []string{"profilingruns"}
	case "operation-cost-measurements":
		resources = []string{"operationcostmeasurements"}
	case "dispatch-occupancy-measurements":
		resources = []string{"dispatchoccupancymeasurements"}
	}
	if len(parts) > 6 && parts[6] != "preflight" {
		verb, name = "get", parts[6]
	}
	for _, resource := range resources {
		if err := authorizer.AuthorizeResource(ctx, principal, authorizationv1.ResourceAttributes{
			Namespace: namespace, Verb: verb, Group: "edge.dacs.io", Version: "v1alpha1", Resource: resource, Name: name,
		}); err != nil {
			return err
		}
	}
	return nil
}

func queryReviewError(w http.ResponseWriter, err error) {
	if errors.Is(err, servingkube.ErrAuthorizationUnavailable) {
		queryAuthorizationError(w, http.StatusServiceUnavailable, "AuthorizationUnavailable")
		return
	}
	if errors.Is(err, servingkube.ErrUnauthenticated) {
		queryAuthorizationError(w, http.StatusUnauthorized, "Unauthenticated")
		return
	}
	if errors.Is(err, servingkube.ErrForbidden) {
		queryAuthorizationError(w, http.StatusForbidden, "Forbidden")
		return
	}
	httpapi.SendQueryError(w, err)
}

func queryAuthorizationError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte("{\"code\":\"" + code + "\"}\n"))
}
