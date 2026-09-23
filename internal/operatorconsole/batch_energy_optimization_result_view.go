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

// ErrBatchEnergyOptimizationResultUnavailable indicates that a campaign has not
// published its terminal result reference.
var ErrBatchEnergyOptimizationResultUnavailable = errors.New("terminal result is not available")

const BatchEnergyOptimizationResultAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchEnergyOptimizationResult is the bounded read model for one finished campaign.
// The stored result names the trials and carries the derived curve; only the
// campaign object names the state those trials were measured in. Joining the
// two is what lets one read answer what a campaign measured and what it found.
type BatchEnergyOptimizationResult struct {
	APIVersion string                                `json:"apiVersion"`
	Kind       string                                `json:"kind"`
	ReadAt     time.Time                             `json:"readAt"`
	Campaign   BatchEnergyOptimizationResultCampaign `json:"campaign"`
	Result     BatchEnergyOptimizationResultRecord   `json:"result"`
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
	StoredResult    evidencestore.ObjectIdentity             `json:"storedResult"`
}

// BatchEnergyOptimizationResultRecord republishes the stored record as it was sealed.
// Detail stays the measurement method's own JSON: the Console does not decide
// what a curve looks like, so a method added later needs no change here.
type BatchEnergyOptimizationResultRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	batchenergyoptimization.ResultRecordSpec
}

func BuildBatchEnergyOptimizationResult(snapshot BatchEnergyOptimizationResultSnapshot) (BatchEnergyOptimizationResult, error) {
	if snapshot.Campaign.Status.Result == nil {
		return BatchEnergyOptimizationResult{}, errors.New("batch-energy-optimization snapshot has no stored result reference")
	}
	schemaVersion := snapshot.Result.SchemaVersion()
	if schemaVersion == "" {
		return BatchEnergyOptimizationResult{}, errors.New("batch-energy-optimization snapshot carries an uninitialized result record")
	}
	campaign := snapshot.Campaign.DeepCopy()
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
			StoredResult: evidencestore.ObjectIdentity{
				Store: campaign.Status.Result.Store,
				Key:   campaign.Status.Result.Key,
				Size:  campaign.Status.Result.Size,
			},
		},
		Result: BatchEnergyOptimizationResultRecord{
			SchemaVersion: schemaVersion, ResultRecordSpec: snapshot.Result.Spec(),
		},
	}, nil
}
