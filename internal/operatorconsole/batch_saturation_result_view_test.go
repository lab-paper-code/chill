package operatorconsole

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchsaturation "github.com/lab-paper-code/chill/internal/profiling/campaign"
)

func TestBuildBatchSaturationResultJoinsDeclaredStateWithTheSealedRecord(t *testing.T) {
	t.Parallel()
	campaign, identity, record := campaignResultFixture(t)
	campaign.Spec = edgev1alpha1.BatchSaturationSpec{
		State: edgev1alpha1.MeasurementStateSpec{
			TargetNode: "jetsono",
			Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
				DeviceClass: "jetson-agx-orin-64g", ExecutionPath: "ort-cpu",
				ModelSpec: "mobilenet-v2-050", PowerMode: "MAXN",
			},
		},
		BatchSizes: []int32{1, 2, 4, 8, 16, 32, 64, 96},
	}
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))

	view, err := BuildBatchSaturationResult(BatchSaturationResultSnapshot{
		ReadAt: readAt, Campaign: *campaign, Result: record,
	})
	if err != nil {
		t.Fatal(err)
	}

	if view.APIVersion != BatchSaturationResultAPIVersion || view.Kind != "BatchSaturationResult" ||
		!view.ReadAt.Equal(readAt.UTC()) || view.ReadAt.Location() != time.UTC {
		t.Fatalf("envelope = %#v", view)
	}
	// The state a campaign measured in lives only on the campaign object; a
	// caller must not need a second read to learn it.
	if view.Campaign.Spec.State.Candidate.PowerMode != "MAXN" ||
		view.Campaign.Spec.State.TargetNode != "jetsono" ||
		len(view.Campaign.Spec.BatchSizes) != 8 {
		t.Fatalf("declared state = %#v", view.Campaign.Spec)
	}
	if view.Campaign.Outcome != campaign.Status.Outcome ||
		view.Campaign.AcceptedTrials != campaign.Status.AcceptedTrials ||
		view.Campaign.StoredResult != identity {
		t.Fatalf("campaign status = %#v", view.Campaign)
	}
	if view.Result.SchemaVersion != record.SchemaVersion() ||
		len(view.Result.AdmittedTrials) != 1 || len(view.Result.ExcludedTrials) != 1 ||
		view.Result.ExcludedTrials[0].Reasons[0] != "PowerWindowCoverage" {
		t.Fatalf("record = %#v", view.Result)
	}
}

func TestBuildBatchSaturationResultPassesDetailThroughUninterpreted(t *testing.T) {
	t.Parallel()
	campaign, _, _ := campaignResultFixture(t)
	// A detail shape the Console has never seen must survive untouched, because
	// a later measurement method may publish one.
	detail := json.RawMessage(`{"method":"not-yet-invented","points":[{"batch":7,"value":1.5}]}`)
	record, err := batchsaturation.NewResultRecord(batchsaturation.ResultRecordSpec{
		Outcome:          "Stopped/NoFurtherImprovement",
		MeasurementState: resultMeasurementState(),
		CurvePlan: batchsaturation.StoredObjectReference{
			Store: "chill-evidence-store", Key: "batch-saturations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 2459,
		},
		Detail: detail,
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := BuildBatchSaturationResult(BatchSaturationResultSnapshot{
		ReadAt: time.Now(), Campaign: *campaign, Result: record,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(view.Result.Detail, detail) {
		t.Fatalf("detail = %s, want %s", view.Result.Detail, detail)
	}

	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"detail":{"method":"not-yet-invented"`)) {
		t.Fatalf("encoded view did not inline detail verbatim: %s", encoded)
	}
	// The record fields are embedded, so they must appear beside schemaVersion
	// rather than nested under a wrapper key.
	if !bytes.Contains(encoded, []byte(`"schemaVersion":"chill.dacs.io/batch-saturation-result.v1alpha3"`)) ||
		bytes.Contains(encoded, []byte(`"ResultRecordSpec"`)) {
		t.Fatalf("record encoding = %s", encoded)
	}
}

func TestBuildBatchSaturationResultRejectsAnIncompleteSnapshot(t *testing.T) {
	t.Parallel()
	campaign, _, record := campaignResultFixture(t)
	tests := map[string]struct {
		snapshot BatchSaturationResultSnapshot
		match    string
	}{
		"no stored reference": {
			snapshot: func() BatchSaturationResultSnapshot {
				without := campaign.DeepCopy()
				without.Status.Result = nil
				return BatchSaturationResultSnapshot{Campaign: *without, Result: record}
			}(),
			match: "no stored result reference",
		},
		"uninitialized record": {
			snapshot: BatchSaturationResultSnapshot{Campaign: *campaign},
			match:    "uninitialized result record",
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildBatchSaturationResult(test.snapshot); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("BuildBatchSaturationResult() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestBuildBatchSaturationResultDoesNotAliasTheCampaign(t *testing.T) {
	t.Parallel()
	campaign, _, record := campaignResultFixture(t)
	view, err := BuildBatchSaturationResult(BatchSaturationResultSnapshot{
		ReadAt: time.Now(), Campaign: *campaign, Result: record,
	})
	if err != nil {
		t.Fatal(err)
	}
	view.Campaign.Spec.State.TargetNode = "aliased-node"
	if campaign.Spec.State.TargetNode == "aliased-node" {
		t.Fatal("view aliases the campaign spec")
	}
}

// campaignResultFixture builds the record through its own constructor so the
// fixture cannot drift from the schema the decoder accepts.
func campaignResultFixture(t *testing.T) (
	*edgev1alpha1.BatchSaturation,
	evidencestore.ObjectIdentity,
	batchsaturation.ResultRecord,
) {
	t.Helper()
	record, err := batchsaturation.NewResultRecord(batchsaturation.ResultRecordSpec{
		Outcome:          "Stopped/NoFurtherImprovement",
		MeasurementState: resultMeasurementState(),
		CurvePlan: batchsaturation.StoredObjectReference{
			Store: "chill-evidence-store", Key: "batch-saturations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha4", Size: 2459,
		},
		AdmittedTrials: []batchsaturation.AdmittedTrialReference{{
			Run: batchsaturation.RunReference{
				Namespace: "chill-system", Name: "campaign-active-b1-a1", UID: "run-uid",
			},
			Measurement: batchsaturation.ResultMeasurement{
				WorkloadMode: batchsaturation.WorkloadModeActive, Batch: 1,
			},
			Evidence: batchsaturation.StoredObjectReference{
				Store: "chill-evidence-store", Key: "profiling-runs/run-uid/execution.json",
				SchemaVersion: "chill.dacs.io/profiling-execution-evidence.v1alpha10", Size: 23188,
			},
		}},
		ExcludedTrials: []batchsaturation.ExcludedTrialReference{{
			Run: batchsaturation.RunReference{
				Namespace: "chill-system", Name: "campaign-active-b1-a2", UID: "excluded-uid",
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
			Namespace: "chill-system", Name: "campaign", UID: "campaign-uid",
		},
		Status: edgev1alpha1.BatchSaturationStatus{
			Phase: "Complete", Outcome: "Stopped/NoFurtherImprovement", AcceptedTrials: 27,
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key, Size: identity.Size,
				SchemaVersion: record.SchemaVersion(),
			},
		},
	}
	return campaign, identity, record
}

func resultMeasurementState() batchsaturation.ResultMeasurementState {
	return batchsaturation.ResultMeasurementState{
		TargetNode: "jetsono", ModelSpec: "mobilenet-v2-050",
		DeviceClass: "jetson-agx-orin-64g", ExecutionPath: "ort-cpu", PowerMode: "MODE_50W",
	}
}
