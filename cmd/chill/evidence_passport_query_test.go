package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestEvidencePassportUsesAuthenticatedOwnerHTTPS(t *testing.T) {
	passport := testEvidencePassport()
	owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/namespaces/chill-system/profiling-runs/cpu-run/evidence-passport" ||
			r.Header.Get("Authorization") != "Bearer caller-token" {
			t.Errorf("wrong passport authentication boundary: %s %s", r.Method, r.URL)
		}
		if err := json.NewEncoder(w).Encode(passport); err != nil {
			t.Error(err)
		}
	}), operationOptions{namespace: "chill-system", name: "cpu-run"})
	got, err := (&ownerEvidencePassportQuery{owner}).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Run.UID != passport.Run.UID || got.Measurement.Runtime.Latency.P99 != passport.Measurement.Runtime.Latency.P99 {
		t.Fatalf("passport=%+v", got)
	}
}

func TestEvidencePassportOwnerRejectsWrongIdentityOversizeAndDenial(t *testing.T) {
	for _, mode := range []string{"identity", "api-version", "oversize", "denied"} {
		t.Run(mode, func(t *testing.T) {
			passport := testEvidencePassport()
			if mode == "identity" {
				passport.Run.Name = "other"
			}
			if mode == "api-version" {
				passport.APIVersion = "future"
			}
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if mode == "denied" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if mode == "oversize" {
					_, _ = w.Write([]byte(strings.Repeat("x", int(maximumEvidencePassportBytes)+1)))
					return
				}
				if err := json.NewEncoder(w).Encode(passport); err != nil {
					t.Error(err)
				}
			}), operationOptions{namespace: "chill-system", name: "cpu-run"})
			_, err := (&ownerEvidencePassportQuery{owner}).Read(context.Background())
			if err == nil {
				t.Fatal("invalid or unauthorized passport accepted")
			}
		})
	}
}
