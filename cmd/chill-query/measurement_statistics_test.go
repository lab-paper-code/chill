package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	campaignevidence "github.com/lab-paper-code/chill/internal/profiling/campaign/evidence"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	evidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	projection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	runconfig "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/queryapi/readapi"
	"github.com/lab-paper-code/chill/internal/workloadobserver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const statisticsResultUnavailableCode = "BatchEnergyOptimizationResultUnavailable"

type statisticsObjects struct {
	mu       sync.Mutex
	payloads map[evidencestore.ObjectLocation][]byte
	err      error
	reads    []evidencestore.ObjectLocation
}

func (s *statisticsObjects) Open(
	ctx context.Context,
	id evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	return s.OpenLocation(ctx, id.Location())
}

func (s *statisticsObjects) OpenLocation(
	ctx context.Context,
	loc evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, loc)
	if s.err != nil {
		return nil, s.err
	}
	payload, ok := s.payloads[loc]
	if !ok {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}

func TestMeasurementStatisticsRetainedReferencesOnly(t *testing.T) {
	cr, record, objects, reader, loader := statisticsFixture(t)
	before := record.Spec()
	statistics, reason, err := newMeasurementStatistics(
		reader,
		objects,
		loader,
	)(
		context.Background(),
		cr,
		record,
	)
	if err != nil || reason != nil || statistics == nil {
		t.Fatalf("statistics=%+v reason=%v err=%v", statistics, reason, err)
	}
	if len(statistics.Batches) != 1 {
		t.Fatal(statistics)
	}
	row := statistics.Batches[0]
	if row.Batch != 1 || row.ActiveTrialCount != 2 || row.IdleTrialCount != 2 ||
		row.LatencySampleCount != 42 ||
		row.LatencyP95Milliseconds != 1 ||
		row.Confidence.Level != .8 ||
		row.Confidence.HalfWidth == 0 ||
		len(row.Sources) != 9 {
		t.Fatalf("wrong independent trials/pooled samples/CI/sources: %+v", row)
	}
	assertRetainedMeasurementFacts(t, statistics, string(cr.UID))
	if len(objects.reads) != 9 {
		t.Fatalf("unreferenced/excluded evidence read: %v", objects.reads)
	}
	if !reflect.DeepEqual(record.Spec(), before) {
		t.Fatal("sealed record changed")
	}
	view, err := queryapi.BuildBatchEnergyOptimizationResult(
		queryapi.BatchEnergyOptimizationResultSnapshot{
			Campaign:   *cr,
			Result:     record,
			Statistics: statistics,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if view.MeasurementFacts == nil || view.MeasurementFactsReason != nil || len(view.ResultRows) != 1 {
		t.Fatalf("result omitted measurement facts siblings/rows: %+v", view)
	}
	if _, err := json.Marshal(view); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(view.Result.ResultRecordSpec, before) {
		t.Fatal("sealed detail projection changed")
	}
}

func assertRetainedMeasurementFacts(
	t *testing.T,
	statistics *queryapi.BatchEnergyOptimizationStatistics,
	requestUID string,
) {
	t.Helper()
	facts := statistics.MeasurementFacts
	if facts == nil || facts.Request.UID != requestUID || len(facts.Batches) != 1 {
		t.Fatalf("retained measurement facts missing: %+v", facts)
	}
	measured, row := facts.Batches[0], statistics.Batches[0]
	if measured.P95Milliseconds == nil || *measured.P95Milliseconds != row.LatencyP95Milliseconds ||
		measured.Confidence == nil || measured.Confidence.Level != row.Confidence.Level ||
		measured.ActiveTrialCount != 2 || measured.IdleTrialCount != 2 || len(measured.Quality) != 4 ||
		measured.TotalJoulesPerItem == nil || measured.ActiveWatts == nil || measured.IdleWatts == nil {
		t.Fatalf("facts diverged from retained statistics: %+v", measured)
	}
}

func TestMeasurementStatisticsMissingAndErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		reason string
	}{
		{"missing raw", evidencestore.ErrNotFound, statisticsEvidenceMissingReason},
		{"authentication", evidencestore.ErrAuthentication, ""},
		{"transport", evidencestore.ErrTimeout, ""},
		{"forbidden", apierrors.NewForbidden(
			schema.GroupResource{Resource: "profilingruns"}, "run", errors.New("denied"),
		), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			cr, record, objects, reader, loader := statisticsFixture(t)
			objects.err = test.err
			got, reason, err := newMeasurementStatistics(
				reader,
				objects,
				loader,
			)(
				context.Background(),
				cr,
				record,
			)
			if got != nil {
				t.Fatal(got)
			}
			if test.reason != "" {
				if err != nil || reason == nil || *reason != test.reason {
					t.Fatalf("reason=%v err=%v", reason, err)
				}
			} else if !errors.Is(err, test.err) || reason != nil {
				t.Fatalf("hidden dependency error: reason=%v err=%v", reason, err)
			}
		})
	}
	cr, record, objects, reader, loader := statisticsFixture(t)
	missing := &api.ProfilingRun{}
	ref := record.Spec().CompletedTrials[0]
	key := types.NamespacedName{Namespace: ref.Run.Namespace, Name: ref.Run.Name}
	if err := reader.Get(context.Background(), key, missing); err != nil {
		t.Fatal(err)
	}
	if err := reader.Delete(context.Background(), missing); err != nil {
		t.Fatal(err)
	}
	_, reason, err := newMeasurementStatistics(
		reader,
		objects,
		loader,
	)(
		context.Background(),
		cr,
		record,
	)
	if err != nil || reason == nil || *reason != statisticsEvidenceMissingReason {
		t.Fatalf("missing Run reason=%v err=%v", reason, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, reason, err = newMeasurementStatistics(reader, objects, loader)(ctx, cr, record)
	if !errors.Is(err, context.Canceled) || reason != nil {
		t.Fatalf("cancellation reason=%v err=%v", reason, err)
	}
}

func TestMeasurementStatisticsRejectsForeignRunAndCorruption(t *testing.T) {
	for _, mutate := range []func(*api.ProfilingRun){
		func(r *api.ProfilingRun) { r.UID = "replacement" },
		func(r *api.ProfilingRun) { r.OwnerReferences[0].UID = "foreign" },
		func(r *api.ProfilingRun) { r.Spec.Measurement.Batch = 2 },
		func(r *api.ProfilingRun) { r.Spec.Measurement.DurationSeconds++ },
		func(r *api.ProfilingRun) { r.Status.Evidence.Key = "other" },
	} {
		cr, record, objects, reader, loader := statisticsFixture(t)
		ref := record.Spec().CompletedTrials[0]
		run := &api.ProfilingRun{}
		key := types.NamespacedName{Namespace: ref.Run.Namespace, Name: ref.Run.Name}
		if err := reader.Get(context.Background(), key, run); err != nil {
			t.Fatal(err)
		}
		mutate(run)
		if err := reader.Update(context.Background(), run); err != nil {
			t.Fatal(err)
		}
		got, reason, err := newMeasurementStatistics(reader, objects, loader)(context.Background(), cr, record)
		if err == nil || got != nil || reason != nil {
			t.Fatalf("foreign Run accepted: %+v %v %v", got, reason, err)
		}
	}
	cr, record, objects, reader, loader := statisticsFixture(t)
	ref := record.Spec().CurvePlan
	plan := evidencestore.ObjectIdentity{Store: ref.Store, Key: ref.Key, Size: ref.Size}
	objects.payloads[plan.Location()] = bytes.Repeat([]byte("x"), int(plan.Size))
	if _, reason, err := newMeasurementStatistics(reader, objects, loader)(context.Background(), cr, record); err == nil ||
		reason != nil {
		t.Fatalf("corrupt plan hidden: %v %v", reason, err)
	}
	huge := evidencestore.ObjectIdentity{Store: "evidence", Key: "huge", Size: 65 << 20}
	if _, err := readStatisticsObject(context.Background(), objects, huge); err == nil {
		t.Fatal("unbounded plan read")
	}
}

func TestMeasurementStatisticsMissingRawDoesNotMaskMissingCredentials(t *testing.T) {
	cr, record, objects, reader, loader := statisticsFixture(t)
	ref := record.Spec().CompletedTrials[0]
	delete(objects.payloads, evidencestore.ObjectLocation{Store: ref.Evidence.Store, Key: ref.Run.Name + "/raw"})
	statistics, reason, err := newMeasurementStatistics(reader, objects, loader)(context.Background(), cr, record)
	if statistics != nil || err != nil || reason == nil || *reason != statisticsEvidenceMissingReason {
		t.Fatalf("missing raw: statistics=%+v reason=%v err=%v", statistics, reason, err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: cr.Namespace, Name: "credentials"}}
	if err := reader.Delete(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	statistics, reason, err = newMeasurementStatistics(reader, objects, loader)(context.Background(), cr, record)
	if statistics != nil || reason != nil || !apierrors.IsNotFound(err) {
		t.Fatalf("missing credentials masked: statistics=%+v reason=%v err=%v", statistics, reason, err)
	}
}

func TestMeasurementStatisticsNotRecorded(t *testing.T) {
	cr, record, objects, reader, loader := statisticsFixture(t)
	spec := record.Spec()
	spec.CompletedTrials = nil
	record, err := campaign.NewResultRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	got, reason, err := newMeasurementStatistics(
		reader,
		objects,
		loader,
	)(
		context.Background(),
		cr,
		record,
	)
	if err != nil || got != nil || reason == nil || *reason != "StatisticsNotRecorded" {
		t.Fatalf("unrecorded statistics=%+v reason=%v err=%v", got, reason, err)
	}
}

func TestMeasurementFactsExcludeFailedIdleTrials(t *testing.T) {
	cr, record, objects, reader, loader := statisticsFixture(t)
	spec := record.Spec()
	spec.ExcludedTrials = append(spec.ExcludedTrials, campaign.ExcludedTrialReference{
		Run:         campaign.RunReference{Namespace: cr.Namespace, Name: "failed-idle", UID: "failed-idle"},
		Measurement: campaign.ResultMeasurement{WorkloadMode: campaign.WorkloadModeIdle, Batch: 1},
		Attempt:     2, Reasons: []string{"Failed"},
	})
	record, err := campaign.NewResultRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	statistics, reason, err := newMeasurementStatistics(reader, objects, loader)(context.Background(), cr, record)
	if err != nil || reason != nil || statistics == nil || statistics.MeasurementFacts == nil {
		t.Fatalf("statistics=%+v reason=%v error=%v", statistics, reason, err)
	}
	row := statistics.MeasurementFacts.Batches[0]
	if row.ActiveTrialCount != 2 || row.IdleTrialCount != 2 || len(row.Quality) != 4 || len(objects.reads) != 9 {
		t.Fatalf(
			"excluded failed idle became an independent repetition or extra raw read: %+v reads=%v",
			row,
			objects.reads,
		)
	}
}

func TestMeasurementStatisticsReaderReasonsHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		trial  int
		mutate func(*api.ProfilingRun)
		reason string
	}{
		{"Run UID", 0, func(r *api.ProfilingRun) { r.UID = "replacement" },
			"completed Run identity or campaign ownership mismatch"},
		{"owner UID", 0, func(r *api.ProfilingRun) { r.OwnerReferences[0].UID = "foreign" },
			"completed Run identity or campaign ownership mismatch"},
		{"owner kind before batch", 0, func(r *api.ProfilingRun) {
			r.OwnerReferences[0].Kind = "Other"
			r.Spec.Measurement.Batch++
		}, "completed Run identity or campaign ownership mismatch"},
		{"owner API version", 0, func(r *api.ProfilingRun) { r.OwnerReferences[0].APIVersion = "other/v1" },
			"completed Run identity or campaign ownership mismatch"},
		{"batch", 0, func(r *api.ProfilingRun) { r.Spec.Measurement.Batch++ },
			"completed Run measurement contradicts result"},
		{"unsupported active", 0, func(r *api.ProfilingRun) { r.Spec.Measurement.WorkloadMode = "unsupported" },
			"completed Run workload mode is unsupported"},
		{"unsupported idle", 2, func(r *api.ProfilingRun) { r.Spec.Measurement.WorkloadMode = "unsupported" },
			"completed Run workload mode is unsupported"},
		{"mode reference", 0, func(r *api.ProfilingRun) { r.Spec.Measurement.WorkloadMode = "idle-model-loaded" },
			"completed Run workload mode contradicts result"},
		{"completion", 0, func(r *api.ProfilingRun) { r.Status.Conditions = nil },
			"completed Run lacks current verified completion"},
		{"completion before evidence and protocol", 0, func(r *api.ProfilingRun) {
			r.Status.Conditions[0].ObservedGeneration++
			r.Status.Evidence = nil
			r.Spec.Measurement.DurationSeconds++
		}, "completed Run lacks current verified completion"},
		{"absent evidence before protocol", 0, func(r *api.ProfilingRun) {
			r.Status.Evidence = nil
			r.Spec.Measurement.DurationSeconds++
		}, "completed evidence reference is absent from Run"},
		{"evidence identity before protocol", 0, func(r *api.ProfilingRun) {
			r.Status.Evidence.Key = "different-key"
			r.Spec.Measurement.DurationSeconds++
		}, "completed evidence reference contradicts Run"},
		{"evidence schema", 0, func(r *api.ProfilingRun) { r.Status.Evidence.SchemaVersion = "different-schema" },
			"completed evidence reference contradicts Run"},
		{"active protocol", 0, func(r *api.ProfilingRun) { r.Spec.Measurement.DurationSeconds++ },
			"completed active Run protocol contradicts campaign"},
		{"idle protocol", 2, func(r *api.ProfilingRun) { r.Spec.Measurement.DurationSeconds++ },
			"completed idle Run protocol contradicts campaign"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cr, record, objects, reader, loader := statisticsFixture(t)
			ref := record.Spec().CompletedTrials[tc.trial]
			run := &api.ProfilingRun{}
			if err := reader.Get(context.Background(), client.ObjectKey{
				Namespace: ref.Run.Namespace, Name: ref.Run.Name,
			}, run); err != nil {
				t.Fatal(err)
			}
			tc.mutate(run)
			if err := reader.Update(context.Background(), run); err != nil {
				t.Fatal(err)
			}
			response := statisticsHTTPResponse(t, cr, record, objects, reader, loader)
			var body queryapi.BatchEnergyOptimizationResultError
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			wantReason := fmt.Sprintf("read BatchEnergyOptimization %s/%s statistics: %s", cr.Namespace, cr.Name, tc.reason)
			if response.Code != http.StatusBadGateway ||
				body.Code != statisticsResultUnavailableCode ||
				body.EvidenceError.Code != body.Code || body.EvidenceError.Status != response.Code ||
				body.Reason != wantReason || body.Campaign.UID != string(cr.UID) ||
				body.Result != nil || body.Statistics != nil || body.MeasurementFacts != nil {
				t.Fatalf("response changed: status=%d body=%s; want reason %q", response.Code, response.Body.String(), wantReason)
			}
		})
	}
}

func TestMeasurementStatisticsSameNameDifferentUIDHTTP(t *testing.T) {
	cr, record, objects, reader, loader := statisticsFixture(t)
	spec := record.Spec()
	originalUID := spec.CompletedTrials[0].Run.UID
	spec.CompletedTrials[1] = spec.CompletedTrials[0]
	spec.CompletedTrials[1].Run.UID += "-replacement"
	var err error
	record, err = campaign.NewResultRecord(spec)
	if err != nil {
		t.Fatal(err)
	}
	if record.Spec().CompletedTrials[0].Run.UID != originalUID {
		t.Fatal("regression requires the valid Run reference before the same-name replacement")
	}
	response := statisticsHTTPResponse(t, cr, record, objects, reader, loader)
	var body queryapi.BatchEnergyOptimizationResultError
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	wantReason := fmt.Sprintf(
		"read BatchEnergyOptimization %s/%s statistics: completed Run identity or campaign ownership mismatch",
		cr.Namespace, cr.Name,
	)
	if response.Code != http.StatusBadGateway || body.Code != statisticsResultUnavailableCode ||
		body.EvidenceError.Code != body.Code || body.EvidenceError.Status != response.Code ||
		body.Reason != wantReason || body.Campaign.UID != string(cr.UID) ||
		body.Result != nil || body.Statistics != nil || body.MeasurementFacts != nil {
		t.Fatalf("replacement Run response changed: status=%d body=%s; want reason %q",
			response.Code, response.Body.String(), wantReason)
	}
}

func TestMeasurementStatisticsReportingPolicyHTTP(t *testing.T) {
	for _, tc := range []string{
		"original status", "different status schema", "missing Run", "missing raw", "missing credentials", "not recorded",
	} {
		t.Run(tc, func(t *testing.T) {
			cr, record, objects, reader, loader := statisticsFixture(t)
			var wantReason string
			switch tc {
			case "different status schema":
				cr.Status.Result.SchemaVersion = "different"
			case "missing Run":
				ref := record.Spec().CompletedTrials[0]
				if err := reader.Delete(context.Background(), &api.ProfilingRun{
					ObjectMeta: metav1.ObjectMeta{Namespace: ref.Run.Namespace, Name: ref.Run.Name},
				}); err != nil {
					t.Fatal(err)
				}
				wantReason = statisticsEvidenceMissingReason
			case "missing raw":
				ref := record.Spec().CompletedTrials[0]
				delete(objects.payloads, evidencestore.ObjectLocation{Store: ref.Evidence.Store, Key: ref.Run.Name + "/raw"})
				wantReason = statisticsEvidenceMissingReason
			case "missing credentials":
				if err := reader.Delete(context.Background(), &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Namespace: cr.Namespace, Name: "credentials"},
				}); err != nil {
					t.Fatal(err)
				}
			case "not recorded":
				spec := record.Spec()
				spec.CompletedTrials = nil
				var err error
				record, err = campaign.NewResultRecord(spec)
				if err != nil {
					t.Fatal(err)
				}
				wantReason = statisticsNotRecordedReason
			}
			response := statisticsHTTPResponse(t, cr, record, objects, reader, loader)
			if tc == "missing credentials" {
				var body queryapi.BatchEnergyOptimizationResultError
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if response.Code != http.StatusNotFound || body.Code != statisticsResultUnavailableCode ||
					body.Result != nil || !strings.Contains(body.Reason, `secrets "credentials" not found`) {
					t.Fatalf("credentials failure hidden: %d %s", response.Code, response.Body.String())
				}
				return
			}
			assertStatisticsReportingResponse(t, response, record, wantReason)
		})
	}
}

func assertStatisticsReportingResponse(
	t *testing.T,
	response *httptest.ResponseRecorder,
	record campaign.ResultRecord,
	wantReason string,
) {
	t.Helper()
	var body queryapi.BatchEnergyOptimizationResult
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || body.Result == nil ||
		!reflect.DeepEqual(body.Result.ResultRecordSpec, record.Spec()) {
		t.Fatalf("reporting result changed: %d %s", response.Code, response.Body.String())
	}
	if wantReason != "" {
		if body.Statistics != nil || body.MeasurementFacts != nil ||
			body.StatisticsReason == nil || *body.StatisticsReason != wantReason ||
			body.MeasurementFactsReason == nil || *body.MeasurementFactsReason != wantReason {
			t.Fatalf("reporting reason changed: %s", response.Body.String())
		}
	} else if body.Statistics == nil || len(body.Statistics.Batches) != 1 ||
		body.StatisticsReason != nil || body.MeasurementFactsReason != nil ||
		body.Statistics.Batches[0].ActiveTrialCount != 2 || body.Statistics.Batches[0].IdleTrialCount != 2 ||
		body.Statistics.Batches[0].LatencySampleCount != 42 {
		t.Fatalf("statistics changed: %s", response.Body.String())
	}
}

func statisticsHTTPResponse(
	t *testing.T,
	cr *api.BatchEnergyOptimization,
	record campaign.ResultRecord,
	objects *statisticsObjects,
	reader client.Client,
	loader *evidencekube.RetainedLoader,
) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := record.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	objects.payloads[evidencestore.ObjectLocation{Store: cr.Status.Result.Store, Key: cr.Status.Result.Key}] = payload
	cr.Status.Result.Size = int64(len(payload))
	cr.Status.Phase = string(api.MeasurementPhaseComplete)
	if err := reader.Create(context.Background(), cr); err != nil {
		t.Fatal(err)
	}
	results, err := querykube.NewKubernetesBatchEnergyOptimizationResultQuery(
		reader, objects, newMeasurementStatistics(reader, objects, loader),
	)
	if err != nil {
		t.Fatal(err)
	}
	passports, err := querykube.NewKubernetesEvidencePassportQuery(reader, &failingPassportLoader{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := readapi.NewHandler(passports, results, unusedPreflightReader{})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v1/namespaces/%s/batch-energy-optimizations/%s/result", cr.Namespace, cr.Name), nil))
	return response
}

func TestStatisticsCampaignErrorPreservesCauses(t *testing.T) {
	for _, cause := range []error{
		campaignevidence.ErrRunEvidenceAbsent, campaignevidence.ErrRunEvidenceMismatch,
		campaignevidence.ErrUnsupportedWorkloadMode, campaignevidence.ErrActiveProtocolMismatch,
	} {
		err := statisticsCampaignError(fmt.Errorf("completed Run namespace/name: %w", cause))
		if !errors.Is(err, cause) {
			t.Fatalf("lost shared cause %v: %v", cause, err)
		}
	}
	for _, prefix := range []string{
		"read frozen curve plan: ", "get completed Run namespace/name: ",
		"load active Run namespace/name: ", "load idle Run namespace/name: ",
	} {
		cause := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "credentials", errors.New("denied"))
		err := statisticsCampaignError(fmt.Errorf("%s%w", prefix, cause))
		var status *apierrors.StatusError
		if err != cause || !errors.As(err, &status) || !apierrors.IsForbidden(err) {
			t.Fatalf("dependency error changed: %v", err)
		}
	}
}

func statisticsFixture(
	t *testing.T,
) (
	*api.BatchEnergyOptimization,
	campaign.ResultRecord,
	*statisticsObjects,
	client.Client,
	*evidencekube.RetainedLoader,
) {
	t.Helper()
	payload, err := os.ReadFile(
		"../../internal/profiling/profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json",
	)
	if err != nil {
		t.Fatal(err)
	}
	var base runconfig.RunConfig
	if err := json.Unmarshal(payload, &base); err != nil {
		t.Fatal(err)
	}
	base.SchemaVersion = runconfig.RunConfigSchemaVersion
	base.State.ArtifactFiles = []string{"/artifact/mobilenet-v2-050.onnx"}
	base.Images.WorkloadObserver = "registry.example/workload-observer@sha256:" + strings.Repeat(
		"d",
		64,
	)
	base.Workload = runconfig.CurrentWorkloadContract()
	idle := base
	idle.Measurement = runconfig.MeasurementContract{
		Batch:                   1,
		SettlingDurationSeconds: 60,
		DurationSeconds:         30,
		WorkloadMode:            runconfig.IdleModelLoadedWorkloadMode,
	}
	shared, err := projection.ProjectCurvePlanSpec(
		"statistics",
		base,
		idle,
		derivation.CurveConversionContract{},
		[]int{1, 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := derivation.NewCurvePlan(shared)
	if err != nil {
		t.Fatal(err)
	}
	planBytes, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	objects := &statisticsObjects{
		payloads: map[evidencestore.ObjectLocation][]byte{
			{Store: "evidence", Key: "plan"}: planBytes,
		},
	}
	cr := &api.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system",
			Name:      "campaign",
			UID:       "campaign-uid",
		},
		Spec: api.BatchEnergyOptimizationSpec{
			State: api.MeasurementStateSpec{
				TargetNode: base.TargetNode.Name,
				Candidate: api.ProfilingRunCandidateSpec{
					ModelSpec:     base.Candidate.Inputs.ModelSpec.Name,
					DeviceClass:   base.State.DeviceClass,
					ExecutionPath: base.Candidate.ExecutionPath,
					PowerMode:     base.State.PowerMode,
				},
			},
			TrialsPerBatch: 2, PointPolicy: api.BatchEnergyOptimizationPointPolicySpec{ConfidenceLevel: "0.8"},
			ActiveProtocol: api.ActiveProtocolSpec{
				WarmupIterations: int32(base.Measurement.WarmupIterations),
				DurationSeconds:  30,
				Dataset:          base.Measurement.Dataset.Name,
			},
			IdleProtocol: api.IdleProtocolSpec{SettlingDurationSeconds: 60, DurationSeconds: 30},
		},
	}
	metricProtocol := api.ProfilingRunMetricObservationSpec{
		IntervalMilliseconds: int32(base.Metrics.IntervalMilliseconds),
		TimeoutMilliseconds:  int32(base.Metrics.TimeoutMilliseconds),
		DurationSeconds:      int32(base.Metrics.DurationSeconds),
	}
	cr.Spec.ActiveProtocol.MetricObservation, cr.Spec.IdleProtocol.MetricObservation = metricProtocol, metricProtocol
	var refs []campaign.CompletedTrialReference
	var native []client.Object
	for _, isIdle := range []bool{false, true} {
		for index := range 2 {
			config := base
			if isIdle {
				config = idle
			}
			name := fmt.Sprintf("trial-%t-%d", isIdle, index)
			execution, raw := statisticsTrial(t, config, name, isIdle, index)
			rawID := execution.RawMeasurement
			objects.payloads[rawID.Location()] = raw
			data, err := json.Marshal(execution)
			if err != nil {
				t.Fatal(err)
			}
			id := evidencestore.ObjectIdentity{
				Store: "evidence",
				Key:   name + "/execution",
				Size:  int64(len(data)),
			}
			objects.payloads[id.Location()] = data
			mode := campaign.WorkloadModeActive
			measurement := api.ProfilingRunMeasurementSpec{
				Batch:            1,
				WorkloadMode:     config.Measurement.WorkloadMode,
				DurationSeconds:  30,
				WarmupIterations: int32(config.Measurement.WarmupIterations),
				Dataset:          cr.Spec.ActiveProtocol.Dataset,
			}
			if isIdle {
				mode = campaign.WorkloadModeIdle
				measurement.Dataset = ""
				measurement.SettlingDurationSeconds = ptr.To(int32(60))
			}
			ref := campaign.CompletedTrialReference{
				Run:         campaign.RunReference{Namespace: cr.Namespace, Name: name, UID: name},
				Measurement: campaign.ResultMeasurement{WorkloadMode: mode, Batch: 1},
				Evidence: campaign.StoredObjectReference{
					Store:         id.Store,
					Key:           id.Key,
					Size:          id.Size,
					SchemaVersion: execution.SchemaVersion,
				},
			}
			refs = append(refs, ref)
			native = append(
				native,
				&api.ProfilingRun{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: cr.Namespace,
						Name:      name,
						UID:       types.UID(name),
						OwnerReferences: []metav1.OwnerReference{
							*metav1.NewControllerRef(cr, api.GroupVersion.WithKind("BatchEnergyOptimization")),
						},
					},
					Spec: api.ProfilingRunSpec{
						MeasurementStateSpec: cr.Spec.State,
						Measurement:          measurement,
						MetricObservation:    metricProtocol,
					},
					Status: api.ProfilingRunStatus{
						Job: &api.ProfilingRunObjectReference{
							Name: execution.Terminal.Job.Name,
							UID:  execution.Terminal.Job.UID,
						},
						ObservedExecution: &api.ProfilingRunObservedExecutionStatus{
							Pod: api.ProfilingRunObjectReference{
								Name: execution.Terminal.Pod.Name,
								UID:  execution.Terminal.Pod.UID,
							},
							Node: api.ProfilingRunObjectReference{
								Name: execution.Terminal.Node.Name,
								UID:  execution.Terminal.Node.UID,
							},
						},
						Evidence: &api.ProfilingRunEvidenceReference{
							Store:         id.Store,
							Key:           id.Key,
							Size:          id.Size,
							SchemaVersion: execution.SchemaVersion,
						},
						EvidenceStore: &api.ProfilingRunEvidenceStoreBinding{
							Store: "evidence", Endpoint: "http://store:9000", Bucket: "evidence",
							Region: "test", CredentialsSecretName: "credentials",
						},
						Conditions: []metav1.Condition{
							{
								Type:   api.ProfilingRunConditionExecutionComplete,
								Status: metav1.ConditionTrue,
							},
							{
								Type:   api.ProfilingRunConditionEvidenceVerified,
								Status: metav1.ConditionTrue,
							},
						},
					},
				},
			)
		}
	}
	native = append(
		native,
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: cr.Namespace, Name: "credentials"},
			Data: map[string][]byte{
				component.EvidenceStoreAccessKeyKey: []byte("access"),
				component.EvidenceStoreSecretKeyKey: []byte("secret"),
			},
		},
	)
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(native...).Build()
	loader, err := evidencekube.NewRetainedLoader(
		reader,
		func(storekube.Binding, storekube.Credentials) (evidencestore.LocationReader, error) {
			return objects, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := campaign.NewResultRecord(
		campaign.ResultRecordSpec{
			Outcome: "MinimumFound/LocalMinimum",
			MeasurementState: campaign.ResultMeasurementState{
				TargetNode:    cr.Spec.State.TargetNode,
				ModelSpec:     cr.Spec.State.Candidate.ModelSpec,
				DeviceClass:   cr.Spec.State.Candidate.DeviceClass,
				ExecutionPath: cr.Spec.State.Candidate.ExecutionPath,
				PowerMode:     cr.Spec.State.Candidate.PowerMode,
			},
			CurvePlan: campaign.StoredObjectReference{
				Store:         "evidence",
				Key:           "plan",
				Size:          int64(len(planBytes)),
				SchemaVersion: plan.SchemaVersion(),
			},
			CompletedTrials: refs,
			ExcludedTrials: []campaign.ExcludedTrialReference{
				{
					Run: campaign.RunReference{
						Namespace: cr.Namespace,
						Name:      "excluded",
						UID:       "excluded",
					},
					Measurement: campaign.ResultMeasurement{
						WorkloadMode: campaign.WorkloadModeActive,
						Batch:        1,
					},
					Attempt: 2,
					Reasons: []string{"Failed"},
				},
			},
			Detail: json.RawMessage(`{"bestBatch":1}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	cr.Status.Result = &api.ProfilingRunEvidenceReference{
		Store:         "evidence",
		Key:           "result",
		Size:          1,
		SchemaVersion: record.SchemaVersion(),
	}
	return cr, record, objects, reader, loader
}

func statisticsTrial(
	t *testing.T,
	config runconfig.RunConfig,
	name string,
	idle bool,
	index int,
) (evidence.ExecutionEvidence, []byte) {
	t.Helper()
	start := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Second)
	cpu := runconfig.RuntimeCPU{
		CgroupCPUMax:      "max 100000",
		ORTIntraOpThreads: 0,
		ORTInterOpThreads: 0,
		AffinityCPUCount:  4,
		AffinityCPUs:      []int{0, 1, 2, 3},
		CPUSetEffective:   "0-3",
	}
	provider := "CPUExecutionProvider"
	ff := &runconfig.FirstfloorRuntimeEvidence{
		Mode:               "profiling",
		ModelInstanceCount: 1,
		MaxBatchSize:       int(config.State.Firstfloor.MaxBatchSize),
		MaxQueueSize:       int(config.State.Firstfloor.MaxQueueSize),
		DeadlinePolicy: modelcompat.EffectiveDeadlinePolicy(
			config.State.Firstfloor.DeadlinePolicy,
		),
		BatchExecutionCounts: map[string]uint64{},
		Runtime: firstfloor.RuntimeMetadata{
			Runtime:            config.State.Backend,
			RuntimeVersion:     "1.16.0",
			ModelFiles:         config.State.ModelFiles(),
			Inputs:             config.State.Preprocessor.Inputs,
			Outputs:            config.State.Preprocessor.Outputs,
			Providers:          []string{provider},
			AvailableProviders: []string{provider},
			Device:             "cpu",
			CPUAffinity:        cpu.AffinityCPUs,
			IntraOpThreads:     cpu.ORTIntraOpThreads,
			InterOpThreads:     cpu.ORTInterOpThreads,
			InitializedBatches: []int{1},
			CgroupCPUMax:       cpu.CgroupCPUMax,
			CPUSet:             cpu.CPUSetEffective,
		},
	}
	metrics := metricobserver.Result{
		Source: metricobserver.SourceIdentity{
			Kind:            config.Metrics.SourceKind,
			NodeName:        config.Metrics.NodeName,
			Endpoint:        config.Metrics.Endpoint,
			Metric:          config.Metrics.PowerMetric,
			MetricLabels:    config.Metrics.MetricLabels,
			ThermalEndpoint: config.Metrics.ThermalEndpoint,
			ThermalMetrics: &metricobserver.ThermalMetricIdentity{
				ObservationAvailable:      config.Metrics.ThermalMetrics.ObservationAvailable,
				Active:                    config.Metrics.ThermalMetrics.Active,
				CPUCoreThrottleEvents:     config.Metrics.ThermalMetrics.CPUCoreThrottleEvents,
				CPUPackageThrottleEvents:  config.Metrics.ThermalMetrics.CPUPackageThrottleEvents,
				Latched:                   config.Metrics.ThermalMetrics.Latched,
				MaximumTemperatureCelsius: config.Metrics.ThermalMetrics.MaximumTemperatureCelsius,
			},
		},
		StartedAt: start,
		EndedAt:   end.Add(time.Second),
		Summary: metricobserver.Summary{
			Attempts:                   30,
			SuccessfulSamples:          30,
			ObservationDurationSeconds: 31,
			MaximumSampleGapSeconds:    1,
			SourceTimestampAbsent:      true,
		},
	}
	watts := float64(6 + index)
	if idle {
		watts = float64(4 + index)
	}
	for i := range 30 {
		metrics.Samples = append(
			metrics.Samples,
			metricobserver.Sample{
				ObservedAt: start.Add(time.Duration(i)*time.Second + 500*time.Millisecond),
				Watts:      watts,
				Thermal: &metricobserver.ThermalReading{
					ObservationAvailable: ptr.To(true),
					Active:               ptr.To(false),
				},
			},
		)
	}
	pod := workloadobserver.Pod{
		Namespace: "chill-system",
		Name:      name,
		UID:       name,
		Phase:     "Running",
		Images:    []string{config.Images.Runtime},
	}
	workload := &workloadobserver.Result{
		SchemaVersion: workloadobserver.ResultSchemaVersion,
		Target: workloadobserver.TargetIdentity{
			NodeName: config.TargetNode.Name,
			NodeUID:  config.TargetNode.UID,
		},
		Run: workloadobserver.RunIdentity{
			Namespace: pod.Namespace,
			JobUID:    "job-" + name,
			PodName:   name,
			PodUID:    name,
		},
		CoverageStartedAt:      start.Add(-time.Second),
		CoverageEndedAt:        end.Add(time.Second),
		InitialResourceVersion: "10",
		LastResourceVersion:    "11",
		EventLimit:             10,
		InitialActivePods:      []workloadobserver.Pod{pod},
		FinalActivePods:        []workloadobserver.Pod{pod},
		Continuous:             true,
	}
	derived := runconfig.DerivedContract{
		Backend:      config.State.Backend,
		Provider:     provider,
		NodeSnapshot: config.TargetNode,
	}
	var raw []byte
	var err error
	if idle {
		raw, err = (evidence.IdleRawMeasurement{
			SchemaVersion: evidence.IdleRawMeasurementSchemaVersion, RunConfig: config, PodUID: name,
			Metrics: metrics, Workload: workload,
			Runtime: runconfig.IdleRuntimeResult{
				SchemaVersion: runconfig.IdleRuntimeResultSchemaVersion, Status: "Succeeded",
				PodUID: name, NodeName: config.TargetNode.Name,
				Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
				Backend: config.State.Backend, Provider: provider, AvailableProviders: []string{provider},
				Inputs:               config.State.Preprocessor.Inputs,
				ModelLoadCompletedAt: start.Add(-61 * time.Second).Format(time.RFC3339Nano),
				SettlingStartedAt:    start.Add(-60 * time.Second).Format(time.RFC3339Nano),
				SettlingCompletedAt:  start.Format(time.RFC3339Nano), MeasurementStartedAt: start.Format(time.RFC3339Nano),
				MeasurementEndedAt: end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30, ModelResidentAtEnd: true,
				CPUContract: cpu, DerivedContract: derived, Firstfloor: ff,
				MemoryBytes: 123456, MemorySource: "cgroup-v2-memory.peak:runtime-container-lifetime",
			},
		}).CanonicalJSON()
	} else {
		latencies := []float64{100, 100}
		if index == 1 {
			latencies = make([]float64, 40)
			for i := range latencies {
				latencies[i] = 1
			}
		}
		ff.ExecutionCount = uint64(len(latencies))
		ff.InferenceCount = ff.ExecutionCount
		ff.BatchExecutionCounts["1"] = ff.ExecutionCount
		ff.Runtime.WarmupIterations = config.Measurement.WarmupIterations
		var samples []runconfig.NativeServiceSample
		for i := range latencies {
			samples = append(samples, runconfig.NativeServiceSample{
				BatchID: uint64(i + 1), BatchSize: 1, RecordIDs: []string{"record"}, Units: []int{1},
				RunnerServiceMilliseconds: .7, PreprocessMilliseconds: .1, PostprocessMilliseconds: .1,
				BackendCallMilliseconds: .4, NativeRequestMilliseconds: .9,
			})
		}
		raw, err = (evidence.RawMeasurement{
			SchemaVersion: evidence.RawMeasurementSchemaVersion, RunConfig: config, PodUID: name,
			Metrics: metrics, Workload: workload,
			Runtime: runconfig.RuntimeResult{
				SchemaVersion: runconfig.RuntimeResultSchemaVersion, Status: "Succeeded",
				ExperimentID: name, NodeName: config.TargetNode.Name,
				Model: config.State.Model, Architecture: "x86_64", Runtime: config.State.RuntimeFamily, RuntimeVersion: "1.16.0",
				Backend: config.State.Backend, Provider: provider, AvailableProviders: []string{provider},
				Inputs:    config.State.Preprocessor.Inputs,
				BatchSize: 1, WarmupIterations: config.Measurement.WarmupIterations,
				WarmupCompletedAt: start.Add(-time.Second).Format(time.RFC3339Nano),
				InferenceCount:    len(latencies), MeasurementMode: "duration",
				MeasurementStartedAt: start.Format(time.RFC3339Nano),
				MeasurementEndedAt:   end.Format(time.RFC3339Nano), MeasurementElapsedSeconds: 30,
				TargetDurationSeconds: ptr.To(float64(30)),
				CPUContract:           cpu, DerivedContract: derived, Firstfloor: ff,
				MemoryBytes: 123456, MemorySource: "cgroup-v2-memory.peak:runtime-container-lifetime",
				RepetitionResults: []runconfig.Repetition{{Repetition: 1, MeasurementStartedAt: start.Format(time.RFC3339Nano),
					MeasurementEndedAt: end.Format(time.RFC3339Nano), LatencyMilliseconds: latencies, NativeSamples: samples}},
				Latency: map[string]float64{"p95": latencies[0], "mean": latencies[0]},
			},
		}).CanonicalJSON()
	}
	if err != nil {
		t.Fatal(err)
	}
	containers := make([]evidence.ContainerTerminal, 0, 6)
	images := map[string]string{
		"runtime":           config.Images.Runtime,
		"artifact":          config.Images.Artifact,
		"preprocessor":      config.State.Preprocessor.Image,
		"metric-observer":   config.Images.MetricObserver,
		"workload-observer": config.Images.WorkloadObserver,
	}
	if config.Measurement.Dataset != nil {
		images["dataset"] = config.Measurement.Dataset.Image
	}
	for name, image := range images {
		containers = append(
			containers,
			evidence.ContainerTerminal{
				Name:           name,
				RequestedImage: image,
				ImageID:        image,
				Reason:         "Completed",
				StartedAt:      start.Add(-time.Minute),
				FinishedAt:     end.Add(time.Second),
			},
		)
	}
	execution := evidence.ExecutionEvidence{
		SchemaVersion: evidence.ExecutionEvidenceSchemaVersion,
		RunConfig:     config,
		RawMeasurement: evidencestore.ObjectIdentity{
			Store: "evidence",
			Key:   name + "/raw",
			Size:  int64(len(raw)),
		},
		Terminal: evidence.TerminalFacts{
			Job: evidence.JobTerminalFacts{
				Namespace:      pod.Namespace,
				Name:           "job-" + name,
				UID:            "job-" + name,
				Succeeded:      1,
				CompletionTime: end.Add(2 * time.Second),
			},
			Pod: evidence.PodTerminalFacts{
				Namespace:          pod.Namespace,
				Name:               name,
				UID:                name,
				ControllerOwnerUID: "job-" + name,
				NodeName:           config.TargetNode.Name,
				Phase:              "Succeeded",
			},
			Node: evidence.NodeTerminalFacts{
				Name: config.TargetNode.Name,
				UID:  config.TargetNode.UID,
			},
			Containers: containers,
			NodeWorkload: evidence.NodeWorkload{
				ObservedAt: end.Add(3 * time.Second),
				Pods: []evidence.WorkloadPod{
					{
						Namespace: pod.Namespace,
						Name:      name,
						UID:       name,
						Phase:     "Succeeded",
						Images:    []string{config.Images.Runtime},
					},
				},
			},
		},
		FinalizedAt: end.Add(4 * time.Second),
	}
	return execution, raw
}
