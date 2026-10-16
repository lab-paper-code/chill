package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving/httpapi"
)

func TestProfileUnadoptionExplicitVersionAndOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, version, status, code string
		persisted, loaded           bool
		wantError                   bool
	}{
		{"stale", "rv1", "Removed", "", false, false, true},
		{"in-use", "rv2", "Rejected", "ProfileInUse", true, true, true},
		{"refresh-failed", "rv2", "Pending", "ProfileUnloadFailed", false, true, true},
		{"removed", "rv2", "Removed", "", false, false, false},
		{"already-absent", "rv2", "Unselected", "", false, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/scheduler/profiles/ns/task" ||
					r.Header.Get("If-Match") != test.version {
					t.Errorf("unadoption changed precondition: %s %s If-Match=%q", r.Method, r.URL, r.Header.Get("If-Match"))
				}
				if r.Header.Get("If-Match") != "rv2" {
					http.Error(w, "ConfigMap conflict: no mutation", http.StatusConflict)
					return
				}
				_ = json.NewEncoder(w).Encode(httpapi.ProfileSelectionResult{
					Campaign: httpapi.ProfileCampaign{Namespace: "ns", Name: "task"}, Status: test.status,
					Persisted: test.persisted, Loaded: test.loaded, ConfigVersion: "rv3",
					Code: test.code, Reason: "actual owner state",
				})
			}), operationOptions{namespace: "ns", name: "task", ifMatch: test.version})
			deps := dependencies{newProfiles: func(operationOptions) (profileOperations, error) {
				return &profileClient{owner}, nil
			}}
			var stdout strings.Builder
			err := execute(context.Background(), []string{"profiles", operationUnadopt, "task", "-n", "ns",
				"--if-match", test.version, "-o", "json"}, &stdout, io.Discard, deps)
			if (err != nil) != test.wantError || requests != 1 {
				t.Fatalf("error=%v requests=%d", err, requests)
			}
			if test.name == "stale" {
				if stdout.Len() != 0 || !strings.Contains(err.Error(), "409") {
					t.Fatalf("stale result %q error %v", stdout.String(), err)
				}
				return
			}
			var result httpapi.ProfileSelectionResult
			if decodeErr := json.Unmarshal([]byte(stdout.String()), &result); decodeErr != nil ||
				result.Persisted != test.persisted || result.Loaded != test.loaded || result.Status != test.status ||
				result.Reason != "actual owner state" {
				t.Fatalf("lost actual state %#v error %v", result, decodeErr)
			}
		})
	}
}
