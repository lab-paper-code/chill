package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/queryapi/readapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type failingPassportLoader struct{ calls int }

func (l *failingPassportLoader) Load(
	context.Context,
	*api.ProfilingRun,
	evidencestore.ObjectIdentity,
) (querykube.VerifiedEvidenceView, error) {
	l.calls++
	return nil, errors.New("retained evidence unavailable")
}

type failingResultReader struct{}

func (failingResultReader) Open(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	return nil, errors.New("completed Run measurement contradicts result")
}

type unusedPreflightReader struct{}

func (unusedPreflightReader) Read(
	context.Context,
	*api.BatchEnergyOptimization,
) (queryapi.BatchEnergyOptimizationPreflightSnapshot, error) {
	return queryapi.BatchEnergyOptimizationPreflightSnapshot{}, errors.New("unexpected preflight")
}

func evidenceReadHandler(t *testing.T, loader *failingPassportLoader, objects ...client.Object) http.Handler {
	t.Helper()
	reader := fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(objects...).Build()
	passports, err := querykube.NewKubernetesEvidencePassportQuery(reader, loader)
	if err != nil {
		t.Fatal(err)
	}
	results, err := querykube.NewKubernetesBatchEnergyOptimizationResultQuery(reader, failingResultReader{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := readapi.NewHandler(passports, results, unusedPreflightReader{})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestMeasurementPassportPendingIsNotAnEvidenceConnectionFailure(t *testing.T) {
	for _, state := range []string{
		"running", "pending", "failed", "previous generation", "invalid evidence", "published",
	} {
		t.Run(state, func(t *testing.T) {
			run := &api.ProfilingRun{
				ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "run", UID: "run-uid", Generation: 2},
				Status: api.ProfilingRunStatus{
					ObservedGeneration: 2,
					Conditions: []meta.Condition{
						{
							Type:               api.ProfilingRunConditionExecutionComplete,
							Status:             meta.ConditionUnknown,
							ObservedGeneration: 2,
							Reason:             "JobRunning",
							Message:            "measurement Job has not completed",
						},
					},
				},
			}
			switch state {
			case "pending":
				run.Status.Conditions[0].Reason = "JobPending"
			case "failed":
				run.Status.Conditions[0].Status = meta.ConditionFalse
				run.Status.Conditions[0].Reason = "JobFailed"
			case "previous generation":
				run.Status.ObservedGeneration = 1
			case "invalid evidence":
				run.Status.Conditions = append(
					run.Status.Conditions,
					meta.Condition{
						Type:               api.ProfilingRunConditionEvidenceVerified,
						Status:             meta.ConditionFalse,
						ObservedGeneration: 2,
						Reason:             "EvidenceInvalid",
					},
				)
			case "published":
				run.Status.Evidence = &api.ProfilingRunEvidenceReference{
					Store:         "store",
					Key:           "execution.json",
					Size:          8,
					SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
				}
			}
			loader := &failingPassportLoader{}
			response := httptest.NewRecorder()
			evidenceReadHandler(
				t,
				loader,
				run,
			).ServeHTTP(response, httptest.NewRequest(
				http.MethodGet, "/api/v1/namespaces/allowed/profiling-runs/run/evidence-passport", nil,
			))
			var body queryapi.EvidencePassportState
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Run.UID != "run-uid" || body.Run.Conditions[0].Reason != run.Status.Conditions[0].Reason ||
				body.Measurement != nil ||
				body.Evidence != nil ||
				body.Assessment.Evidence.Accepted {
				t.Fatalf("native metadata or null evidence lost: %s", response.Body.String())
			}
			pending := state == "running" || state == "pending"
			if pending {
				if response.Code != http.StatusAccepted || body.Code != "EvidencePassportPending" ||
					loader.calls != 0 ||
					body.Assessment.DisplayStatus != queryapi.MeasurementRunning {
					t.Fatalf("pending became evidence failure: %d %s", response.Code, response.Body.String())
				}
				t.Logf(
					"pending: status=%d code=%s uid=%s evidence=null measurement=null",
					response.Code,
					body.Code,
					body.Run.UID,
				)
			} else if response.Code != http.StatusBadGateway || body.Code != "EvidencePassportUnavailable" {
				t.Fatalf("stale/invalid/published failure hidden: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMeasurementResultFailureRetainsNativeRequestWithoutInventingEvidence(t *testing.T) {
	for _, phase := range []string{"Failed", "Complete"} {
		t.Run(phase, func(t *testing.T) {
			task := &api.BatchEnergyOptimization{
				ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "request", UID: "request-uid", Generation: 1},
				Status: api.BatchEnergyOptimizationStatus{
					ObservedGeneration: 1,
					Phase:              phase,
					Outcome:            "RetryLimitExhausted",
					CompletedTrials:    3,
					Result: &api.ProfilingRunEvidenceReference{
						Store: "store",
						Key:   "result.json",
						Size:  100,
					},
					Conditions: []meta.Condition{
						{
							Type:               "MeasurementComplete",
							Status:             meta.ConditionFalse,
							ObservedGeneration: 1,
							Reason:             "RetryLimitExhausted",
							Message:            "runtime failed repeatedly",
						},
					},
				},
			}
			response := httptest.NewRecorder()
			evidenceReadHandler(
				t,
				&failingPassportLoader{},
				task,
			).ServeHTTP(response, httptest.NewRequest(
				http.MethodGet, "/api/v1/namespaces/allowed/batch-energy-optimizations/request/result", nil,
			))
			var body queryapi.BatchEnergyOptimizationResultError
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusBadGateway || body.Code != "BatchEnergyOptimizationResultUnavailable" ||
				body.EvidenceError.Status != response.Code ||
				body.EvidenceError.Code != body.Code ||
				!strings.Contains(body.Reason, "completed Run measurement contradicts result") ||
				body.Campaign.UID != string(task.UID) ||
				body.Campaign.Phase != phase ||
				body.Campaign.CompletedTrials != 3 ||
				body.Campaign.Conditions[0].Message != "runtime failed repeatedly" ||
				body.Result != nil ||
				body.MeasurementFacts != nil ||
				body.Assessment.Evidence.Accepted {
				t.Fatalf("native failure or original error lost: %d %s", response.Code, response.Body.String())
			}
			if phase == "Failed" &&
				(body.Assessment.DisplayStatus != queryapi.MeasurementProblem || len(body.Assessment.FailureSummary) == 0) {
				t.Fatalf("failure cause lost: %+v", body.Assessment)
			}
			t.Logf(
				"evidence-error: status=%d code=%s uid=%s phase=%s trials=%d result=null reason=%s",
				response.Code,
				body.Code,
				body.Campaign.UID,
				body.Campaign.Phase,
				body.Campaign.CompletedTrials,
				body.Reason,
			)
		})
	}
}

func TestR18RequestResultJoinsOnlyExactAcceptedSelection(t *testing.T) {
	request, record, _, _, _ := statisticsFixture(t)
	request.Status.Phase = "Complete"
	id := coverageRequestID(*request)
	selected, unselected := true, false
	for _, tc := range []struct {
		name       string
		currentIDs []string
		noAccepted bool
		catalogErr error
		want       *bool
	}{
		{name: "selected request", currentIDs: []string{id}, want: &selected},
		{name: "same condition different request", currentIDs: []string{request.Namespace + "/other/other-uid"}, want: &unselected},
		{name: "same name different UID", currentIDs: []string{request.Namespace + "/" + request.Name + "/other-uid"}, want: &unselected},
		{name: "different namespace", currentIDs: []string{"other/" + request.Name + "/" + string(request.UID)}, want: &unselected},
		{name: "reuse profile is not confirmation identity", currentIDs: []string{id + "/reuse-target"}, want: &unselected},
		{name: "known empty selection", currentIDs: []string{}, want: &unselected},
		{name: "selection facts missing"},
		{name: "accepted catalog missing", currentIDs: []string{id}, noAccepted: true},
		{name: "catalog unavailable", catalogErr: errors.New("owner unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := []profilederivation.MeasurementSelection(nil)
			if tc.currentIDs != nil {
				current = make([]profilederivation.MeasurementSelection, 0, len(tc.currentIDs))
				for _, currentID := range tc.currentIDs {
					current = append(current, profilederivation.MeasurementSelection{
						ID: currentID, Model: request.Spec.State.Candidate.ModelSpec,
						DeviceClass:   request.Spec.State.Candidate.DeviceClass,
						ExecutionPath: request.Spec.State.Candidate.ExecutionPath,
						PowerMode:     request.Spec.State.Candidate.PowerMode,
					})
				}
			}
			accepted := &servingkube.CatalogSnapshot{CurrentMeasurements: current}
			if tc.noAccepted {
				accepted = nil
			}
			service := &measurementService{catalog: func(context.Context) (servingkube.CatalogRead, error) {
				return servingkube.CatalogRead{
					// Current reporting must not replace the accepted selection.
					CatalogSnapshot: servingkube.CatalogSnapshot{
						CurrentMeasurements: []profilederivation.MeasurementSelection{{ID: id}},
					},
					Accepted: accepted,
				}, tc.catalogErr
			}}
			readAt := time.Unix(42, 0).UTC()
			reader := measurementResultReader{
				read: func(_ context.Context, namespace, name string) (queryapi.BatchEnergyOptimizationResultSnapshot, error) {
					if namespace != request.Namespace || name != request.Name {
						t.Fatalf("request identity changed: %s/%s", namespace, name)
					}
					return queryapi.BatchEnergyOptimizationResultSnapshot{
						ReadAt: readAt, Campaign: *request, Result: record,
						MeasurementFacts: &profilederivation.MeasurementFacts{
							Batches: []profilederivation.MeasurementBatchFacts{{Batch: 1}},
						},
					}, nil
				},
				measurements: service,
			}
			handler, err := readapi.NewHandler(nilPassportReader{}, reader, unusedPreflightReader{})
			if err != nil {
				t.Fatal(err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet, "/api/v1/namespaces/"+request.Namespace+"/batch-energy-optimizations/"+request.Name+"/result", nil,
			))
			var result queryapi.BatchEnergyOptimizationResult
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK || !result.ReadAt.Equal(readAt) || len(result.ResultRows) != 1 {
				t.Fatalf("result/source time changed: status=%d result=%+v", response.Code, result)
			}
			for _, scope := range []queryapi.MeasurementScope{result.Assessment.Scope, result.ResultRows[0].Assessment.Scope} {
				if (scope.SchedulerUsed == nil) != (tc.want == nil) ||
					tc.want != nil && *scope.SchedulerUsed != *tc.want {
					t.Fatalf("selection=%v scope=%+v want=%v", tc.currentIDs, scope, tc.want)
				}
			}
		})
	}
}

type nilPassportReader struct{}

func (nilPassportReader) Read(context.Context, string, string) (queryapi.EvidencePassportSnapshot, error) {
	return queryapi.EvidencePassportSnapshot{}, errors.New("unexpected passport")
}
