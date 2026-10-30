package kube

import (
	"context"
	"reflect"
	"testing"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const replacementSuffix = "-replacement"

func TestReplacementSelectionPreservesOriginalsAndExactTrialPolicy(t *testing.T) {
	for _, name := range []string{"jetsono-012-smolvlm-fp32-q4-maxn", "orangepi-017-plc-cpu-q4"} {
		t.Run(name, func(t *testing.T) {
			original := edge.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "chill-system"}, Spec: edge.BatchEnergyOptimizationSpec{TrialsPerBatch: 3, RetryLimit: 1, BatchSizes: []int32{1, 2, 4}}}
			original.Spec.State.TargetNode = "node"
			before := original.DeepCopy()
			replacement := *original.DeepCopy()
			replacement.Name += replacementSuffix
			replacement.Annotations = map[string]string{replacesCampaignAnnotation: original.Name}
			replacement.Spec.ActiveProtocol.MetricObservation.DurationSeconds = 1800
			items := []edge.BatchEnergyOptimization{original, replacement}
			selection := replacementCampaignSelection(items)
			if selection[original.Name] == "" || selection[replacement.Name] != "" {
				t.Fatalf("selection=%+v", selection)
			}
			if !reflect.DeepEqual(&items[0], before) {
				t.Fatal("original was modified")
			}
			items[1].Spec.TrialsPerBatch = 4
			selection = replacementCampaignSelection(items)
			if selection[original.Name] == "" || selection[replacement.Name] == "" {
				t.Fatalf("incoherent trial policy accepted: %+v", selection)
			}
			items[1] = replacement
			items[1].Spec.State.TargetNode = "other-node"
			if replacementCampaignSelection(items)[replacement.Name] == "" {
				t.Fatal("changed physical state accepted")
			}
		})
	}
}

func TestCatalogReplacementPendingExcludesCachedOriginalWithoutMutation(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	ctx := context.Background()
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("fixture=%+v error=%v", view, err)
	}
	original := &edge.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, original); err != nil {
		t.Fatal(err)
	}
	before := original.DeepCopy()
	replacement := original.DeepCopy()
	replacement.Name, replacement.UID, replacement.ResourceVersion = "coherent-replacement", "replacement-uid", ""
	replacement.Annotations = map[string]string{replacesCampaignAnnotation: original.Name}
	replacement.Status = edge.BatchEnergyOptimizationStatus{Phase: "Measuring"}
	if err := catalog.store.client.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	view, err = catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || view.Insufficiencies[0].Code != "ReplacementSelection" {
		t.Fatalf("pending replacement used old profile: %+v error=%v", view, err)
	}
	if _, found := catalog.current[source]; found {
		t.Fatal("superseded cached profile retained")
	}
	actual := &edge.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, actual) {
		t.Fatal("consumer modified original evidence/status")
	}
}

func TestCatalogReplacementStillRequiresNormalVerifiedEvidence(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	ctx := context.Background()
	replacement := &edge.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, replacement); err != nil {
		t.Fatal(err)
	}
	// Rename the actual verified fixture only in the replacement direction: its
	// evidence remains bound to source, so create an old source with no evidence.
	original := replacement.DeepCopy()
	original.Name, original.UID, original.ResourceVersion = "preserved-original", types.UID("original-uid"), ""
	original.Status = edge.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: original.Generation}
	if err := catalog.store.client.Create(ctx, original); err != nil {
		t.Fatal(err)
	}
	replacement.Annotations = map[string]string{replacesCampaignAnnotation: original.Name}
	if err := catalog.store.client.Update(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 || view.Profiles[0].ID != source.String()+"/"+string(replacement.UID) {
		t.Fatalf("verified replacement not selected: %+v error=%v", view, err)
	}
}

func TestCatalogReplacementMissingOriginalFailsClosed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	replacement := &edge.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Name: "replacement", Namespace: "chill-system", Generation: 1, Annotations: map[string]string{replacesCampaignAnnotation: "missing"}}, Status: edge.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1}}
	objects := &catalogTestObjects{}
	catalog, err := NewCatalog(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(replacement).Build()}, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(context.Background(), []string{replacement.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 1 || objects.opens != 0 {
		t.Fatalf("missing original=%+v error=%v", view, err)
	}
	var retained edge.BatchEnergyOptimizationList
	if err := catalog.store.client.List(context.Background(), &retained, client.InNamespace(replacement.Namespace)); err != nil || len(retained.Items) != 1 {
		t.Fatalf("unexpected consumer mutation: %v", err)
	}
}
