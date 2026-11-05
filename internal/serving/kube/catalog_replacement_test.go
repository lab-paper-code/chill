package kube

import (
	"context"
	"reflect"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const replacementSuffix = "-replacement"

func TestReplacementSelectionPreservesOriginalsAndReportsSpecDifferences(t *testing.T) {
	for _, name := range []string{"jetsono-012-smolvlm-fp32-q4-maxn", "orangepi-017-plc-cpu-q4"} {
		t.Run(name, func(t *testing.T) {
			original := edge.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "chill-system"}, Spec: edge.BatchEnergyOptimizationSpec{TrialsPerBatch: 3, RetryLimit: 3}}
			original.Spec.State.TargetNode = "node"
			before := original.DeepCopy()
			replacement := *original.DeepCopy()
			replacement.Name += replacementSuffix
			replacement.Annotations = map[string]string{profilederivation.ReplacesMeasurementAnnotation: original.Name}
			replacement.Spec.ActiveProtocol.MetricObservation.DurationSeconds = 1800
			items := []edge.BatchEnergyOptimization{original, replacement}
			selection := replacementCampaignSelection(items)
			if selection[original.Name] == "" || selection[replacement.Name] == "" {
				t.Fatalf("selection=%+v", selection)
			}
			if !reflect.DeepEqual(&items[0], before) {
				t.Fatal("original was modified")
			}
			items[1].Spec.TrialsPerBatch = 4
			selection = replacementCampaignSelection(items)
			if selection[original.Name] == "" || selection[replacement.Name] == "" {
				t.Fatalf("changed trial policy was not diagnosed: %+v", selection)
			}
			items[1] = replacement
			items[1].Spec.State.TargetNode = "other-node"
			if replacementCampaignSelection(items)[replacement.Name] == "" {
				t.Fatal("changed physical state was not diagnosed")
			}
		})
	}
}

func TestCatalogReplacementPendingPreservesExistingPublicPopulation(t *testing.T) {
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
	replacement.Annotations = map[string]string{profilederivation.ReplacesMeasurementAnnotation: original.Name}
	replacement.Status = edge.BatchEnergyOptimizationStatus{Phase: "Measuring"}
	if err := catalog.store.client.Create(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	view, err = catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.CurrentProfiles()) != 0 || len(view.Insufficiencies) != 1 || view.Insufficiencies[0].Code != "ReplacementSelection" {
		t.Fatalf("pending replacement revived an explicitly replaced profile: %+v error=%v", view, err)
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
	replacement.Annotations = map[string]string{profilederivation.ReplacesMeasurementAnnotation: original.Name}
	if err := catalog.store.client.Update(ctx, replacement); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 || view.Profiles[0].ID != source.String()+"/"+string(replacement.UID) {
		t.Fatalf("verified replacement not selected: %+v error=%v", view, err)
	}
}

func TestCatalogReplacementMissingOriginalIsDiagnosticButEvidenceStillRequired(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	replacement := &edge.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Name: "replacement", Namespace: "chill-system", Generation: 1, Annotations: map[string]string{profilederivation.ReplacesMeasurementAnnotation: "missing"}}, Status: edge.BatchEnergyOptimizationStatus{Phase: "Complete", ObservedGeneration: 1}}
	objects := &catalogTestObjects{}
	catalog, err := NewCatalog(&Store{client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(replacement).Build()}, objects, 1024)
	if err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(context.Background(), []string{replacement.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.Insufficiencies) != 2 || objects.opens != 0 {
		t.Fatalf("missing original=%+v error=%v", view, err)
	}
	var retained edge.BatchEnergyOptimizationList
	if err := catalog.store.client.List(context.Background(), &retained, client.InNamespace(replacement.Namespace)); err != nil || len(retained.Items) != 1 {
		t.Fatalf("unexpected consumer mutation: %v", err)
	}
}

func TestOperatorReplacementDiagnosticsDoNotRejectVerifiedProfile(t *testing.T) {
	for _, target := range []string{"missing", "self", "different-spec"} {
		t.Run(target, func(t *testing.T) {
			catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
			ctx := context.Background()
			replacement := &edge.BatchEnergyOptimization{}
			if err := catalog.store.client.Get(ctx, source, replacement); err != nil {
				t.Fatal(err)
			}
			name := target
			if target == "self" {
				name = replacement.Name
			}
			if target == "different-spec" {
				original := replacement.DeepCopy()
				original.Name, original.UID, original.ResourceVersion = target, "old-uid", ""
				original.Spec.TrialsPerBatch++
				original.Status = edge.BatchEnergyOptimizationStatus{Phase: "Failed"}
				if err := catalog.store.client.Create(ctx, original); err != nil {
					t.Fatal(err)
				}
			}
			replacement.Annotations = map[string]string{profilederivation.ReplacesMeasurementAnnotation: name}
			if err := catalog.store.client.Update(ctx, replacement); err != nil {
				t.Fatal(err)
			}
			view, err := catalog.Refresh(ctx, []string{source.Namespace})
			if err != nil || len(view.CurrentProfiles()) != 1 || len(view.Profiles) != 1 {
				t.Fatalf("diagnostic blocked verified execution: %+v %v", view, err)
			}
			found := false
			for _, issue := range view.Insufficiencies {
				found = found || issue.Source == source && issue.Code == "ReplacementSelection"
			}
			if !found {
				t.Fatal("ineffective or changed-spec operator selection was not diagnosed")
			}
		})
	}
}

func TestCatalogReplacementDoesNotReviveOlderConditionProfiles(t *testing.T) {
	catalog, source, _, _ := catalogEvidenceFixture(t, 4096)
	ctx := context.Background()
	view, err := catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("fixture: %+v %v", view, err)
	}
	q3 := &edge.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(ctx, source, q3); err != nil {
		t.Fatal(err)
	}
	// Seed already-verified cache entries; cloned evidence cannot be rebound to
	// another campaign. Refresh must still read each native object's identity.
	for i, name := range []string{"q1", "q2", testReplacementUID} {
		campaign := q3.DeepCopy()
		campaign.Name, campaign.UID, campaign.ResourceVersion = name, types.UID(name), ""
		if name == testReplacementUID {
			campaign.Annotations = map[string]string{profilederivation.ReplacesMeasurementAnnotation: q3.Name}
		}
		if err := catalog.store.client.Create(ctx, campaign); err != nil {
			t.Fatal(err)
		}
		profile := cloneCatalogProfile(view.Profiles[0])
		profile.ID = campaign.Namespace + "/" + campaign.Name + "/" + string(campaign.UID)
		profile.MeasuredAt = profile.MeasuredAt.Add(time.Duration(i-3) * time.Hour)
		if name == testReplacementUID {
			profile.MeasuredAt = view.Profiles[0].MeasuredAt.Add(time.Hour)
			profile.Condition.ActiveProtocol.DurationSeconds++
		}
		key := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
		catalog.current[key] = catalogProfile{uid: campaign.UID, version: campaign.ResourceVersion, profile: profile}
	}
	view, err = catalog.Refresh(ctx, []string{source.Namespace})
	if err != nil || len(view.Profiles) != 1 || view.Profiles[0].ID != source.Namespace+"/replacement/replacement" {
		t.Fatalf("older condition resurfaced: %+v error=%v", view.Profiles, err)
	}
}
