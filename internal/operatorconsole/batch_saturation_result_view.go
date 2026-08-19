package operatorconsole

import (
	"errors"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// BatchSaturationResultSnapshot joins one BatchSaturation control-plane record
// with the immutable terminal result it references. It is a read-time
// projection, not a second persisted copy of either source.
type BatchSaturationResultSnapshot struct {
	ReadAt   time.Time
	Campaign edgev1alpha1.BatchSaturation
	Result   batchsaturation.ResultRecord
}

// ErrBatchSaturationResultUnavailable indicates that a campaign has not
// published its terminal result reference.
var ErrBatchSaturationResultUnavailable = errors.New("terminal result is not available")

const BatchSaturationResultAPIVersion = "console.chill.dacs.io/v1alpha1"

// BatchSaturationResult is the bounded read model for one finished campaign.
// The stored result names the trials and carries the derived curve; only the
// campaign object names the state those trials were measured in. Joining the
// two is what lets one read answer what a campaign measured and what it found.
type BatchSaturationResult struct {
	APIVersion string                        `json:"apiVersion"`
	Kind       string                        `json:"kind"`
	ReadAt     time.Time                     `json:"readAt"`
	Campaign   BatchSaturationResultCampaign `json:"campaign"`
	Result     BatchSaturationResultRecord   `json:"result"`
}

type BatchSaturationResultCampaign struct {
	Namespace       string                           `json:"namespace"`
	Name            string                           `json:"name"`
	UID             string                           `json:"uid"`
	ResourceVersion string                           `json:"resourceVersion"`
	Generation      int64                            `json:"generation"`
	Spec            edgev1alpha1.BatchSaturationSpec `json:"spec"`
	Phase           string                           `json:"phase,omitempty"`
	Outcome         string                           `json:"outcome,omitempty"`
	AcceptedTrials  int32                            `json:"acceptedTrials"`
	Conditions      []metav1.Condition               `json:"conditions,omitempty"`
	StoredResult    evidencestore.ObjectIdentity     `json:"storedResult"`
}

// BatchSaturationResultRecord republishes the stored record as it was sealed.
// Detail stays the measurement method's own JSON: the Console does not decide
// what a curve looks like, so a method added later needs no change here.
type BatchSaturationResultRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	batchsaturation.ResultRecordSpec
}

func BuildBatchSaturationResult(snapshot BatchSaturationResultSnapshot) (BatchSaturationResult, error) {
	if snapshot.Campaign.Status.Result == nil {
		return BatchSaturationResult{}, errors.New("batch-saturation snapshot has no stored result reference")
	}
	schemaVersion := snapshot.Result.SchemaVersion()
	if schemaVersion == "" {
		return BatchSaturationResult{}, errors.New("batch-saturation snapshot carries an uninitialized result record")
	}
	campaign := snapshot.Campaign.DeepCopy()
	return BatchSaturationResult{
		APIVersion: BatchSaturationResultAPIVersion,
		Kind:       "BatchSaturationResult",
		ReadAt:     snapshot.ReadAt.UTC(),
		Campaign: BatchSaturationResultCampaign{
			Namespace: campaign.Namespace, Name: campaign.Name, UID: string(campaign.UID),
			ResourceVersion: campaign.ResourceVersion, Generation: campaign.Generation,
			Spec:  campaign.Spec,
			Phase: campaign.Status.Phase, Outcome: campaign.Status.Outcome,
			AcceptedTrials: campaign.Status.AcceptedTrials,
			Conditions:     append([]metav1.Condition(nil), campaign.Status.Conditions...),
			StoredResult: evidencestore.ObjectIdentity{
				Store: campaign.Status.Result.Store,
				Key:   campaign.Status.Result.Key,
				Size:  campaign.Status.Result.Size,
			},
		},
		Result: BatchSaturationResultRecord{
			SchemaVersion: schemaVersion, ResultRecordSpec: snapshot.Result.Spec(),
		},
	}, nil
}
