package s3

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

func TestConfigValidate(t *testing.T) {
	valid := Config{
		LogicalStore:    "chill-evidence-store",
		Bucket:          "chill-profiling-evidence",
		Endpoint:        "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Region:          "us-east-1",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
		TempDir:         "/workspace",
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	tests := map[string]func(*Config){
		"missing store":      func(config *Config) { config.LogicalStore = "" },
		"missing bucket":     func(config *Config) { config.Bucket = "" },
		"store whitespace":   func(config *Config) { config.LogicalStore = " " + config.LogicalStore },
		"bucket whitespace":  func(config *Config) { config.Bucket += " " },
		"missing endpoint":   func(config *Config) { config.Endpoint = "" },
		"relative endpoint":  func(config *Config) { config.Endpoint = "chill-evidence-store:9000" },
		"endpoint query":     func(config *Config) { config.Endpoint += "?debug=true" },
		"missing region":     func(config *Config) { config.Region = "" },
		"missing access key": func(config *Config) { config.AccessKeyID = "" },
		"missing secret":     func(config *Config) { config.SecretAccessKey = "" },
		"relative temp dir":  func(config *Config) { config.TempDir = "workspace" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			config := valid
			mutate(&config)
			if err := config.validate(); err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
}

func TestNew(t *testing.T) {
	store, err := New(Config{
		LogicalStore:    "chill-evidence-store",
		Bucket:          "chill-profiling-evidence",
		Endpoint:        "http://chill-evidence-store.chill-system.svc.cluster.local:9000",
		Region:          "us-east-1",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
		TempDir:         "/workspace",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if store == nil || store.client == nil || store.logicalStore != "chill-evidence-store" ||
		store.bucket != "chill-profiling-evidence" || store.tempDir != "/workspace" {
		t.Fatal("New() returned an unusable Store")
	}
}

// dnsLossClient answers like a pod whose cluster DNS stops answering for the
// first failures attempts; put decides the response of every later attempt.
type dnsLossClient struct {
	failures, attempts int
	bodies             []string
	put                func(attempt int) *http.Response
}

func (c *dnsLossClient) Do(request *http.Request) (*http.Response, error) {
	c.attempts++
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	c.bodies = append(c.bodies, string(body))
	if c.attempts <= c.failures {
		return nil, &net.OpError{Op: "dial", Net: "tcp", Err: &net.DNSError{
			Err: "read udp 192.168.141.176:49222->10.96.0.10:53: i/o timeout", Name: request.URL.Hostname(),
			Server: "10.96.0.10:53", IsTimeout: true,
		}}
	}
	response := c.put(c.attempts)
	response.Request = request
	return response, nil
}

func s3Response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Header: http.Header{"Content-Type": {"application/xml"}},
		Body: io.NopCloser(strings.NewReader(body)), ContentLength: int64(len(body)),
	}
}

func TestNewRetriesTransientTransportFailures(t *testing.T) {
	backoff := retryBackoff
	retryBackoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
	t.Cleanup(func() { retryBackoff = backoff })
	content := []byte(`{"verdict":"Accepted"}`)
	for name, tc := range map[string]struct {
		failures     int
		put          func(attempt int) *http.Response
		wantAttempts int
		wantErr      error
		wantOutcome  evidencestore.CreateOutcome
	}{
		"DNS loss outlasting the SDK default attempts": {
			failures: 6, put: func(int) *http.Response { return s3Response(http.StatusOK, "") },
			wantAttempts: 7, wantOutcome: evidencestore.CreateOutcomeCreated,
		},
		"lost success answers precondition on retry": {
			failures: 1, put: func(int) *http.Response {
				return s3Response(http.StatusPreconditionFailed, "<Error><Code>PreconditionFailed</Code></Error>")
			},
			wantAttempts: 2, wantOutcome: evidencestore.CreateOutcomeExisting,
		},
		"access denied is permanent": {
			put: func(int) *http.Response {
				return s3Response(http.StatusForbidden, "<Error><Code>AccessDenied</Code></Error>")
			},
			wantAttempts: 1, wantErr: evidencestore.ErrAuthentication,
		},
		"DNS loss past every attempt": {
			failures: 100, wantAttempts: storeRetryAttempts, wantErr: evidencestore.ErrTransport,
		},
	} {
		t.Run(name, func(t *testing.T) {
			httpClient := &dnsLossClient{failures: tc.failures, put: tc.put}
			store, err := New(Config{
				LogicalStore: "chill-evidence-store", Bucket: "chill-profiling-evidence",
				Endpoint: "http://chill-evidence-store.chill-system.svc.cluster.local:9000", Region: "us-east-1",
				AccessKeyID: "access-key", SecretAccessKey: "secret-key", TempDir: t.TempDir(), HTTPClient: httpClient,
			})
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := store.PutImmutable(context.Background(), identityFor(content), bytes.NewReader(content))
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("PutImmutable() error = %v, want %v", err, tc.wantErr)
				}
			} else if err != nil || receipt.Outcome != tc.wantOutcome {
				t.Fatalf("PutImmutable() = %+v, %v; want %s", receipt, err, tc.wantOutcome)
			}
			if httpClient.attempts != tc.wantAttempts {
				t.Fatalf("attempts = %d, want %d", httpClient.attempts, tc.wantAttempts)
			}
			for _, body := range httpClient.bodies {
				if !strings.Contains(body, string(content)) {
					t.Fatalf("attempt body %q lost the candidate bytes", body)
				}
			}
		})
	}
}
