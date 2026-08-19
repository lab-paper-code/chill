package operatorconsole

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuildInventoryJoinsClassesWithMatchedNodesAndTheModelCatalog(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	view := BuildInventory(InventorySnapshot{
		ReadAt:                       readAt,
		DeviceClassesResourceVersion: "classes-rv",
		NodesResourceVersion:         "nodes-rv",
		ModelSpecsResourceVersion:    "models-rv",
		Classes: []deviceclass.InventoryClass{{
			Name: "jetson-agx-orin-64g", UID: "class-uid", ResourceVersion: "3",
			Spec: edgev1alpha1.DeviceClassSpec{
				NodeSelector: map[string]string{"edge.dacs.io/device-class": "jetson-agx-orin-64g"},
				Architecture: "arm64", MemoryBytes: resource.MustParse("64Gi"),
				PowerModes: []edgev1alpha1.PowerMode{{Name: "MAXN"}},
			},
			Nodes: []deviceclass.InventoryNode{{
				Name: "jetsono", UID: "node-uid", ResourceVersion: "11",
				Ready: corev1.ConditionTrue, SchedulingDisabled: false,
			}},
		}},
		Models: []edgev1alpha1.ModelSpec{{
			ObjectMeta: metav1.ObjectMeta{Name: "mobilenet-v2-050", UID: "model-uid", ResourceVersion: "5"},
		}},
	})

	// The three reads keep their own resourceVersions; this is not one atomic
	// cross-resource snapshot and must not read like one.
	if view.DeviceClassesResourceVersion != "classes-rv" || view.NodesResourceVersion != "nodes-rv" ||
		view.ModelSpecsResourceVersion != "models-rv" || view.ReadAt != readAt.Format(time.RFC3339Nano) {
		t.Fatalf("view = %#v", view)
	}
	if len(view.Classes) != 1 || len(view.Models) != 1 {
		t.Fatalf("classes/models = %d/%d", len(view.Classes), len(view.Models))
	}
	class := view.Classes[0]
	if class.Name != "jetson-agx-orin-64g" || class.Architecture != "arm64" ||
		class.Memory != "64Gi" || len(class.PowerModes) != 1 || class.PowerModes[0].Name != "MAXN" {
		t.Fatalf("class = %#v", class)
	}
	if len(class.Nodes) != 1 || class.Nodes[0].Name != "jetsono" ||
		class.Nodes[0].Ready != string(corev1.ConditionTrue) || class.Nodes[0].SchedulingDisabled {
		t.Fatalf("nodes = %#v", class.Nodes)
	}
	if view.Models[0].Name != "mobilenet-v2-050" || view.Models[0].UID != "model-uid" {
		t.Fatalf("models = %#v", view.Models)
	}
}

// Node Ready is the tri-state Kubernetes reports. An unknown Node must not be
// flattened into a boolean by the projection.
func TestBuildInventoryPreservesNodeReadyTriState(t *testing.T) {
	t.Parallel()
	for _, ready := range []corev1.ConditionStatus{
		corev1.ConditionTrue, corev1.ConditionFalse, corev1.ConditionUnknown,
	} {
		view := BuildInventory(InventorySnapshot{
			ReadAt: time.Unix(0, 0).UTC(),
			Classes: []deviceclass.InventoryClass{{
				Name:  "class",
				Nodes: []deviceclass.InventoryNode{{Name: "node", Ready: ready}},
			}},
		})
		if got := view.Classes[0].Nodes[0].Ready; got != string(ready) {
			t.Fatalf("Ready = %q, want %q", got, ready)
		}
	}
}

func TestBuildInventoryEncodesEmptyListsRatherThanNulls(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(BuildInventory(InventorySnapshot{ReadAt: time.Unix(0, 0).UTC()}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"classes":[]`) ||
		!strings.Contains(string(encoded), `"models":[]`) {
		t.Fatalf("encoded = %s", encoded)
	}
}
