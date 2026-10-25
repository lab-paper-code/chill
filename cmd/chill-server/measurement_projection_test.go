package main

import (
	"context"
	"errors"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMeasurementProjectionNativeListsDetailsAndNamespace(t *testing.T) {
	ctx := context.Background()
	var objects []client.Object
	for _, namespace := range []string{"allowed", "outside"} {
		for _, name := range []string{"z-last", "a-first"} {
			metadata := meta.ObjectMeta{Namespace: namespace, Name: name, UID: "uid-" + "original"}
			objects = append(objects,
				&api.OperationCostMeasurement{ObjectMeta: metadata},
				&api.DispatchOccupancyMeasurement{ObjectMeta: metadata},
			)
		}
	}
	service := &measurementService{
		client: fake.NewClientBuilder().WithScheme(measurementScheme(t)).WithObjects(objects...).Build(),
		opts:   options{servingNamespace: "allowed", storeName: "evidence"},
	}
	for _, tc := range []struct {
		name string
		list func(context.Context, string) (httpapi.MeasurementProjectionList, error)
		get  func(context.Context, string, string) (httpapi.MeasurementProjection, error)
	}{
		{"operation", service.listOperationCosts, service.getOperationCost},
		{"occupancy", service.listDispatchOccupancies, service.getDispatchOccupancy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			list, err := tc.list(ctx, "allowed")
			if err != nil || len(list.Items) != 2 ||
				list.Items[0].Metadata.Name != "a-first" || list.Items[1].Metadata.Name != "z-last" {
				t.Fatalf("list=%+v err=%v", list, err)
			}
			detail, err := tc.get(ctx, "allowed", "a-first")
			if err != nil || detail.Metadata.UID != "uid-original" || detail.References.CR.Namespace != "allowed" {
				t.Fatalf("detail=%+v err=%v", detail, err)
			}
			if _, err = tc.get(ctx, "allowed", "absent"); !apierrors.IsNotFound(err) {
				t.Fatalf("expected native not found: %v", err)
			}
			if _, err = tc.list(ctx, "outside"); !errors.Is(err, servingkube.ErrForbidden) {
				t.Fatalf("outside list: %v", err)
			}
			if _, err = tc.get(ctx, "outside", "a-first"); !errors.Is(err, servingkube.ErrForbidden) {
				t.Fatalf("outside get: %v", err)
			}
			service.opts.profileNamespaces = []string{"empty"}
			empty, err := tc.list(ctx, "empty")
			if err != nil || empty.Items == nil || len(empty.Items) != 0 {
				t.Fatalf("empty=%+v err=%v", empty, err)
			}
			service.opts.profileNamespaces = nil
		})
	}
}
