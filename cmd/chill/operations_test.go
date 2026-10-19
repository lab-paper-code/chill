package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	"k8s.io/client-go/rest"
)

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
		if r.Method != http.MethodGet {
			t.Errorf("profiles are read-only: %s", r.Method)
		}
		_ = json.NewEncoder(w).Encode(httpapi.ProfilesView{ConfigVersion: "rv1"})
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
	if leaked != 0 || requests != 3 {
		t.Fatalf("redirect leaked=%d owner requests=%d", leaked, requests)
	}
}
