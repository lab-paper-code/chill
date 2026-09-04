package operatorconsole

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// consoleTestNamespace is the namespace these view tests address.
const consoleTestNamespace = "chill-system"

func TestBuildProfilingRunsProjectsIdentitySpecAndStatus(t *testing.T) {
	t.Parallel()
	readAt := time.Date(2026, 8, 19, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	created := metav1.NewTime(time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC))
	view := BuildProfilingRuns(ProfilingRunsSnapshot{
		ReadAt: readAt, ResourceVersion: "runs-rv", Namespace: consoleTestNamespace,
		Runs: []edgev1alpha1.ProfilingRun{{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: consoleTestNamespace, Name: "listed-run", UID: "run-uid",
				ResourceVersion: "9", Generation: 1, CreationTimestamp: created,
			},
			Status: edgev1alpha1.ProfilingRunStatus{ObservedGeneration: 1},
		}},
	})

	if view.ReadAt != readAt.Format(time.RFC3339Nano) || view.ResourceVersion != "runs-rv" ||
		view.Namespace != consoleTestNamespace || view.AllNamespaces || len(view.Runs) != 1 {
		t.Fatalf("view = %#v", view)
	}
	run := view.Runs[0]
	if run.Namespace != consoleTestNamespace || run.Name != "listed-run" || run.UID != "run-uid" ||
		run.ResourceVersion != "9" || run.CreationTimestamp != "2026-08-14T03:00:00Z" ||
		run.Status.ObservedGeneration != 1 {
		t.Fatalf("run = %#v", run)
	}
}

// An empty snapshot namespace means the read covered every namespace. The view
// says so rather than leaving a reader to infer scope from a blank field.
func TestBuildProfilingRunsStatesItsOwnScope(t *testing.T) {
	t.Parallel()
	all := BuildProfilingRuns(ProfilingRunsSnapshot{ReadAt: time.Unix(0, 0).UTC()})
	if !all.AllNamespaces {
		t.Fatalf("empty namespace did not report all-namespaces scope: %#v", all)
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	// namespace is omitted when empty; runs is an empty list, never null.
	if strings.Contains(string(encoded), `"namespace"`) ||
		!strings.Contains(string(encoded), `"allNamespaces":true`) ||
		!strings.Contains(string(encoded), `"runs":[]`) {
		t.Fatalf("encoded = %s", encoded)
	}
}
