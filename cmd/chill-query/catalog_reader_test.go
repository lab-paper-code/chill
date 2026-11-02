package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func TestServingCatalogReadUsesPinnedTLSServiceTokenAndNoRedirects(t *testing.T) {
	calls := 0
	redirect := false
	owner := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/scheduler/catalog-read" ||
			r.Header.Get("Authorization") != "Bearer query-service-token" {
			t.Error("owner read lost its exact path or service identity")
		}
		if redirect {
			w.Header().Set("Location", "https://untrusted.invalid/catalog")
			w.WriteHeader(http.StatusTemporaryRedirect)
			return
		}
		_ = json.NewEncoder(w).Encode(servingkube.CatalogRead{
			CatalogSnapshot: servingkube.CatalogSnapshot{ConfigVersion: "rv"},
			Accepted:        &servingkube.CatalogSnapshot{Profiles: []servingkube.ProfileSummary{{ID: "profile"}}},
		})
	}))
	defer owner.Close()
	directory := t.TempDir()
	certificate := filepath.Join(directory, "serving.crt")
	token := filepath.Join(directory, "token")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: owner.Certificate().Raw})
	if err := os.WriteFile(certificate, certificatePEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("query-service-token"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := newServingCatalogReader(
		options{
			servingURL:       owner.URL,
			servingCAFile:    certificate,
			servingTokenFile: token,
			requestTimeout:   time.Second,
			maxRequestBytes:  1 << 20,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	got, err := read(context.Background())
	if err != nil || got.ConfigVersion != "rv" || got.Accepted == nil || len(got.Accepted.Profiles) != 1 || calls != 1 {
		t.Fatalf("typed owner read = %+v %v calls=%d", got, err, calls)
	}
	redirect = true
	if _, err := read(context.Background()); err == nil || calls != 2 {
		t.Fatalf("owner redirect was followed or approved: %v calls=%d", err, calls)
	}
	owner.Close()
	if _, err := read(context.Background()); err == nil {
		t.Fatal("owner transport failure became a successful catalog")
	}
}
