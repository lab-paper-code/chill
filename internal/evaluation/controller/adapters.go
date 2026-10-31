package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
)

// NewRuntime resolves only the run's frozen binding, never current operator config.
func NewRuntime(reader client.Reader) Runtime {
	return Runtime{
		Store: func(ctx context.Context, namespace, logicalStore string, v map[string]string) (ObjectStore, error) {
			credentials, err := storekube.LoadCredentials(ctx, reader, namespace, v["evidenceCredentialsSecret"])
			if err != nil {
				return nil, err
			}
			return stores3.New(stores3.Config{LogicalStore: logicalStore, Endpoint: v["evidenceEndpoint"], Bucket: v["evidenceBucket"], Region: v["evidenceRegion"], AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey})
		},
		HTTP: func(ctx context.Context, namespace string, v map[string]string, path string, result any) error {
			ca := &corev1.Secret{}
			if err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: v["caSecret"]}, ca); err != nil {
				return err
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(ca.Data["tls.crt"]) {
				return fmt.Errorf("Chill CA has no certificates")
			}
			transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
			defer transport.CloseIdleConnections()
			apiClient := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
			if err != nil {
				return err
			}
			request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(v["chillAPIURL"], "/")+path, nil)
			if err != nil {
				return err
			}
			request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			response, err := apiClient.Do(request)
			if err != nil {
				return err
			}
			defer func() { _ = response.Body.Close() }()
			if response.StatusCode != http.StatusOK {
				return fmt.Errorf("Chill GET %s: HTTP %d", path, response.StatusCode)
			}
			data, err := io.ReadAll(io.LimitReader(response.Body, maximumResultBytes+1))
			if err != nil {
				return err
			}
			if int64(len(data)) > maximumResultBytes {
				return fmt.Errorf("Chill response exceeds limit")
			}
			return json.Unmarshal(data, result)
		},
	}
}
