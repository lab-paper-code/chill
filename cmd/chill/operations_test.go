package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
	"k8s.io/client-go/rest"
)

type fakeProfileOperations struct {
	result entrypoint.ProfileSelectionResult
	err    error
}

func (f fakeProfileOperations) List(context.Context) (entrypoint.ProfileSelectionsView, error) {
	return entrypoint.ProfileSelectionsView{}, f.err
}
func (f fakeProfileOperations) Adopt(context.Context) (entrypoint.ProfileSelectionResult, error) {
	return f.result, f.err
}
func (f fakeProfileOperations) Unadopt(context.Context) (entrypoint.ProfileSelectionResult, error) {
	return f.result, f.err
}

func TestProfileAdoptionExitAndStructuredReason(t *testing.T) {
	for _, test := range []struct {
		status    string
		persisted bool
		loaded    bool
		wantError bool
	}{
		{"Pending", false, false, true}, {"Rejected", false, false, true},
		{"Pending", true, false, true}, {"Rejected", true, false, true},
		{"Persisted", true, false, false}, {"Persisted", true, true, false},
		{"Loaded", true, true, false}, {"AlreadySelected", true, true, false},
	} {
		t.Run(test.status, func(t *testing.T) {
			result := entrypoint.ProfileSelectionResult{
				Campaign: entrypoint.ProfileCampaign{Namespace: "ns", Name: "task"}, Status: test.status,
				Persisted: test.persisted, Loaded: test.loaded, Code: "MeasuredState", Reason: "actual reason",
			}
			deps := dependencies{newProfiles: func(opts operationOptions) (profileOperations, error) {
				if opts.namespace != "ns" || opts.servingService != "chill-serving-entrypoint" {
					t.Fatalf("options %#v", opts)
				}
				return fakeProfileOperations{result: result}, nil
			}}
			var stdout strings.Builder
			err := execute(context.Background(),
				[]string{profilesCommand, "adopt", "task", "-n", "ns", "-o", "json"}, &stdout, io.Discard, deps)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v", err)
			}
			var got entrypoint.ProfileSelectionResult
			if err := json.Unmarshal([]byte(stdout.String()), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != test.status || got.Reason != result.Reason ||
				got.Loaded != test.loaded || got.Persisted != test.persisted {
				t.Fatalf("result %#v", got)
			}
		})
	}
	failure := errors.New("forbidden")
	err := execute(context.Background(), []string{profilesCommand, "adopt", "task"}, io.Discard, io.Discard,
		dependencies{newProfiles: func(operationOptions) (profileOperations, error) {
			return fakeProfileOperations{err: failure}, nil
		}})
	if !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
}

func TestProfileOwnerHTTPSCredentialsAndTrust(t *testing.T) {
	requests := 0
	reject := false
	redirect := ""
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if reject {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		if redirect != "" {
			http.Redirect(w, r, redirect, http.StatusTemporaryRedirect)
			return
		}
		if r.URL.Path != "/api/v1/scheduler/profiles" || r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Errorf("wrong owner request %s", r.URL)
		}
		if len(r.TLS.PeerCertificates) != 0 {
			t.Error("forwarded API-server client certificate")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(entrypoint.ProfileSelectionsView{ConfigVersion: "rv1"})
			return
		}
		var request entrypoint.ProfileSelectionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || request.Campaign.Namespace != "ns" || request.Campaign.Name != "task" {
			t.Errorf("wrong adoption request %#v", request)
		}
		_ = json.NewEncoder(w).Encode(entrypoint.ProfileSelectionResult{
			Campaign: request.Campaign, Status: "Persisted", Persisted: true,
		})
	}))
	defer server.Close()
	dir := t.TempDir()
	caFile := filepath.Join(dir, "owner-ca.pem")
	tokenFile := filepath.Join(dir, "token")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("caller-token"), 0600); err != nil {
		t.Fatal(err)
	}
	opts := operationOptions{
		namespace: "ns", name: "task", servingURL: server.URL, servingCAFile: caFile, tokenFile: tokenFile,
	}
	kubeConfig := &rest.Config{
		BearerToken: "wrong-api-token",
		TLSClientConfig: rest.TLSClientConfig{
			Insecure: true, CertData: []byte("never-forward"), KeyData: []byte("never-forward"),
		},
	}
	owner, err := newOwnerHTTPSClient(opts, kubeConfig)
	if err != nil {
		t.Fatal(err)
	}
	q := &profileClient{owner}
	list, err := q.List(context.Background())
	if err != nil || list.ConfigVersion != "rv1" {
		t.Fatalf("list %#v error %v", list, err)
	}
	result, err := q.Adopt(context.Background())
	if err != nil || !result.Persisted || result.Loaded {
		t.Fatalf("adopt %#v error %v", result, err)
	}
	reject = true
	if _, err := q.List(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("401 error = %v", err)
	}
	reject = false
	opts.tokenFile = ""
	if _, err := newOwnerHTTPSClient(opts, &rest.Config{}); err == nil ||
		!strings.Contains(err.Error(), "bearer credentials required") {
		t.Fatalf("certificate-only credentials error = %v", err)
	}
	opts.servingCAFile = ""
	untrustedOwner, err := newOwnerHTTPSClient(opts, &rest.Config{BearerToken: "caller-token"})
	if err != nil {
		t.Fatal(err)
	}
	untrusted := &profileClient{untrustedOwner}
	if _, err := untrusted.List(context.Background()); err == nil {
		t.Fatal("accepted untrusted owner TLS")
	}
	leaked := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked++ }))
	defer target.Close()
	redirect = target.URL
	if _, err := q.List(context.Background()); err == nil {
		t.Fatal("accepted owner redirect")
	}
	if leaked != 0 || requests != 4 {
		t.Fatalf("redirect leaked=%d owner requests=%d", leaked, requests)
	}
}
