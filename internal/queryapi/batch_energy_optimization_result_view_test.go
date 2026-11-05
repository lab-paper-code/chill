package queryapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
)

func TestBuildBatchEnergyOptimizationResultJoinsDeclaredStateWithTheSealedRecord(t *testing.T) {
	t.Parallel()
	campaign, identity, record := campaignResultFixture(t)
	campaign.Spec = edgev1alpha1.BatchEnergyOptimizationSpec{
		State: edgev1alpha1.MeasurementStateSpec{
			TargetNode: "jetsono",
			Candidate: edgev1alpha1.ProfilingRunCandidateSpec{
				DeviceClass: "jetson-agx-orin-64g", ExecutionPath: "ort-cpu",
				ModelSpec: "mobilenet-v2-050", PowerMode: "MAXN",
			},
		},
	}
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))

	view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{
		ReadAt: readAt, Campaign: *campaign, Result: record,
	})
	if err != nil {
		t.Fatal(err)
	}

	if view.APIVersion != BatchEnergyOptimizationResultAPIVersion || view.Kind != "BatchEnergyOptimizationResult" ||
		!view.ReadAt.Equal(readAt.UTC()) || view.ReadAt.Location() != time.UTC {
		t.Fatalf("envelope = %#v", view)
	}
	// The state a campaign measured in lives only on the campaign object; a
	// caller must not need a second read to learn it.
	if view.Campaign.Spec.State.Candidate.PowerMode != "MAXN" ||
		view.Campaign.Spec.State.TargetNode != "jetsono" {
		t.Fatalf("declared state = %#v", view.Campaign.Spec)
	}
	if view.Campaign.Outcome != campaign.Status.Outcome ||
		view.Campaign.CompletedTrials != campaign.Status.CompletedTrials ||
		view.Campaign.StoredResult == nil || *view.Campaign.StoredResult != identity {
		t.Fatalf("campaign status = %#v", view.Campaign)
	}
	if view.Result.SchemaVersion != record.SchemaVersion() ||
		len(view.Result.CompletedTrials) != 1 || len(view.Result.ExcludedTrials) != 1 ||
		view.Result.ExcludedTrials[0].Reasons[0] != "PowerWindowCoverage" {
		t.Fatalf("record = %#v", view.Result)
	}
}

func TestBuildBatchEnergyOptimizationResultRejectsAnIncompleteSnapshot(t *testing.T) {
	t.Parallel()
	campaign, _, record := campaignResultFixture(t)
	tests := map[string]struct {
		snapshot BatchEnergyOptimizationResultSnapshot
		match    string
	}{
		"no stored reference": {
			snapshot: func() BatchEnergyOptimizationResultSnapshot {
				without := campaign.DeepCopy()
				without.Status.Result = nil
				return BatchEnergyOptimizationResultSnapshot{Campaign: *without, Result: record}
			}(),
			match: "no stored result reference",
		},
		"uninitialized record": {
			snapshot: BatchEnergyOptimizationResultSnapshot{Campaign: *campaign},
			match:    "uninitialized result record",
		},
	}
	for name, test := range tests {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := BuildBatchEnergyOptimizationResult(test.snapshot); err == nil ||
				!strings.Contains(err.Error(), test.match) {
				t.Fatalf("BuildBatchEnergyOptimizationResult() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestBuildBatchEnergyOptimizationResultDoesNotAliasTheCampaign(t *testing.T) {
	t.Parallel()
	campaign, _, record := campaignResultFixture(t)
	view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{
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

func TestBuildBatchEnergyOptimizationResultEmptyDiscoveryIsNotAccepted(t *testing.T) {
	for activeTrials := range 3 {
		t.Run(fmt.Sprintf("active-trials-%d", activeTrials), func(t *testing.T) {
			campaign, _, original := campaignResultFixture(t)
			spec := original.Spec()
			spec.Outcome = batchenergyoptimization.BatchDiscoveryComplete
			completed := spec.CompletedTrials[0]
			spec.CompletedTrials = nil
			for _, mode := range []batchenergyoptimization.WorkloadMode{
				batchenergyoptimization.WorkloadModeIdle, batchenergyoptimization.WorkloadModeActive,
			} {
				count := 3
				if mode == batchenergyoptimization.WorkloadModeActive {
					count = activeTrials
				}
				for attempt := 1; attempt <= count; attempt++ {
					trial := completed
					trial.Measurement.WorkloadMode = mode
					trial.Run.Name = fmt.Sprintf("campaign-%s-b1-a%d", mode, attempt)
					trial.Run.UID = trial.Run.Name + "-uid"
					trial.Evidence.Key = "profiling-runs/" + trial.Run.UID + "/execution.json"
					spec.CompletedTrials = append(spec.CompletedTrials, trial)
				}
			}
			failed := completed.Run
			failed.Name = fmt.Sprintf("campaign-active-b1-a%d", activeTrials+1)
			failed.UID = failed.Name + "-uid"
			spec.ExcludedTrials = []batchenergyoptimization.ExcludedTrialReference{{
				Run: failed, Measurement: completed.Measurement, Attempt: activeTrials + 1,
				Reasons: []string{batchenergyoptimization.DiscoveryStopOOM},
			}}
			var err error
			spec.Detail, err = json.Marshal(batchenergyoptimization.DiscoveryResult{
				MeasuredBatchSizes: []int{},
				Stop: batchenergyoptimization.DiscoveryStop{
					Reason: batchenergyoptimization.DiscoveryStopOOM, Run: &failed, Attempt: activeTrials + 1,
					FailedMeasurement: &batchenergyoptimization.Measurement{
						WorkloadMode: batchenergyoptimization.WorkloadModeActive, Batch: 1,
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			record, err := batchenergyoptimization.NewResultRecord(spec)
			if err != nil {
				t.Fatal(err)
			}
			discovery, sequential, err := batchenergyoptimization.DecodeDiscoveryResult(record, []int{1, 2})
			if err != nil || !sequential || len(discovery.MeasuredBatchSizes) != 0 {
				t.Fatalf("invalid empty-prefix boundary: %+v sequential=%v err=%v", discovery, sequential, err)
			}
			campaign.Generation, campaign.Status.ObservedGeneration = 1, 1
			campaign.Status.Outcome = string(spec.Outcome)
			missing := "StatisticsNotRecorded"
			view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{
				Campaign: *campaign, Result: record, StatisticsReason: &missing,
			})
			if err != nil {
				t.Fatal(err)
			}
			if view.Assessment.Evidence.Accepted || !view.Assessment.Evidence.Available ||
				view.Assessment.DisplayStatus == MeasurementUsable || view.Assessment.DisplayStatus == MeasurementProblem ||
				len(view.ResultRows) != 0 || len(view.Assessment.FailureSummary) != 0 {
				t.Fatalf("empty prefix became usable or failed: %+v", view)
			}
			if view.Campaign.Phase != string(edgev1alpha1.MeasurementPhaseComplete) ||
				view.Campaign.StoredResult == nil || view.Result == nil ||
				string(view.Result.Detail) != string(spec.Detail) || len(view.Result.CompletedTrials) != 3+activeTrials {
				t.Fatalf("sealed expected stop was lost: %+v", view)
			}
			if len(view.Assessment.Reasons) != 1 || view.Assessment.Reasons[0].Code != missing {
				t.Fatalf("absence reason was lost: %+v", view.Assessment)
			}
		})
	}
}

func TestBuildBatchEnergyOptimizationResultPreservesPublishedLegacyAcceptance(t *testing.T) {
	for _, reason := range []string{"StatisticsNotRecorded", "RetainedEvidenceMissing"} {
		t.Run(reason, func(t *testing.T) {
			campaign, _, record := campaignResultFixture(t)
			view, err := BuildBatchEnergyOptimizationResult(BatchEnergyOptimizationResultSnapshot{
				Campaign: *campaign, Result: record, StatisticsReason: &reason,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !view.Assessment.Evidence.Accepted || view.Assessment.DisplayStatus != MeasurementUsable {
				t.Fatalf("published legacy acceptance changed: %+v", view.Assessment)
			}
		})
	}
}

// campaignResultFixture builds the record through its own constructor so the
// fixture cannot drift from the schema the decoder accepts.
func campaignResultFixture(t *testing.T) (
	*edgev1alpha1.BatchEnergyOptimization,
	evidencestore.ObjectIdentity,
	batchenergyoptimization.ResultRecord,
) {
	t.Helper()
	record, err := batchenergyoptimization.NewResultRecord(batchenergyoptimization.ResultRecordSpec{
		Outcome:          "MinimumFound/LocalMinimum",
		MeasurementState: resultMeasurementState(),
		CurvePlan: batchenergyoptimization.StoredObjectReference{
			Store: "chill-evidence-store", Key: "batch-energy-optimizations/campaign/curve-plan.json",
			SchemaVersion: "chill.dacs.io/curve-plan.v1alpha7", Size: 2459,
		},
		CompletedTrials: []batchenergyoptimization.CompletedTrialReference{{
			Run: batchenergyoptimization.RunReference{
				Namespace: "chill-system", Name: "campaign-active-b1-a1", UID: "run-uid",
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
				Namespace: "chill-system", Name: "campaign-active-b1-a2", UID: "excluded-uid",
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
			Namespace: "chill-system", Name: "campaign", UID: "campaign-uid",
		},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{
			Phase: "Complete", Outcome: "MinimumFound/LocalMinimum", CompletedTrials: 27,
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key, Size: identity.Size,
				SchemaVersion: record.SchemaVersion(),
			},
		},
	}
	return campaign, identity, record
}

func resultMeasurementState() batchenergyoptimization.ResultMeasurementState {
	return batchenergyoptimization.ResultMeasurementState{
		TargetNode: "jetsono", ModelSpec: "mobilenet-v2-050",
		DeviceClass: "jetson-agx-orin-64g", ExecutionPath: "ort-cpu", PowerMode: "MODE_50W",
	}
}
