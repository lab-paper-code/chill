package kube_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/measurement"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAdmissionPretrialFailureReturnsNodeAndAllowsPublicDelete(t *testing.T) {
	servingkube.ExercisePretrialFailureReturn(t, func(ctx context.Context, native client.Client, kube kubernetes.Interface, objects servingkube.PretrialFailureObjects, root *edge.BatchEnergyOptimization) error {
		service := measurement.New(native, kube, nil, objects, measurement.Config{ServingNamespace: root.Namespace, StoreName: "evidence", MaxEvidenceBytes: 1 << 20}, nil, nil)
		if _, err := service.Delete(ctx, root.Namespace, root.Name, "other-uid", root.ResourceVersion); !apierrors.IsConflict(err) {
			return fmt.Errorf("public DELETE lost UID precondition: %v", err)
		}
		if _, err := service.Delete(ctx, root.Namespace, root.Name, string(root.UID), "stale-version"); !apierrors.IsConflict(err) {
			return fmt.Errorf("public DELETE lost resourceVersion precondition: %v", err)
		}
		result, err := service.Delete(ctx, root.Namespace, root.Name, string(root.UID), root.ResourceVersion)
		if root.Status.Phase == "" {
			var refusal *serving.Error
			if !errors.As(err, &refusal) || refusal.Code != "MeasurementInUse" {
				return fmt.Errorf("nonterminal public DELETE: %v", err)
			}
			return nil
		}
		if err != nil { return err }
		if !result.EvidenceDeleted || result.Task == nil || result.Task.UID != root.UID || len(result.DeletedEvidence) != 3 {
			return fmt.Errorf("public DELETE did not finish source-owned cleanup: %+v", result)
		}
		return nil
	})
}
