package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

func TestManifestRejectsUnknownFieldsBeforeSending(t *testing.T) {
	calls := 0
	url, ca, token := chillServerFixture(t, func(http.ResponseWriter, *http.Request) { calls++ })
	manifest := writeFile(t, "m.json", `{"kind":"prepare","reason":"guard unknown fields","journalDirectory":"/tmp"}`)
	err := run(context.Background(), clientArgs("--execute-config", manifest, url, ca, token), &bytes.Buffer{})
	if err == nil || calls != 0 {
		t.Fatalf("unknown field accepted: %v calls=%d", err, calls)
	}
}

func TestClientDoesNotFollowRedirectOrAcceptAmbientTrust(t *testing.T) {
	redirectCalls, foreignCalls := 0, 0
	foreign := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { foreignCalls++ }))
	defer foreign.Close()
	url, ca, token := chillServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
		redirectCalls++
		http.Redirect(w, r, foreign.URL, http.StatusFound)
	})
	manifest := writeFile(t, "m.json",
		`{"kind":"remove","workloadUID":"w","node":{"name":"n","uid":"u"},"instance":"p/c","reason":"security regression"}`)
	err := run(context.Background(), clientArgs("--execute-config", manifest, url, ca, token), &bytes.Buffer{})
	if err == nil || redirectCalls != 1 || foreignCalls != 0 {
		t.Fatalf("redirect guard was not exercised: %v origin=%d foreign=%d", err, redirectCalls, foreignCalls)
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

func TestManualManifestRelaysOnlySuppliedKindFields(t *testing.T) {
	for _, manifest := range []string{
		`{"kind":"route","workloadUID":"w","weights":{"pod/container":1000},"reason":"select measured instance"}`,
		`{"kind":"reset","workloadUIDs":["w"],"reason":"finish measurement"}`,
		`{"kind":"reset","scope":"all-non-autonomous","reason":"clear manual instances"}`,
	} {
		t.Run(manifest, func(t *testing.T) {
			calls := 0
			url, ca, token := chillServerFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != operationsPath ||
					r.Header.Get("Authorization") != "Bearer caller-token" || r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("unexpected relay request: %s %s %v", r.Method, r.URL, r.Header)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				var got, want map[string]json.RawMessage
				if err := json.Unmarshal(raw, &got); err != nil {
					t.Error(err)
				}
				if err := json.Unmarshal([]byte(manifest), &want); err != nil {
					t.Error(err)
				}
				if len(got) != len(want) {
					t.Errorf("relay injected fields: %s", raw)
				}
				for field, value := range want {
					if !bytes.Equal(got[field], value) {
						t.Errorf("relay changed %s: got %s want %s", field, got[field], value)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"status":"complete","operation":{"kind":"reset"}}`)
			})
			var output bytes.Buffer
			args := clientArgs("--execute-config", writeFile(t, "m.json", manifest), url, ca, token)
			err := run(context.Background(), args, &output)
			if err != nil || calls != 1 || !bytes.Contains(output.Bytes(), []byte(`"status":"complete"`)) {
				t.Fatalf("relay failed: %v calls=%d output=%s", err, calls, output.String())
			}
		})
	}
}

func TestManualManifestRequiresReasonBeforeSending(t *testing.T) {
	calls := 0
	url, ca, token := chillServerFixture(t, func(http.ResponseWriter, *http.Request) { calls++ })
	for _, manifest := range []string{
		`{"kind":"route","workloadUID":"w","weights":{"p/c":1}}`,
		`{"kind":"reset","workloadUIDs":["w"],"reason":null}`,
		`{"kind":"reset","scope":"all-non-autonomous","reason":" "}`,
	} {
		args := clientArgs("--execute-config", writeFile(t, "m.json", manifest), url, ca, token)
		err := run(context.Background(), args, &bytes.Buffer{})
		if err == nil || calls != 0 {
			t.Fatalf("missing reason sent: %s err=%v calls=%d", manifest, err, calls)
		}
	}
}
