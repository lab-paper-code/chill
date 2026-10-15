package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
)

// Stream large JSON strings without allocating a second full response fixture.
type resultDetailReader struct{}

func (resultDetailReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestMeasurementResultResponseSizeBoundary(t *testing.T) {
	for _, test := range []struct {
		name        string
		detailBytes int64
		wantError   bool
	}{
		{"above-passport-limit", maximumEvidencePassportBytes + 1, false},
		{"above-measurement-result-limit", maximumMeasurementResultBytes + 1, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/namespaces/ns/batch-energy-optimizations/task/result" {
					t.Errorf("unexpected owner path %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"apiVersion":%q,"kind":"BatchEnergyOptimizationResult",`+
					`"campaign":{"namespace":"ns","name":"task","phase":"Complete"},`+
					`"result":{"schemaVersion":"test","detail":"`,
					operatorconsole.BatchEnergyOptimizationResultAPIVersion)
				_, _ = io.CopyN(w, resultDetailReader{}, test.detailBytes)
				_, _ = io.WriteString(w, `"}}`)
			}))
			defer server.Close()
			q := &measurementClient{ownerClient: &ownerClient{
				opts:       operationOptions{namespace: "ns", name: "task"},
				httpClient: server.Client(), origin: server.URL,
			}}
			result, err := q.Show(context.Background())
			if test.wantError {
				if err == nil || !strings.Contains(err.Error(), "response exceeds size limit") {
					t.Fatalf("oversized response error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Result == nil || int64(len(result.Result.Detail)) != test.detailBytes+2 {
				t.Fatal("valid result above Evidence Passport size limit was not retained")
			}
		})
	}
}
