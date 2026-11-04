package kube

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCatalogReplacementSelectionPreservesReuseSource(t *testing.T) {
	source, target := reuseProfilesFixture(t)
	conflict := cloneCatalogProfile(source)
	conflict.ID = "chill-system/new-confirmation/uid"
	conflict.MeasuredAt = source.MeasuredAt.Add(time.Second)
	request, err := json.Marshal(map[string]any{"sourceProfileID": source.ID, "maximumRelativeDifference": .05})
	if err != nil {
		t.Fatal(err)
	}
	requests := map[string]string{target.ID: string(request), conflict.ID: `{"sourceProfileID":"missing/source/uid","maximumRelativeDifference":0.05}`}
	scheme := runtime.NewScheme()
	if err := edge.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store := &Store{client: fake.NewClientBuilder().WithScheme(scheme).Build()}
	catalog, err := NewCatalog(store, &catalogTestObjects{}, 4096)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Seed verified entries, preserving immutable evidence bindings. Refresh still
	// reads native UID/resourceVersion and applies the real selection/reuse pipeline.
	for _, profile := range []serving.Profile{source, conflict, target} {
		parts := strings.Split(profile.ID, "/")
		campaign := &edge.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Namespace: parts[0], Name: parts[1], UID: types.UID(parts[2]), Generation: 1}, Status: edge.BatchEnergyOptimizationStatus{Phase: catalogCompletePhase, ObservedGeneration: 1}}
		campaign.Status.Result = &edge.ProfilingRunEvidenceReference{}
		if request, found := requests[profile.ID]; found {
			campaign.Annotations = map[string]string{profileReuseAnnotation: request}
		}
		if err := store.client.Create(ctx, campaign); err != nil {
			t.Fatal(err)
		}
		key := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
		catalog.current[key] = catalogProfile{uid: campaign.UID, version: campaign.ResourceVersion, profile: profile}
	}
	view, err := catalog.Refresh(ctx, []string{"chill-system"})
	if err != nil || len(view.Profiles) != 2 {
		t.Fatalf("valid direct/reused profiles lost: profiles=%+v checks=%+v error=%v", view.Profiles, view.ReuseChecks, err)
	}
	direct, reused := false, false
	for _, profile := range view.Profiles {
		direct = direct || profile.ID == source.ID
		reused = reused || (profile.Reuse != nil && profile.Reuse.ConfirmationProfileID == target.ID)
	}
	if !direct || !reused {
		t.Fatalf("wanted source and accepted target: %+v", view.Profiles)
	}
}
