package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const passportCallerBearer = "Bearer caller-token"

func TestEvidencePassportUsesAuthenticatedOwnerHTTPS(t *testing.T) {
	passport := testEvidencePassport()
	owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet ||
			r.URL.Path != "/api/v1/namespaces/chill-system/profiling-runs/cpu-run/evidence-passport" ||
			r.Header.Get("Authorization") != passportCallerBearer {
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
	if got.Run.UID != passport.Run.UID ||
		got.Measurement.Runtime.Latency.P99 != passport.Measurement.Runtime.Latency.P99 {
		t.Fatalf("passport=%+v", got)
	}
}

func TestEvidencePassportOwnerRejectsWrongIdentityOversizeAndDenial(t *testing.T) {
	for _, mode := range []string{"identity", "api-version", "oversize", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			passport := testEvidencePassport()
			if mode == "identity" {
				passport.Run.Name = "other"
			}
			if mode == "api-version" {
				passport.APIVersion = "future"
			}
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if mode == "forbidden" {
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

func TestEvidencePassportPendingHTTP202PreservesJSONAndTable(t *testing.T) {
	ready := testEvidencePassport()
	run := api.ProfilingRun{
		ObjectMeta: meta.ObjectMeta{
			Namespace: ready.Run.Namespace, Name: ready.Run.Name, UID: "run-uid",
			Generation: 1, ResourceVersion: "pending-rv",
		},
		Spec: ready.Run.Spec,
		Status: api.ProfilingRunStatus{
			ObservedGeneration: 1,
			Conditions: []meta.Condition{{
				Type: api.ProfilingRunConditionExecutionComplete, Status: meta.ConditionUnknown,
				ObservedGeneration: 1, Reason: "JobRunning",
			}},
		},
	}
	pending := queryapi.BuildEvidencePassportState(
		queryapi.EvidencePassportSnapshot{ReadAt: ready.ReadAt, Run: run}, "EvidencePassportPending", "",
	)
	for _, format := range []string{outputFormatJSON, outputFormatTable} {
		t.Run(format, func(t *testing.T) {
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				if err := json.NewEncoder(w).Encode(pending); err != nil {
					t.Error(err)
				}
			}), operationOptions{namespace: run.Namespace, name: run.Name})
			deps := dependencies{newRunShowQuery: func(operationOptions) (evidencePassportQuery, error) {
				return &ownerEvidencePassportQuery{owner}, nil
			}}
			var output strings.Builder
			args := []string{"runs", "show", run.Name, "-n", run.Namespace, "-o", format}
			if err := execute(context.Background(), args, &output, &strings.Builder{}, deps); err != nil {
				t.Fatal(err)
			}
			if format == outputFormatJSON {
				var got queryapi.EvidencePassportState
				if err := json.Unmarshal([]byte(output.String()), &got); err != nil {
					t.Fatal(err)
				}
				if got.Code != pending.Code || got.Evidence != nil || got.Measurement != nil ||
					got.Run.UID != pending.Run.UID || got.Run.ResourceVersion != run.ResourceVersion ||
					len(got.Run.Conditions) != 1 || got.Run.Conditions[0].Reason != "JobRunning" {
					t.Fatalf("HTTP 202 lost its pending/null/native contract: %s", output.String())
				}
			} else if !strings.Contains(output.String(), pending.Code) ||
				!strings.Contains(output.String(), run.Namespace+"/"+run.Name) ||
				strings.Contains(output.String(), "RUNTIME MEASUREMENT") {
				t.Fatalf("pending table presented zero measurements: %s", output.String())
			}
		})
	}
}
