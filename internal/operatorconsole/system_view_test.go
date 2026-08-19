package operatorconsole

import (
	"encoding/json"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildSystemsProjectsIdentityAndSeparatesRequestFromStatus(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	created := metav1.NewTime(time.Date(2026, 7, 15, 12, 53, 0, 0, time.UTC))
	view := BuildSystems(SystemSnapshot{
		ReadAt: readAt, ResourceVersion: "systems-rv",
		Systems: []edgev1alpha1.ChillSystem{{
			ObjectMeta: metav1.ObjectMeta{
				Name: "chill", UID: "system-uid", ResourceVersion: "7",
				Generation: 4, CreationTimestamp: created,
			},
			Spec: edgev1alpha1.ChillSystemSpec{
				ManagementNamespace: "chill-system",
				Profiler:            edgev1alpha1.ChillProfilerSpec{Enabled: true},
			},
			Status: edgev1alpha1.ChillSystemStatus{ObservedGeneration: 3},
		}},
	})

	if view.ReadAt != readAt.Format(time.RFC3339Nano) || view.ResourceVersion != "systems-rv" ||
		len(view.Systems) != 1 {
		t.Fatalf("view = %#v", view)
	}
	system := view.Systems[0]
	if system.Name != "chill" || system.UID != "system-uid" || system.ResourceVersion != "7" ||
		system.Generation != 4 || system.CreationTimestamp != "2026-07-15T12:53:00Z" {
		t.Fatalf("identity = %#v", system)
	}
	// The request and the observed status stay separate; no aggregate health.
	if !system.Spec.Profiler.Enabled || system.Status.ObservedGeneration != 3 {
		t.Fatalf("request/status = %#v", system)
	}
}

func TestBuildSystemsEncodesTheWireContractEveryTransportShares(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(BuildSystems(SystemSnapshot{
		ReadAt: time.Unix(0, 0).UTC(), ResourceVersion: "rv",
	}))
	if err != nil {
		t.Fatal(err)
	}
	// An empty read is an empty list, never a null, so a transport can range
	// over it without a nil check.
	want := `{"readAt":"1970-01-01T00:00:00Z","resourceVersion":"rv","systems":[]}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}
}
