package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
)

// queryReadHandler forwards opaque query responses. It never reads Kubernetes
// or interprets domain bodies, even when the backend is unavailable.
func queryReadHandler(origin *url.URL) http.Handler {
	if origin == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			queryError(w, http.StatusServiceUnavailable, queryUnavailableCode)
		})
	}
	return &httputil.ReverseProxy{
		Rewrite: func(proxy *httputil.ProxyRequest) {
			proxy.Out.URL.Scheme, proxy.Out.URL.Host = origin.Scheme, origin.Host
			proxy.Out.Host = origin.Host
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
		ModifyResponse: func(response *http.Response) error {
			response.Header.Set("Cache-Control", "no-store")
			response.Header.Del("Set-Cookie")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, _ error) {
			status, code := http.StatusBadGateway, queryUnavailableCode
			if errors.Is(r.Context().Err(), context.DeadlineExceeded) {
				status, code = http.StatusGatewayTimeout, "ConsoleTimeout"
			}
			queryError(w, status, code)
		},
	}
}
