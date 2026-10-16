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
		return nil, errors.New("private URL must be one fixed HTTP(S) origin without credentials, path, query or fragment")
	}
	return origin, nil
}

func consoleError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(struct {
		Code string `json:"code"`
	}{code})
}

// composeChillServer is the only cross-domain assembly. The Console's existing
// retained-evidence and production preflight composition stays authoritative.
func composeChillServer(serving http.Handler, authorizer *servingkube.Authorizer, opts options) (http.Handler, error) {
	origin, err := privateOrigin(opts.consoleURL)
	if err != nil {
		return nil, err
	}
	var console http.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		consoleError(w, http.StatusServiceUnavailable, "ConsoleUnavailable")
	})
	if origin != nil {
		console = &httputil.ReverseProxy{
			Rewrite: func(proxy *httputil.ProxyRequest) {
				proxy.Out.URL.Scheme, proxy.Out.URL.Host = origin.Scheme, origin.Host
				proxy.Out.Host = origin.Host
				// Allowlist, rather than chasing every forged identity header.
				proxy.Out.Header = make(http.Header)
				proxy.Out.URL.User = nil
				proxy.Out.URL.RawQuery = proxy.In.URL.RawQuery
				for _, key := range []string{"Accept", "Content-Type"} {
					if value := proxy.In.Header.Get(key); value != "" {
						proxy.Out.Header.Set(key, value)
					}
				}
				proxy.Out.Header.Set("User-Agent", "")
			},
			// ReverseProxy uses RoundTrip directly: redirects are never followed.
			ModifyResponse: func(response *http.Response) error {
				response.Header.Set("Cache-Control", "no-store")
				response.Header.Del("Set-Cookie")
				return nil
			},
			ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
				if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
					consoleError(w, http.StatusGatewayTimeout, "ConsoleTimeout")
					return
				}
				consoleError(w, http.StatusBadGateway, "ConsoleUnavailable")
			},
		}
	}
	authenticated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), opts.requestTimeout)
		defer cancel()
		r = r.WithContext(ctx)
		scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
		if !ok || !strings.EqualFold(scheme, "Bearer") || token == "" {
			consoleError(w, http.StatusUnauthorized, "Unauthenticated")
			return
		}
		_, err := authorizer.Authorize(ctx, token, r.Method, r.URL.Path)
		if err != nil {
			status, code := http.StatusServiceUnavailable, "AuthorizationUnavailable"
			if errors.Is(err, servingkube.ErrUnauthenticated) {
				status, code = http.StatusUnauthorized, "Unauthenticated"
			}
			if errors.Is(err, servingkube.ErrForbidden) {
				status, code = http.StatusForbidden, "Forbidden"
			}
			consoleError(w, status, code)
			return
		}
		deadline, _ := ctx.Deadline()
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(deadline); err != nil && !errors.Is(err, http.ErrNotSupported) {
			consoleError(w, http.StatusServiceUnavailable, "ConsoleUnavailable")
			return
		}
		if err := controller.SetWriteDeadline(deadline.Add(opts.requestTimeout)); err != nil &&
			!errors.Is(err, http.ErrNotSupported) {
			consoleError(w, http.StatusServiceUnavailable, "ConsoleUnavailable")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, opts.maxRequestBytes)
		console.ServeHTTP(w, r)
	})
	mux := http.NewServeMux()
	for _, route := range []string{
		"GET /api/v1/systems",
		"GET /api/v1/inventory",
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
		// Native proxy preserves the browser Host and bearer for the web BFF.
		web = httputil.NewSingleHostReverseProxy(webOrigin)
		web.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
			consoleError(w, http.StatusBadGateway, "DashboardUnavailable")
		}
	}
	mux.Handle("/", serving)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if web != nil && !strings.HasPrefix(r.URL.Path, "/api/v1") && r.URL.Path != "/healthz" && r.URL.Path != "/readyz" {
			web.ServeHTTP(w, r)
			return
		}
		// Prevent mux path-clean redirects from creating a different auth target.
		// RawPath also excludes escaped separators from namespace/name segments.
		if r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path {
			consoleError(w, http.StatusBadRequest, "InvalidPath")
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}
