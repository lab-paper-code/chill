package kube

import (
	"context"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/queryapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestBatchEnergyOptimizationPreflightQueryRejectsInvalidCampaign(t *testing.T) {
	t.Parallel()
	tests := map[string]*edgev1alpha1.BatchEnergyOptimization{
		"nil campaign":        nil,
		"empty namespace":     batchEnergyOptimizationPreflightQueryCampaign(),
		"spaced namespace":    batchEnergyOptimizationPreflightQueryCampaign(),
		"empty name":          batchEnergyOptimizationPreflightQueryCampaign(),
		"space-suffixed name": batchEnergyOptimizationPreflightQueryCampaign(),
	}
	tests["empty namespace"].Namespace = ""
	tests["spaced namespace"].Namespace = " " + queryTestNamespace
	tests["empty name"].Name = ""
	tests["space-suffixed name"].Name += " "

	for name, campaign := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			query, err := NewKubernetesBatchEnergyOptimizationPreflightQuery(
				fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build(),
			)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := query.Read(context.Background(), campaign); err == nil {
				t.Fatal("invalid campaign was accepted")
			}
		})
	}
}

func TestPreflightWithoutSessionSettingsIsUnresolved(t *testing.T) {
	t.Parallel()
	query, err := NewKubernetesBatchEnergyOptimizationPreflightQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build(),
	)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := query.Read(context.Background(), batchEnergyOptimizationPreflightQueryCampaign())
	if err != nil {
		t.Fatal(err)
	}
	view, err := queryapi.BuildBatchEnergyOptimizationPreflight(snapshot)
	if err != nil || view.Resolves || view.Reason == "" {
		t.Fatalf("view = %+v, err = %v", view, err)
	}
}

func batchEnergyOptimizationPreflightQueryCampaign() *edgev1alpha1.BatchEnergyOptimization {
	return &edgev1alpha1.BatchEnergyOptimization{
		ObjectMeta: metav1.ObjectMeta{Namespace: queryTestNamespace, Name: "campaign"},
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{
			State: edgev1alpha1.MeasurementStateSpec{TargetNode: "orangepi"},
		},
	}
}
