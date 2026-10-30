package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
)

// SCH-AC-53: a failed batch is sent once; the next call rereads rotated SA tokens.
func TestResourcePostFailureAndTokenRotation(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("old-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		expected := "Bearer old-token"
		if requests == 2 {
			expected = "Bearer new-token"
		}
		if r.Header.Get("Authorization") != expected {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if requests == 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"targets":[{"podUID":"new-pod","containerID":"containerd://new"}]}`))
	}))
	defer server.Close()
	config := resourceAgentConfig{URL: server.URL, TokenFile: tokenFile, MaxResponseBytes: 1024}
	batch := serving.ResourceBatch{
		Node:    serving.NodeReference{Name: "edge", UID: "edge-uid"},
		Samples: []serving.ResourceSample{},
	}
	if _, err := sendResourceBatch(context.Background(), server.Client(), config, batch); err == nil {
		t.Fatal("failed POST accepted")
	}
	if requests != 1 {
		t.Fatalf("failed batch replayed %d times", requests)
	}
	if err := os.WriteFile(tokenFile, []byte("new-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	targets, err := sendResourceBatch(context.Background(), server.Client(), config, batch)
	if err != nil || len(targets) != 1 || targets[0].PodUID != "new-pod" || requests != 2 {
		t.Fatalf("targets=%v err=%v requests=%d", targets, err, requests)
	}
}
