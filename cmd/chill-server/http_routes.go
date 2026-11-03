package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authorizationv1 "k8s.io/api/authorization/v1"
)

func privateOrigin(raw string) (*url.URL, error) {
	if raw == "" {
		return nil, nil
	}
	origin, err := url.Parse(raw)
	if err != nil || strings.TrimSpace(raw) != raw || origin.Opaque != "" ||
		(origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.Hostname() == "" ||
		origin.User != nil || (origin.Path != "" && origin.Path != "/") || origin.RawPath != "" ||
		origin.RawQuery != "" || origin.ForceQuery || origin.Fragment != "" {
		return nil, errors.New(
			"private URL must be one fixed HTTP(S) origin without credentials, path, query or fragment",
		)
	}
	return origin, nil
}

const queryUnavailableCode = "ConsoleUnavailable"
const queryListVerb = "list"
const queryGetVerb = "get"

func queryError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code string `json:"code"`
	}{code})
}

// composeChillServer is the only cross-domain assembly. The QueryAPI's existing
// retained-evidence and production preflight composition stays authoritative.
func composeChillServer(
	serving http.Handler, authorizer *servingkube.Authorizer, opts options, measurements ...*measurementService,
) (http.Handler, error) {
	origin, err := privateOrigin(opts.queryURL)
	if err != nil {
		return nil, err
	}
	var measurement *measurementService
	if len(measurements) != 0 {
		measurement = measurements[0]
	}
	query := queryReadHandler(origin)
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), opts.requestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			queryError(w, http.StatusUnauthorized, "Unauthenticated")
			return
		}
		principal, err := authorizer.Authorize(ctx, token, r.Method, r.URL.Path)
		if err != nil {
			queryAuthorizationError(w, err)
			return
		}
		if err := authorizeQueryResources(ctx, r, measurement, authorizer, principal); err != nil {
			queryAuthorizationError(w, err)
			return
		}
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			queryError(w, http.StatusServiceUnavailable, queryUnavailableCode)
			return
		}
		if err := controller.SetWriteDeadline(deadline.Add(opts.requestTimeout)); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			queryError(w, http.StatusServiceUnavailable, queryUnavailableCode)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, opts.maxRequestBytes)
		query.ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	for _, route := range []string{
		"GET /api/v1/scheduler/profiles",
		"GET /api/v1/namespaces/{namespace}/measurement-coverage",
		"GET /api/v1/namespaces/{namespace}/batch-energy-optimizations",
		"GET /api/v1/namespaces/{namespace}/operation-cost-measurements",
		"GET /api/v1/namespaces/{namespace}/operation-cost-measurements/{name}",
		"GET /api/v1/namespaces/{namespace}/dispatch-occupancy-measurements",
		"GET /api/v1/namespaces/{namespace}/dispatch-occupancy-measurements/{name}",
		"GET /api/v1/systems",
		"GET /api/v1/inventory",
		"GET /api/v1/model-compatibility",
		"GET /api/v1/model-compatibility-matrix",
		"GET /api/v1/namespaces/{namespace}/profiling-runs",
		"GET /api/v1/namespaces/{namespace}/profiling-runs/{name}/evidence-passport",
		"GET /api/v1/namespaces/{namespace}/batch-energy-optimizations/{name}/result",
		"POST /api/v1/namespaces/{namespace}/batch-energy-optimizations/preflight",
	} {
		mux.Handle(route, authenticated)
	}
	webOrigin, err := privateOrigin(opts.dashboardURL)
	if err != nil {
		return nil, err
	}
	var web *httputil.ReverseProxy
	if webOrigin != nil {
		// Native proxy preserves Upgrade, Origin and the public Host for /api/live.
		// Do not SetURL/replace Host: the BFF validates the browser's TLS origin.
		web = httputil.NewSingleHostReverseProxy(webOrigin)
		web.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			queryError(w, http.StatusBadGateway, "DashboardUnavailable")
		}
	}
	mux.Handle("/", serving)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if web != nil && !strings.HasPrefix(r.URL.Path, "/api/v1") && r.URL.Path != "/healthz" &&
			r.URL.Path != "/readyz" {
			web.ServeHTTP(w, r)
			return
		}
		// Prevent mux path-clean redirects from creating a different auth target.
		// RawPath also excludes escaped separators from namespace/name segments.
		if r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path {
			queryError(w, http.StatusBadRequest, "InvalidPath")
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), queryBearerContextKey{}, r.Header.Get("Authorization")))
		mux.ServeHTTP(w, r)
	}), nil
}

func isMeasurementPreflight(request *http.Request) bool {
	return request.Method == http.MethodPost &&
		strings.HasSuffix(request.URL.Path, "/batch-energy-optimizations/preflight")
}

func authorizeQueryResources(
	ctx context.Context,
	request *http.Request,
	measurement *measurementService,
	authorizer *servingkube.Authorizer,
	principal servingkube.Principal,
) error {
	namespace := request.PathValue("namespace")
	if namespace != "" && measurement != nil {
		if err := measurement.namespace(namespace); err != nil {
			return err
		}
	}
	resources := []string{}
	resourceVerb := queryListVerb
	switch {
	case isMeasurementPreflight(request):
		resources = []string{"batchenergyoptimizations"}
	case strings.HasSuffix(request.URL.Path, "/measurement-coverage"):
		resources = []string{"batchenergyoptimizations", "profilingruns", "dispatchoccupancymeasurements"}
	case strings.HasSuffix(request.URL.Path, "/batch-energy-optimizations"):
		resources = []string{"batchenergyoptimizations"}
	case strings.Contains(request.URL.Path, "/operation-cost-measurements"):
		resources = []string{"operationcostmeasurements"}
	case strings.Contains(request.URL.Path, "/dispatch-occupancy-measurements"):
		resources = []string{"dispatchoccupancymeasurements"}
	}
	if request.PathValue("name") != "" {
		resourceVerb = queryGetVerb
	}
	for _, resource := range resources {
		if err := authorizer.AuthorizeResource(ctx, principal, authorizationv1.ResourceAttributes{
			Namespace: namespace, Verb: resourceVerb, Group: "edge.dacs.io", Version: "v1alpha1",
			Resource: resource, Name: request.PathValue("name"),
		}); err != nil {
			return err
		}
	}
	return nil
}

func queryAuthorizationError(writer http.ResponseWriter, err error) {
	status, code := http.StatusServiceUnavailable, "AuthorizationUnavailable"
	if errors.Is(err, servingkube.ErrUnauthenticated) {
		status, code = http.StatusUnauthorized, "Unauthenticated"
	}
	if errors.Is(err, servingkube.ErrForbidden) {
		status, code = http.StatusForbidden, "Forbidden"
	}
	queryError(writer, status, code)
}
