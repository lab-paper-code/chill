package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	batch "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func measurementScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batch.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

func (o *measurementService) profilePayload(ctx context.Context, view httpapi.ProfilesView) (json.RawMessage, error) {
	response, err := o.buildProfiles(ctx, view)
	if err != nil {
		return nil, err
	}
	return json.Marshal(response)
}

func TestMeasurementNamespaceEscapeAndListActualNativeState(t *testing.T) {
	ctx := context.Background()
	pending := &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "pending", UID: "pending-uid"},
	}
	failed := &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "failed"},
		Status: api.BatchEnergyOptimizationStatus{
			Phase: "Failed", Outcome: "CurvePlanResolutionFailed",
			Conditions: []meta.Condition{{
				Type: "Done", Status: meta.ConditionFalse, Reason: "ActualFailure", Message: "unsupported actual input",
			}},
		},
	}
	complete := &api.BatchEnergyOptimization{
		ObjectMeta: meta.ObjectMeta{Namespace: "allowed", Name: "complete"},
		Status:     api.BatchEnergyOptimizationStatus{Phase: "Complete", CompletedTrials: 9},
	}
	escaped := &api.BatchEnergyOptimization{ObjectMeta: meta.ObjectMeta{Namespace: "outside", Name: "hidden"}}
	native := fake.NewClientBuilder().WithScheme(measurementScheme(t)).
		WithObjects(pending, failed, complete, escaped).Build()
	service := &measurementService{client: native, opts: options{profileNamespaces: []string{"allowed"}}}
	list, err := service.list(ctx, "allowed")
	if err != nil || len(list.Items) != 3 {
		t.Fatalf("native list: %+v %v", list, err)
	}
	if list.Items[0].Name != "complete" || list.Items[1].Status.Conditions[0].Reason != "ActualFailure" ||
		list.Items[2].Status.Phase != "" || list.Items[2].UID != pending.UID {
		t.Fatalf("native states lost: %+v", list)
	}
	if _, err := service.list(ctx, "outside"); !errors.Is(err, servingkube.ErrForbidden) {
		t.Fatal("namespace list escaped")
	}
}
