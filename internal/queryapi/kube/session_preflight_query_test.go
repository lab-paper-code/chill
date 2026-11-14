package kube

import (
	"context"
	"errors"
	"slices"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/queryapi"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSessionPreflightResolvesWithPrepareSession(t *testing.T) {
	t.Parallel()
	campaign := batchEnergyOptimizationPreflightQueryCampaign()
	campaign.Spec.Session = &edgev1alpha1.BatchEnergyOptimizationSessionSpec{}
	query, err := NewKubernetesBatchEnergyOptimizationPreflightQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build(),
		func(context.Context, *edgev1alpha1.BatchEnergyOptimization) (profilederivation.CurvePlan, error) {
			t.Error("a session request is not projected to a curve plan")
			return profilederivation.CurvePlan{}, errors.New("unused")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var failure error
	query.Session = func(context.Context, *edgev1alpha1.BatchEnergyOptimization) (queryapi.SessionPreflightPlan, error) {
		return queryapi.SessionPreflightPlan{PlannedBatchSizes: []int{1, 2, 4}, MinimumSamples: 59, SLOPercentile: 95}, failure
	}
	snapshot, err := query.Read(context.Background(), campaign)
	if err != nil {
		t.Fatal(err)
	}
	view, err := queryapi.BuildBatchEnergyOptimizationPreflight(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Resolves || view.Session == nil || view.Session.MinimumSamples != 59 ||
		!slices.Equal(view.BatchPlan.PlannedBatches, []int{1, 2, 4}) || view.Plan != nil {
		t.Fatalf("resolved preflight = %+v", view)
	}

	failure = errors.New("resolve target Node: node orangepi not found")
	snapshot, err = query.Read(context.Background(), campaign)
	if err != nil {
		t.Fatal(err)
	}
	view, err = queryapi.BuildBatchEnergyOptimizationPreflight(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if view.Resolves || view.Reason != failure.Error() || view.Session != nil || len(view.BatchPlan.PlannedBatches) != 0 {
		t.Fatalf("unresolved preflight = %+v", view)
	}
}
