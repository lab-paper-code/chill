package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func chillServerFixture(t *testing.T, handler http.HandlerFunc) (string, string, string) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	ca := writeFile(t, "ca.pem", string(certificate))
	return server.URL, ca, writeFile(t, "token", "caller-token")
}

func clientArgs(kind, manifest, url, ca, token string) []string {
	return []string{kind, manifest, "--chill-server", url, "--chill-ca-file", ca, "--chill-token-file", token}
}

func TestMeasurementSendsRequestsAndFailsOnSequenceError(t *testing.T) {
	var received []servingkube.MeasurementRequest
	url, ca, token := chillServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != measurementsPath || r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Errorf("request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Error(err)
		}
		result := servingkube.SequenceResult{SequenceID: "seq-1", Error: "measurement Insufficient: power"}
		_ = json.NewEncoder(w).Encode(result)
	})
	manifest := writeFile(t, "m.json", `[{"operation":{"id":"prepare","kind":"prepare","node":{"name":"n","uid":"u"}}}]`)
	var out bytes.Buffer
	err := run(context.Background(), clientArgs("--measurement-config", manifest, url, ca, token), &out)
	if err == nil || len(received) != 1 || received[0].Operation.ID != "prepare" ||
		!strings.Contains(out.String(), "seq-1") {
		t.Fatalf("err=%v received=%+v out=%s", err, received, out.String())
	}
}

func TestManifestRejectsUnknownFieldsBeforeSending(t *testing.T) {
	calls := 0
	url, ca, token := chillServerFixture(t, func(http.ResponseWriter, *http.Request) { calls++ })
	manifest := writeFile(t, "m.json", `{"kind":"prepare","journalDirectory":"/tmp"}`)
	err := run(context.Background(), clientArgs("--execute-config", manifest, url, ca, token), &bytes.Buffer{})
	if err == nil || calls != 0 {
		t.Fatalf("unknown field accepted: %v calls=%d", err, calls)
	}
}

func TestClientDoesNotFollowRedirectOrAcceptAmbientTrust(t *testing.T) {
	foreignCalls := 0
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignCalls++ }))
	defer foreign.Close()
	url, ca, token := chillServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusFound)
	})
	manifest := writeFile(t, "m.json",
		`{"kind":"remove","workloadUID":"w","node":{"name":"n","uid":"u"},"instance":"p/c"}`)
	err := run(context.Background(), clientArgs("--execute-config", manifest, url, ca, token), &bytes.Buffer{})
	if err == nil || foreignCalls != 0 {
		t.Fatalf("redirect followed: %v calls=%d", err, foreignCalls)
	}
	for _, origin := range []string{"http://owner", url + "/path", url + "?query=1", "https://user@owner"} {
		if _, err = chillClient(origin, ca, token); err == nil {
			t.Fatalf("unsafe origin accepted: %s", origin)
		}
	}
	if _, err = chillClient(url, "", token); err == nil {
		t.Fatal("ambient trust accepted")
	}
}
