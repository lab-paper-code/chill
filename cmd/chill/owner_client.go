package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"k8s.io/client-go/rest"
)

type ownerClient struct {
	opts       operationOptions
	httpClient *http.Client
	origin     string
}

func newOwnerHTTPSClient(opts operationOptions, kubeConfig *rest.Config) (*ownerClient, error) {
	origin := opts.servingURL
	if origin == "" {
		origin = "https://" + opts.servingService + "." + opts.namespace + ".svc:8080"
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("serving-url must be one HTTPS origin without credentials, path, query or fragment")
	}
	// Owner credentials and trust are independent of the API server: never forward
	// kubeconfig client certificates, impersonation, insecure TLS, or auth plugins.
	config := &rest.Config{Host: origin, TLSClientConfig: rest.TLSClientConfig{CAFile: opts.servingCAFile}}
	if opts.tokenFile != "" {
		config.BearerTokenFile = opts.tokenFile
	} else {
		config.BearerToken = kubeConfig.BearerToken
		config.BearerTokenFile = kubeConfig.BearerTokenFile
	}
	if strings.TrimSpace(config.BearerToken) == "" && config.BearerTokenFile == "" {
		return nil, errors.New("owner bearer credentials required: use --token-file or kubeconfig bearer credentials; " +
			"API-server client certificates do not authenticate to the owner")
	}
	httpClient, err := rest.HTTPClientFor(config)
	if err != nil {
		return nil, fmt.Errorf("configure owner HTTPS transport: %w", err)
	}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &ownerClient{opts: opts, httpClient: httpClient, origin: strings.TrimSuffix(origin, "/")}, nil
}

func (q *ownerClient) read(
	ctx context.Context, verb, path string, payload []byte, version string, maximumBytes int64, result any,
) error {
	request, err := http.NewRequestWithContext(ctx, verb, q.origin+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if version != "" {
		request.Header.Set("If-Match", version)
	}
	response, err := q.httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("request owner HTTPS API: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maximumBytes+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if int64(len(body)) > maximumBytes {
		return errors.New("owner response exceeds size limit")
	}
	if response.StatusCode == http.StatusUnauthorized {
		return errors.New("owner bearer credentials rejected (HTTP 401); supply a valid caller token with --token-file")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("owner HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, result)
}
