package kube

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/queryapi"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// queryTestNamespace is the namespace these Query API tests address.
const queryTestNamespace = "chill-system"

func TestBatchEnergyOptimizationResultQueryFollowsCampaignReferenceAndPreservesSources(t *testing.T) {
	t.Parallel()
	campaign, identity, payload := campaignResultFixture(t)
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
		got.Session == nil || got.Session.Stop == nil {
		t.Fatalf("snapshot = %#v", got)
	}

	got.Campaign.Status.Outcome = "mutated"
	if campaign.Status.Outcome == "mutated" {
		t.Fatal("query returned a mutable alias of the Kubernetes object")
	}
}

func TestCampaignWithoutResultStillExposesFailureAndDoesNotReadStore(t *testing.T) {
	campaign, _, _ := campaignResultFixture(t)
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
	if objects.calls != 0 || view.Campaign.StoredResult != nil ||
		view.Campaign.Phase != "Failed" || len(view.Campaign.Conditions) != 1 ||
		view.Campaign.Conditions[0].Reason != "CurvePlanResolutionFailed" {
		t.Fatalf("missing result hid the actual campaign failure: %#v", view)
	}
}

func TestBatchEnergyOptimizationResultQueryRejectsUnusableResultReference(t *testing.T) {
	t.Parallel()
	validCampaign, _, _ := campaignResultFixture(t)
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
	campaign, _, _ := campaignResultFixture(t)
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
	campaign, _, payload := campaignResultFixture(t)
	tests := map[string]struct {
		payload []byte
		match   string
	}{
		"short read":       {payload: payload[:len(payload)-1], match: "where the reference declares"},
		"trailing content": {payload: append(bytes.Clone(payload), ' '), match: "where the reference declares"},
		"unsupported schema": {
			payload: bytes.Replace(
				payload, []byte("batch-energy-optimization-result.v1alpha6"), []byte("batch-energy-optimization-result.v9alpha9"), 1,
			),
			match: "unsupported session result schema",
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

const sessionResultPayload = `{
 "schemaVersion": "chill.dacs.io/batch-energy-optimization-result.v1alpha6",
 "condition": {"nodeName": "jetsono", "nodeUID": "node-uid", "deviceClass": "jetson-agx-orin-64g", "powerMode": "MAXN",
  "logicalModel": "mobilenet-v2-050", "runtimeFamily": "onnxruntime", "backend": "cpu", "runtimeImage": "registry/firstfloor:1",
  "artifact": {}, "preprocessor": {}, "actor": {}},
 "settings": {"meterLagSeconds": 3, "minimumPowerWindowSeconds": 15, "midLevelRatio": 0.25, "sparseMeanGapSeconds": 1,
  "levelDurationLimitSeconds": 300, "postLoadWindowSeconds": 60, "minimumSamples": 59, "confidenceLevel": 0.95},
 "power": {"idleWatts": {"status": "Identified", "value": 7.5, "range": [7.4, 7.6]},
  "awakeStepWatts": {"status": "NotIdentified", "range": [0, 1.2], "reason": "the hold has no breakpoint"},
  "holdSeconds": {"status": "Identified", "value": 0.2, "range": [0.1, 0.3]},
  "meterIntervalSeconds": 1, "settledAtSeconds": 4, "postLoad": [[0, 9.1], [1, 8.2], [2, 7.6]],
  "holdSegmentsSearched": 0, "holdResolutionWatts": 0},
 "batches": [{"batch": 1, "serviceNanos": [10, 20, 30], "precedingIdleNanos": [1, 2, 3],
  "dispatchOccupancyNanos": [40, 10, 30, 20], "saturationInputRate": 80,
  "joulesPerInput": {"status": "Identified", "value": 0.05, "range": [0.04, 0.06]},
  "peakMemoryBytes": 1048576, "memoryKind": "host", "prepareCostKey": "prepare-1", "removeCostKey": "remove-1",
  "levels": [{"kind": "saturation", "id": "b1-saturation", "start": "2026-11-14T00:00:00Z", "end": "2026-11-14T00:01:00Z",
   "sentInputs": 59, "completedInputs": 59, "completedBatches": 59, "endReason": "Samples", "inputRate": 80,
   "meanWatts": 12.5, "meanWattsStandardError": 0.1, "powerSamples": 57}]}],
 "stop": {"batch": 2, "class": "CapacityLimit", "reason": "batch 2 exceeds the memory envelope"},
 "meterSource": {"kind": "prometheus"}
}`

// campaignResultFixture is a completed session request with its stored result.
func campaignResultFixture(t *testing.T) (
	*edgev1alpha1.BatchEnergyOptimization,
	evidencestore.ObjectIdentity,
	[]byte,
) {
	t.Helper()
	payload := []byte(sessionResultPayload)
	identity := evidencestore.ObjectIdentity{
		Store: "chill-evidence-store", Key: "batch-energy-optimizations/campaign-uid/result.json",
		Size: int64(len(payload)),
	}
	campaign := &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: queryTestNamespace, Name: "campaign", UID: "campaign-uid",
		},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{Session: &edgev1alpha1.BatchEnergyOptimizationSessionSpec{}},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{
			Phase: "Complete", Outcome: "CapacityLimit",
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key, Size: identity.Size,
			},
		},
	}
	return campaign, identity, payload
}
