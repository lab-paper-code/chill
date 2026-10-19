package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
)

func operationTestOwner(t *testing.T, handler http.Handler, opts operationOptions) *ownerClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "owner-ca.pem")
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caFile, caPEM, 0600); err != nil {
		t.Fatal(err)
	}
	opts.servingURL, opts.servingCAFile = server.URL, caFile
	owner, err := newOwnerHTTPSClient(opts, &rest.Config{BearerToken: "caller-token"})
	if err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestMeasurementSubmitOwnerFailureAndDryRunState(t *testing.T) {
	for _, test := range []struct {
		name, request string
		httpStatus    int
		dryRun        bool
		wantError     bool
	}{
		{"denied", "", http.StatusForbidden, false, true},
		{"preflight-rejected", "", http.StatusUnprocessableEntity, false, true},
		{"semantic-rejected", "Rejected", http.StatusOK, false, true},
		{"accepted", "Accepted", http.StatusAccepted, false, false},
		{"dry-run", "NOT_SUBMITTED", http.StatusOK, true, false},
		{"dry-run-claimed-submitted", "Accepted", http.StatusOK, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			opts := operationOptions{namespace: "ns"}
			if test.dryRun {
				opts.dryRun = dryRunServer
			}
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/ns/batch-energy-optimizations" ||
					r.Header.Get("Authorization") != "Bearer caller-token" {
					t.Errorf("unexpected submission boundary %s %s", r.Method, r.URL)
				}
				if (r.URL.Query().Get("dryRun") == "All") != test.dryRun {
					t.Errorf("wrong dry-run state %s", r.URL.RawQuery)
				}
				w.WriteHeader(test.httpStatus)
				if test.httpStatus >= 400 {
					_, _ = io.WriteString(w, `{"error":{"code":"Blocked","message":"no mutation permitted"}}`)
					return
				}
				_ = json.NewEncoder(w).Encode(httpapi.MeasurementMutationResult{
					Request: test.request,
					Task:    &edgev1alpha1.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "task"}},
				})
			}), opts)
			q := &measurementClient{ownerClient: owner, manifest: []byte(`{"apiVersion":"edge.dacs.io/v1alpha1",` +
				`"kind":"BatchEnergyOptimization","metadata":{"name":"task"}}`)}
			deps := dependencies{newMeasurements: func(operationOptions) (measurementOperations, error) { return q, nil }}
			args := []string{"measurements", "submit", "-f", "unused.yaml", "-n", "ns", "-o", "json"}
			if test.dryRun {
				args = append(args, "--dry-run=server")
			}
			var stdout strings.Builder
			err := execute(context.Background(), args, &stdout, io.Discard, deps)
			if (err != nil) != test.wantError || requests != 1 {
				t.Fatalf("error=%v requests=%d", err, requests)
			}
			if test.wantError {
				if stdout.Len() != 0 {
					t.Fatal("failed owner submission printed success")
				}
				return
			}
			var result httpapi.MeasurementMutationResult
			if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil || result.Request != test.request {
				t.Fatalf("submission state %#v error %v", result, err)
			}
		})
	}
}

func TestMeasurementDeletePreservesExplicitStalePreconditions(t *testing.T) {
	for _, test := range []struct {
		name, version, uid string
		semanticFailure    bool
		wantError          bool
	}{
		{"stale-version", "old", "uid", false, true},
		{"replaced-object", "rv2", "old-uid", false, true},
		{"semantic-failure", "rv2", "uid", true, true},
		{"cleanup-not-confirmed", "rv2", "uid", true, true},
		{"accepted", "rv2", "uid", false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutations, requests := 0, 0
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/namespaces/ns/batch-energy-optimizations/task" ||
					r.Header.Get("If-Match") != test.version || r.URL.Query().Get("uid") != test.uid {
					t.Errorf("preconditions changed or refreshed: %s %s If-Match=%q", r.Method, r.URL, r.Header.Get("If-Match"))
				}
				if r.Header.Get("If-Match") != "rv2" || r.URL.Query().Get("uid") != "uid" {
					http.Error(w, "stale native precondition", http.StatusConflict)
					return
				}
				result := httpapi.MeasurementMutationResult{Request: "Rejected"}
				if test.name == "cleanup-not-confirmed" {
					result.Request = measurementDeletionRequested
				}
				if !test.semanticFailure {
					mutations++
					result = httpapi.MeasurementMutationResult{
						Request: measurementDeletionRequested, EvidenceDeleted: true,
						Task: &edgev1alpha1.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{
							Namespace: "ns", Name: "task", UID: types.UID("uid"), ResourceVersion: "rv2",
						}}}
				}
				_ = json.NewEncoder(w).Encode(result)
			}), operationOptions{namespace: "ns", name: "task", uid: test.uid, ifMatch: test.version})
			_, err := (&measurementClient{ownerClient: owner}).Delete(context.Background())
			if (err != nil) != test.wantError || requests != 1 {
				t.Fatalf("error=%v requests=%d", err, requests)
			}
			if (mutations == 0) != test.wantError {
				t.Fatalf("unexpected mutations %d", mutations)
			}
		})
	}
}

func TestOwnerMutationFlagsRejectBeforeClientCreation(t *testing.T) {
	for _, args := range [][]string{
		{"measurements", "delete", "task"},
		{"measurements", "delete", "task", "--if-match", "rv"},
		{"measurements", "delete", "task", "--uid", "uid"},
		{"measurements", "list", "-A"},
		{"measurements", "submit", "-f", "task.yaml", "--console-service", "old-console"},
		{preflightCommand, "-f", "task.yaml", "--console-service", "old-console"},
	} {
		called := false
		deps := dependencies{
			newMeasurements: func(operationOptions) (measurementOperations, error) { called = true; return nil, nil },
			newProfiles:     func(operationOptions) (profileOperations, error) { called = true; return nil, nil },
			newPreflightQuery: func(operationOptions) (batchEnergyOptimizationPreflightQuery, error) {
				called = true
				return nil, nil
			},
		}
		if err := execute(context.Background(), args, io.Discard, io.Discard, deps); err == nil || called {
			t.Fatalf("args=%v error=%v clientCreated=%t", args, err, called)
		}
	}
}

func TestMeasurementListRejectsSemanticFailure(t *testing.T) {
	owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"error":{"code":"Forbidden"}}`)
	}), operationOptions{namespace: "ns"})
	if _, err := (&measurementClient{ownerClient: owner}).List(context.Background()); err == nil {
		t.Fatal("HTTP success with semantic failure became an empty successful list")
	}
}

func TestStandalonePreflightOwnerSemanticBoundary(t *testing.T) {
	for _, mode := range []string{"resolves", "rejected", "denied", "wrong-identity"} {
		t.Run(mode, func(t *testing.T) {
			requests := 0
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/namespaces/ns/batch-energy-optimizations/preflight" {
					t.Errorf("preflight attempted mutation: %s %s", r.Method, r.URL)
				}
				if mode == "denied" {
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				result := testBatchEnergyOptimizationPreflight(mode != "rejected")
				result.Campaign.Namespace = "ns"
				if mode == "wrong-identity" {
					result.Campaign.Namespace = "other"
				}
				_ = json.NewEncoder(w).Encode(result)
			}), operationOptions{namespace: "ns"})
			query := &ownerBatchEnergyOptimizationPreflightQuery{ownerClient: owner, manifest: []byte(`{}`)}
			deps := dependencies{newPreflightQuery: func(operationOptions) (batchEnergyOptimizationPreflightQuery, error) {
				return query, nil
			}}
			var stdout strings.Builder
			err := execute(context.Background(), []string{preflightCommand, "-f", "unused.yaml", "-o", "json"},
				&stdout, io.Discard, deps)
			if (err != nil) != (mode != "resolves") || requests != 1 {
				t.Fatalf("error=%v requests=%d", err, requests)
			}
			if mode == "rejected" && !strings.Contains(stdout.String(), "referenced DeviceClass does not exist") {
				t.Fatal("lost semantic rejection result")
			}
			if (mode == "denied" || mode == "wrong-identity") && stdout.Len() != 0 {
				t.Fatal("failed preflight printed success")
			}
		})
	}
}

func TestMeasurementDeletionHTTPFailureRetainsPartialState(t *testing.T) {
	for _, test := range []struct{ code, state string }{
		{"MeasurementEvidenceDeletionIncomplete", "owned raw objects already removed; measurement object retained"},
		{"MeasurementDeletionIncomplete", "owned evidence cleaned; native deletion not confirmed"},
		{"MeasurementAuditIncomplete", "mutation may have been accepted; final audit record not confirmed"},
	} {
		t.Run(test.code, func(t *testing.T) {
			requests := 0
			owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests++
				w.WriteHeader(http.StatusServiceUnavailable)
				_ = json.NewEncoder(w).Encode(map[string]string{"code": test.code, "message": test.state})
			}), operationOptions{namespace: "ns", name: "task", uid: "uid", ifMatch: "rv"})
			deps := dependencies{newMeasurements: func(operationOptions) (measurementOperations, error) {
				return &measurementClient{ownerClient: owner}, nil
			}}
			var stdout strings.Builder
			err := execute(context.Background(), []string{"measurements", "delete", "task", "--uid", "uid",
				"--if-match", "rv", "-o", "json"}, &stdout, io.Discard, deps)
			if err == nil || !strings.Contains(err.Error(), test.state) || !strings.Contains(err.Error(), test.code) ||
				!strings.Contains(err.Error(), "503") || stdout.Len() != 0 || requests != 1 {
				t.Fatalf("partial deletion was hidden/retried: error=%v stdout=%q requests=%d", err, stdout.String(), requests)
			}
		})
	}
}

func TestMeasurementDeletionReportsCleanupWithoutClaimingNativeCompletion(t *testing.T) {
	deleting := metav1.Now()
	expected := httpapi.MeasurementMutationResult{
		Request: measurementDeletionRequested, EvidenceDeleted: true,
		DeletedEvidence: []httpapi.MeasurementEvidenceLocation{
			{Store: "store", Key: "run/raw"}, {Store: "store", Key: "campaign/result"},
		},
		Task: &edgev1alpha1.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns", Name: "task", UID: types.UID("uid"), ResourceVersion: "rv",
			DeletionTimestamp: &deleting, Finalizers: []string{"native-finalizer"},
		}},
	}
	owner := operationTestOwner(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(expected)
	}), operationOptions{namespace: "ns", name: "task", uid: "uid", ifMatch: "rv"})
	deps := dependencies{newMeasurements: func(operationOptions) (measurementOperations, error) {
		return &measurementClient{ownerClient: owner}, nil
	}}
	for _, format := range []string{outputFormatJSON, outputFormatTable} {
		var stdout strings.Builder
		err := execute(context.Background(), []string{"measurements", "delete", "task", "--uid", "uid",
			"--if-match", "rv", "-o", format}, &stdout, io.Discard, deps)
		if err != nil {
			t.Fatal(err)
		}
		if format == outputFormatJSON {
			var result httpapi.MeasurementMutationResult
			if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil ||
				result.Request != measurementDeletionRequested || !result.EvidenceDeleted ||
				len(result.DeletedEvidence) != 2 || result.DeletedEvidence[0] != expected.DeletedEvidence[0] ||
				result.Task == nil || result.Task.DeletionTimestamp == nil || len(result.Task.Finalizers) != 1 {
				t.Fatalf("lost cleanup/native-lifecycle state %#v error=%v", result, err)
			}
		} else if !strings.Contains(stdout.String(), "OWNED_KEYS") ||
			!strings.Contains(stdout.String(), measurementDeletionRequested) || !strings.Contains(stdout.String(), "true") {
			t.Fatalf("misleading deletion summary %q", stdout.String())
		}
	}
}
