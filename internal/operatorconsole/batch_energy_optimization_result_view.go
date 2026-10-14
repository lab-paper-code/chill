package operatorconsole

import (
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BatchEnergyOptimizationResultSnapshot joins one BatchEnergyOptimization control-plane record
// with the immutable terminal result it references. It is a read-time
// projection, not a second persisted copy of either source.
type BatchEnergyOptimizationResultSnapshot struct {
	ReadAt   time.Time
	Campaign edgev1alpha1.BatchEnergyOptimization
	Result   batchenergyoptimization.ResultRecord
}

const BatchEnergyOptimizationResultAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchEnergyOptimizationResult exposes actual campaign progress and failure,
// with the sealed result only after the campaign publishes its reference.
type BatchEnergyOptimizationResult struct {
	APIVersion string                                `json:"apiVersion"`
	Kind       string                                `json:"kind"`
	ReadAt     time.Time                             `json:"readAt"`
	Campaign   BatchEnergyOptimizationResultCampaign `json:"campaign"`
	Result     *BatchEnergyOptimizationResultRecord  `json:"result"`
}

type BatchEnergyOptimizationResultCampaign struct {
	Namespace       string                                   `json:"namespace"`
	Name            string                                   `json:"name"`
	UID             string                                   `json:"uid"`
	ResourceVersion string                                   `json:"resourceVersion"`
	Generation      int64                                    `json:"generation"`
	Spec            edgev1alpha1.BatchEnergyOptimizationSpec `json:"spec"`
	Phase           string                                   `json:"phase,omitempty"`
	Outcome         string                                   `json:"outcome,omitempty"`
	CompletedTrials int32                                    `json:"completedTrials"`
	Conditions      []metav1.Condition                       `json:"conditions,omitempty"`
	StoredResult    *evidencestore.ObjectIdentity            `json:"storedResult"`
}

// BatchEnergyOptimizationResultRecord republishes the stored record as it was sealed.
// Detail stays the measurement method's own JSON: the Console does not decide
// what a curve looks like, so a method added later needs no change here.
type BatchEnergyOptimizationResultRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	batchenergyoptimization.ResultRecordSpec
}

func BuildBatchEnergyOptimizationResult(snapshot BatchEnergyOptimizationResultSnapshot) (BatchEnergyOptimizationResult, error) {
	if snapshot.Campaign.Status.Result == nil && snapshot.Result.SchemaVersion() != "" {
		return BatchEnergyOptimizationResult{}, errors.New("batch-energy-optimization snapshot has no stored result reference")
	}
	var storedResult *evidencestore.ObjectIdentity
	var result *BatchEnergyOptimizationResultRecord
	campaign := snapshot.Campaign.DeepCopy()
	if campaign.Status.Result != nil {
		schemaVersion := snapshot.Result.SchemaVersion()
		if schemaVersion == "" {
			return BatchEnergyOptimizationResult{}, errors.New("batch-energy-optimization snapshot carries an uninitialized result record")
		}
		storedResult = &evidencestore.ObjectIdentity{
			Store: campaign.Status.Result.Store, Key: campaign.Status.Result.Key, Size: campaign.Status.Result.Size,
		}
		result = &BatchEnergyOptimizationResultRecord{
			SchemaVersion: schemaVersion, ResultRecordSpec: snapshot.Result.Spec(),
		}
	}
	return BatchEnergyOptimizationResult{
		APIVersion: BatchEnergyOptimizationResultAPIVersion,
		Kind:       "BatchEnergyOptimizationResult",
		ReadAt:     snapshot.ReadAt.UTC(),
		Campaign: BatchEnergyOptimizationResultCampaign{
			Namespace: campaign.Namespace, Name: campaign.Name, UID: string(campaign.UID),
			ResourceVersion: campaign.ResourceVersion, Generation: campaign.Generation,
			Spec:  campaign.Spec,
			Phase: campaign.Status.Phase, Outcome: campaign.Status.Outcome,
			CompletedTrials: campaign.Status.CompletedTrials,
			Conditions:      append([]metav1.Condition(nil), campaign.Status.Conditions...),
			StoredResult:    storedResult,
		},
		Result: result,
	}, nil
}
