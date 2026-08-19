package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// consoleTestNamespace is the namespace these Console tests address.
const consoleTestNamespace = "chill-system"

func TestBatchSaturationResultQueryFollowsCampaignReferenceAndPreservesSources(t *testing.T) {
	t.Parallel()
	campaign, identity, record, payload := campaignResultFixture(t)
	objects := &recordingObjectReader{payload: payload}
	query, err := NewKubernetesBatchSaturationResultQuery(
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

func TestBatchSaturationResultQueryRejectsUnusableResultReference(t *testing.T) {
	t.Parallel()
	validCampaign, _, _, _ := campaignResultFixture(t)
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.BatchSaturation)
		match  string
		is     error
	}{
		{
			name: "missing result reference",
			mutate: func(campaign *edgev1alpha1.BatchSaturation) {
				campaign.Status.Result = nil
			},
			match: "terminal result is not available",
			is:    operatorconsole.ErrBatchSaturationResultUnavailable,
		},
		{
			name: "empty key",
			mutate: func(campaign *edgev1alpha1.BatchSaturation) {
				campaign.Status.Result.Key = ""
			},
			match: "key is required",
			is:    evidencestore.ErrInvalidIdentity,
		},
		{
			name: "non-positive size",
			mutate: func(campaign *edgev1alpha1.BatchSaturation) {
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
			query, err := NewKubernetesBatchSaturationResultQuery(
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

func TestBatchSaturationResultQueryReportsAMissingCampaignAsNotFound(t *testing.T) {
	t.Parallel()
	objects := &recordingObjectReader{}
	query, err := NewKubernetesBatchSaturationResultQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build(),
		objects,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.Read(context.Background(), consoleTestNamespace, "absent")
	if !apierrors.IsNotFound(err) {
		t.Fatalf("Read() error = %v, want a NotFound the HTTP layer can classify", err)
	}
	if objects.calls != 0 {
		t.Fatalf("missing campaign reached object reader: calls = %d", objects.calls)
	}
}

func TestBatchSaturationResultQueryPreservesObjectReadFailure(t *testing.T) {
	t.Parallel()
	campaign, _, _, _ := campaignResultFixture(t)
	want := errors.New("object unavailable")
	query, err := NewKubernetesBatchSaturationResultQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(campaign).Build(),
		&recordingObjectReader{err: want},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.Read(context.Background(), campaign.Namespace, campaign.Name)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "read BatchSaturation") {
		t.Fatalf("Read() error = %v", err)
	}
}

func TestBatchSaturationResultQueryRejectsContentTheReferenceDoesNotDescribe(t *testing.T) {
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
				payload, []byte("batch-saturation-result.v1alpha2"), []byte("batch-saturation-result.v9alpha9"), 1,
			),
			match: "unsupported batch-saturation result schema",
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			query, err := NewKubernetesBatchSaturationResultQuery(
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

func TestNewBatchSaturationResultQueryValidatesDependenciesAndIdentity(t *testing.T) {
	t.Parallel()
	objects := &recordingObjectReader{}
	reader := fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build()
	if _, err := NewKubernetesBatchSaturationResultQuery(nil, objects); err == nil {
		t.Fatal("nil Kubernetes reader was accepted")
	}
	if _, err := NewKubernetesBatchSaturationResultQuery(reader, nil); err == nil {
		t.Fatal("nil object reader was accepted")
	}
	query, err := NewKubernetesBatchSaturationResultQuery(reader, objects)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Read(context.Background(), "", "campaign"); err == nil {
		t.Fatal("blank namespace was accepted")
	}
	if _, err := query.Read(context.Background(), " ", "campaign"); err == nil {
		t.Fatal("invalid namespace was accepted")
	}
	if _, err := query.Read(context.Background(), consoleTestNamespace, ""); err == nil {
		t.Fatal("blank name was accepted")
	}
	if _, err := query.Read(context.Background(), consoleTestNamespace, "campaign "); err == nil {
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
	*edgev1alpha1.BatchSaturation,
	evidencestore.ObjectIdentity,
	batchsaturation.ResultRecord,
	[]byte,
) {
	t.Helper()
	record, err := batchsaturation.NewResultRecord(batchsaturation.ResultRecordSpec{
		Outcome: "Stopped/NoFurtherImprovement",
		CurvePlan: batchsaturation.StoredObjectReference{
			Store: "chill-evidence-store", Key: "batch-saturations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 2459,
		},
		AdmittedTrials: []batchsaturation.AdmittedTrialReference{{
			Run: batchsaturation.RunReference{
				Namespace: consoleTestNamespace, Name: "campaign-active-b1-a1", UID: "run-uid",
			},
			Measurement: batchsaturation.ResultMeasurement{
				WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 1,
			},
			Evidence: batchsaturation.StoredObjectReference{
				Store: "chill-evidence-store", Key: "profiling-runs/run-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha8", Size: 23188,
			},
		}},
		ExcludedTrials: []batchsaturation.ExcludedTrialReference{{
			Run: batchsaturation.RunReference{
				Namespace: consoleTestNamespace, Name: "campaign-active-b1-a2", UID: "excluded-uid",
			},
			Measurement: batchsaturation.ResultMeasurement{
				WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 1,
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
		Store: "chill-evidence-store", Key: "batch-saturations/campaign/result.json",
		Size: int64(len(payload)),
	}
	campaign := &edgev1alpha1.BatchSaturation{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: consoleTestNamespace, Name: "campaign", UID: "campaign-uid",
		},
		Status: edgev1alpha1.BatchSaturationStatus{
			Phase: "Complete", Outcome: "Stopped/NoFurtherImprovement", AcceptedTrials: 27,
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key, Size: identity.Size,
				SchemaVersion: record.SchemaVersion(),
			},
		},
	}
	return campaign, identity, record, payload
}
