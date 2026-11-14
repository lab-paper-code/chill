package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/queryapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// queryTestNamespace is the namespace these Query API tests address.
const queryTestNamespace = "chill-system"

func TestBatchEnergyOptimizationResultQueryFollowsCampaignReferenceAndPreservesSources(t *testing.T) {
	t.Parallel()
	campaign, identity, record, payload := campaignResultFixture(t)
	objects := &recordingObjectReader{payload: payload}
	query, err := NewKubernetesBatchEnergyOptimizationResultQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(campaign).Build(),
		objects,
	)
	if err != nil {
		t.Fatal(err)
	}
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	query.now = func() time.Time { return readAt }

	got, err := query.Read(context.Background(), campaign.Namespace, campaign.Name)
	if err != nil {
		t.Fatal(err)
	}
	if objects.calls != 1 || objects.identity != identity {
		t.Fatalf("object reader calls/identity = %d, %#v", objects.calls, objects.identity)
	}
	if !got.ReadAt.Equal(readAt.UTC()) || got.ReadAt.Location() != time.UTC ||
		got.Campaign.UID != campaign.UID ||
		got.Campaign.Status.Outcome != campaign.Status.Outcome ||
		!reflect.DeepEqual(got.Result.Spec(), record.Spec()) {
		t.Fatalf("snapshot = %#v", got)
	}

	got.Campaign.Status.Outcome = "mutated"
	if campaign.Status.Outcome == "mutated" {
		t.Fatal("query returned a mutable alias of the Kubernetes object")
	}
}

func TestCampaignWithoutResultStillExposesFailureAndDoesNotReadStore(t *testing.T) {
	campaign, _, _, _ := campaignResultFixture(t)
	campaign.Status.Result = nil
	campaign.Status.Phase = "Failed"
	campaign.Status.Conditions = []metav1.Condition{{
		Type: "MeasurementComplete", Status: metav1.ConditionFalse,
		Reason: "CurvePlanResolutionFailed", Message: "declared power mode is unavailable",
	}}
	objects := &recordingObjectReader{err: errors.New("store must not be needed before a result exists")}
	query, err := NewKubernetesBatchEnergyOptimizationResultQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(campaign).Build(), objects,
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := query.Read(context.Background(), campaign.Namespace, campaign.Name)
	if err != nil {
		t.Fatal(err)
	}
	view, err := queryapi.BuildBatchEnergyOptimizationResult(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if objects.calls != 0 || view.Result != nil || view.Campaign.StoredResult != nil ||
		view.Campaign.Phase != "Failed" || len(view.Campaign.Conditions) != 1 ||
		view.Campaign.Conditions[0].Reason != "CurvePlanResolutionFailed" {
		t.Fatalf("missing result hid the actual campaign failure: %#v", view)
	}
}

func TestBatchEnergyOptimizationResultQueryRetainsExpectedStopChildren(t *testing.T) {
	for _, test := range []struct {
		name        string
		phase       string
		discovery   bool
		prefix      int
		wantFailure bool
		allocation  bool
	}{
		{name: "completed discovery", phase: "Complete", discovery: true, prefix: 1, wantFailure: true},
		{name: "completed allocation discovery", phase: "Complete", discovery: true, prefix: 1, wantFailure: true, allocation: true},
		{name: "completed empty discovery", phase: "Complete", discovery: true, wantFailure: true},
		{name: "ordinary failure", phase: "Failed", wantFailure: true},
		{name: "legacy terminal result", phase: "Complete"},
	} {
		t.Run(test.name, func(t *testing.T) {
			campaign, _, record, payload := campaignResultFixture(t)
			campaign.Generation, campaign.Status.ObservedGeneration = 1, 1
			campaign.Status.Phase = test.phase
			controller := true
			run := &edgev1alpha1.ProfilingRun{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: campaign.Namespace, Name: "expected-stop", UID: "expected-stop-uid", Generation: 1,
					OwnerReferences: []metav1.OwnerReference{{
						APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "BatchEnergyOptimization",
						Name: campaign.Name, UID: campaign.UID, Controller: &controller,
					}},
				},
				Status: edgev1alpha1.ProfilingRunStatus{
					ObservedGeneration: 1,
					Conditions: []metav1.Condition{{
						Type: edgev1alpha1.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionFalse,
						Reason: "JobFailed", ObservedGeneration: 1,
					}},
					RuntimeTermination: &edgev1alpha1.ProfilingRunRuntimeTermination{
						Class: "ExpectedStop", Container: "runtime", Stage: "Execution", Reason: "OOMKilled", ExitCode: 137,
					},
				},
			}
			if test.allocation {
				run.Status.RuntimeTermination.Reason = "Error"
				run.Status.RuntimeTermination.ExitCode = 3
				run.Status.RuntimeTermination.FailureReason = "MemoryAllocationFailed"
			}
			if test.discovery {
				spec := record.Spec()
				spec.Outcome = batchenergyoptimization.BatchDiscoveryComplete
				template := spec.CompletedTrials[0]
				spec.CompletedTrials = nil
				measurements := []batchenergyoptimization.Measurement{{WorkloadMode: batchenergyoptimization.WorkloadModeIdle, Batch: 1}}
				prefix := []int{}
				if test.prefix == 1 {
					measurements = append(measurements, batchenergyoptimization.Measurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1})
					prefix = append(prefix, 1)
				}
				for _, measurement := range measurements {
					for attempt := 1; attempt <= 3; attempt++ {
						trial := template
						trial.Measurement = batchenergyoptimization.ResultMeasurement(measurement)
						trial.Run.Name = fmt.Sprintf("campaign-%s-b1-a%d", measurement.WorkloadMode, attempt)
						trial.Run.UID = trial.Run.Name + "-uid"
						trial.Evidence.Key = "profiling-runs/" + trial.Run.UID + "/execution.json"
						spec.CompletedTrials = append(spec.CompletedTrials, trial)
					}
				}
				stop := &spec.ExcludedTrials[0]
				stop.Run = batchenergyoptimization.RunReference{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)}
				stop.Measurement = batchenergyoptimization.ResultMeasurement{WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1 << test.prefix}
				stop.Attempt, stop.Reasons = 1, []string{"OOM"}
				var err error
				spec.Detail, err = json.Marshal(batchenergyoptimization.DiscoveryResult{
					MeasuredBatchSizes: prefix,
					Stop: batchenergyoptimization.DiscoveryStop{
						Reason: batchenergyoptimization.DiscoveryStopOOM, Attempt: stop.Attempt, Run: &stop.Run,
						FailedMeasurement: &batchenergyoptimization.Measurement{WorkloadMode: stop.Measurement.WorkloadMode, Batch: stop.Measurement.Batch},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				record, err = batchenergyoptimization.NewResultRecord(spec)
				if err != nil {
					t.Fatal(err)
				}
				if _, discovery, err := batchenergyoptimization.DecodeDiscoveryResult(record, []int{1, 2}); err != nil || !discovery {
					t.Fatalf("invalid stored discovery: discovery=%t err=%v", discovery, err)
				}
				payload, err = record.CanonicalJSON()
				if err != nil {
					t.Fatal(err)
				}
				campaign.Status.Outcome = batchenergyoptimization.BatchDiscoveryComplete
				campaign.Status.Result.Size = int64(len(payload))
			}
			foreignUID := run.DeepCopy()
			foreignUID.Name, foreignUID.UID = "foreign-uid", "foreign-run-uid"
			foreignUID.OwnerReferences[0].UID = "other-campaign-uid"
			foreignNamespace := run.DeepCopy()
			foreignNamespace.Namespace = "other-namespace"
			query, err := NewKubernetesBatchEnergyOptimizationResultQuery(
				fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(campaign, run, foreignUID, foreignNamespace).Build(),
				&recordingObjectReader{payload: payload},
			)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := query.Read(context.Background(), campaign.Namespace, campaign.Name)
			if err != nil {
				t.Fatal(err)
			}
			view, err := queryapi.BuildBatchEnergyOptimizationResult(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if view.Campaign.Phase != test.phase || view.Campaign.Outcome != campaign.Status.Outcome ||
				view.Result == nil || !reflect.DeepEqual(view.Result.Detail, record.Spec().Detail) {
				t.Fatalf("reader changed terminal result: %+v", view)
			}
			if test.phase == "Complete" && (view.Assessment.Status == queryapi.MeasurementFailed ||
				view.Assessment.Execution == nil || view.Assessment.Execution.Phase != "Complete") {
				t.Fatalf("expected-stop child changed completed parent: %+v", view.Assessment)
			}
			failures := view.Assessment.FailureSummary
			wantCode := "HostOOM"
			if test.allocation {
				wantCode = "MemoryAllocationFailed"
			}
			if !test.wantFailure {
				if len(failures) != 0 {
					t.Fatalf("legacy terminal result gained child failures: %+v", failures)
				}
			} else if len(failures) != 1 || failures[0].Code != wantCode || failures[0].RunID != string(run.UID) {
				t.Fatalf("owned expected-stop cause missing or foreign Run included: %+v", failures)
			}
		})
	}
}

func TestBatchEnergyOptimizationResultQueryRejectsUnusableResultReference(t *testing.T) {
	t.Parallel()
	validCampaign, _, _, _ := campaignResultFixture(t)
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.BatchEnergyOptimization)
		match  string
		is     error
	}{
		{
			name: "empty key",
			mutate: func(campaign *edgev1alpha1.BatchEnergyOptimization) {
				campaign.Status.Result.Key = ""
			},
			match: "key is required",
			is:    evidencestore.ErrInvalidIdentity,
		},
		{
			name: "non-positive size",
			mutate: func(campaign *edgev1alpha1.BatchEnergyOptimization) {
				campaign.Status.Result.Size = 0
			},
			match: "size must be positive",
			is:    evidencestore.ErrInvalidIdentity,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			campaign := validCampaign.DeepCopy()
			test.mutate(campaign)
			objects := &recordingObjectReader{}
			query, err := NewKubernetesBatchEnergyOptimizationResultQuery(
				fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(campaign).Build(),
				objects,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = query.Read(context.Background(), campaign.Namespace, campaign.Name)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Read() error = %v, want %q", err, test.match)
			}
			if !errors.Is(err, test.is) {
				t.Fatalf("Read() error = %v, want error type %v", err, test.is)
			}
			if objects.calls != 0 {
				t.Fatalf("object reader was called %d times for an unusable reference", objects.calls)
			}
		})
	}
}

func TestBatchEnergyOptimizationResultQueryReportsAMissingCampaignAsNotFound(t *testing.T) {
	t.Parallel()
	objects := &recordingObjectReader{}
	query, err := NewKubernetesBatchEnergyOptimizationResultQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build(),
		objects,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.Read(context.Background(), queryTestNamespace, "absent")
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Read() error = %v, want a NotFound the HTTP layer can classify", err)
	}
	if objects.calls != 0 {
		t.Fatalf("missing campaign reached object reader: calls = %d", objects.calls)
	}
}

func TestBatchEnergyOptimizationResultQueryPreservesObjectReadFailure(t *testing.T) {
	t.Parallel()
	campaign, _, _, _ := campaignResultFixture(t)
	want := errors.New("object unavailable")
	query, err := NewKubernetesBatchEnergyOptimizationResultQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(campaign).Build(),
		&recordingObjectReader{err: want},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.Read(context.Background(), campaign.Namespace, campaign.Name)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "read BatchEnergyOptimization") {
		t.Fatalf("Read() error = %v", err)
	}
}

func TestBatchEnergyOptimizationResultQueryRejectsContentTheReferenceDoesNotDescribe(t *testing.T) {
	t.Parallel()
	campaign, _, _, payload := campaignResultFixture(t)
	tests := map[string]struct {
		payload []byte
		match   string
	}{
		"short read":       {payload: payload[:len(payload)-1], match: "where the reference declares"},
		"trailing content": {payload: append(bytes.Clone(payload), ' '), match: "where the reference declares"},
		"unsupported schema": {
			payload: bytes.Replace(
				payload, []byte("batch-energy-optimization-result.v1alpha5"), []byte("batch-energy-optimization-result.v9alpha9"), 1,
			),
			match: "unsupported batch-energy-optimization result schema",
		},
	}
	if bytes.Equal(tests["unsupported schema"].payload, payload) {
		t.Fatal("unsupported-schema fixture did not mutate the result schema")
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			query, err := NewKubernetesBatchEnergyOptimizationResultQuery(
				fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(campaign.DeepCopy()).Build(),
				&recordingObjectReader{payload: test.payload},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = query.Read(context.Background(), campaign.Namespace, campaign.Name)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Read() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestNewBatchEnergyOptimizationResultQueryValidatesDependenciesAndIdentity(t *testing.T) {
	t.Parallel()
	objects := &recordingObjectReader{}
	reader := fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build()
	if _, err := NewKubernetesBatchEnergyOptimizationResultQuery(nil, objects); err == nil {
		t.Fatal("nil Kubernetes reader was accepted")
	}
	if _, err := NewKubernetesBatchEnergyOptimizationResultQuery(reader, nil); err == nil {
		t.Fatal("nil object reader was accepted")
	}
	query, err := NewKubernetesBatchEnergyOptimizationResultQuery(reader, objects)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Read(context.Background(), "", "campaign"); err == nil {
		t.Fatal("blank namespace was accepted")
	}
	if _, err := query.Read(context.Background(), " ", "campaign"); err == nil {
		t.Fatal("invalid namespace was accepted")
	}
	if _, err := query.Read(context.Background(), queryTestNamespace, ""); err == nil {
		t.Fatal("blank name was accepted")
	}
	if _, err := query.Read(context.Background(), queryTestNamespace, "campaign "); err == nil {
		t.Fatal("invalid name was accepted")
	}
}

type recordingObjectReader struct {
	identity evidencestore.ObjectIdentity
	payload  []byte
	err      error
	calls    int
}

func (r *recordingObjectReader) Open(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	r.calls++
	r.identity = identity
	if r.err != nil {
		return nil, r.err
	}
	return io.NopCloser(bytes.NewReader(r.payload)), nil
}

// campaignResultFixture builds the record through its own constructor so the
// fixture cannot drift from the schema the decoder accepts.
func campaignResultFixture(t *testing.T) (
	*edgev1alpha1.BatchEnergyOptimization,
	evidencestore.ObjectIdentity,
	batchenergyoptimization.ResultRecord,
	[]byte,
) {
	t.Helper()
	record, err := batchenergyoptimization.NewResultRecord(batchenergyoptimization.ResultRecordSpec{
		Outcome: "MinimumFound/LocalMinimum",
		MeasurementState: batchenergyoptimization.ResultMeasurementState{
			TargetNode: "jetsono", ModelSpec: "mobilenet-v2-050",
			DeviceClass: "jetson-agx-orin-64g", ExecutionPath: "ort-cpu", PowerMode: "MODE_50W",
		},
		CurvePlan: batchenergyoptimization.StoredObjectReference{
			Store: "chill-evidence-store", Key: "batch-energy-optimizations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha7", Size: 2459,
		},
		CompletedTrials: []batchenergyoptimization.CompletedTrialReference{{
			Run: batchenergyoptimization.RunReference{
				Namespace: queryTestNamespace, Name: "campaign-active-b1-a1", UID: "run-uid",
			},
			Measurement: batchenergyoptimization.ResultMeasurement{
				WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
			},
			Evidence: batchenergyoptimization.StoredObjectReference{
				Store: "chill-evidence-store", Key: "profiling-runs/run-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha14", Size: 23188,
			},
		}},
		ExcludedTrials: []batchenergyoptimization.ExcludedTrialReference{{
			Run: batchenergyoptimization.RunReference{
				Namespace: queryTestNamespace, Name: "campaign-active-b1-a2", UID: "excluded-uid",
			},
			Measurement: batchenergyoptimization.ResultMeasurement{
				WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
			},
			Attempt: 2,
			Reasons: []string{"PowerWindowCoverage"},
		}},
		Detail: json.RawMessage(`{"selectedBatch":1}`),
	})
	if err != nil {
		t.Fatalf("build result record: %v", err)
	}
	payload, err := record.CanonicalJSON()
	if err != nil {
		t.Fatalf("encode result record: %v", err)
	}
	identity := evidencestore.ObjectIdentity{
		Store: "chill-evidence-store", Key: "batch-energy-optimizations/campaign/result.json",
		Size: int64(len(payload)),
	}
	campaign := &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: queryTestNamespace, Name: "campaign", UID: "campaign-uid",
		},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{
			Phase: "Complete", Outcome: "MinimumFound/LocalMinimum", CompletedTrials: 27,
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key, Size: identity.Size,
				SchemaVersion: record.SchemaVersion(),
			},
		},
	}
	return campaign, identity, record, payload
}
