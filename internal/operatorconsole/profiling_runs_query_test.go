package operatorconsole

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNewKubernetesProfilingRunsQueryRequiresReader(t *testing.T) {
	if _, err := NewKubernetesProfilingRunsQuery(nil, "chill-system"); err == nil {
		t.Fatal("NewKubernetesProfilingRunsQuery() error = nil")
	}
}

func TestKubernetesProfilingRunsQueryReadsSelectedNamespace(t *testing.T) {
	scheme := inventoryScheme(t)
	runB := &edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run-b"}}
	runA := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run-a", UID: "run-uid", Generation: 2},
		Status:     edgev1alpha1.ProfilingRunStatus{ObservedGeneration: 2, IntentFrozen: true},
	}
	foreign := &edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Namespace: "other", Name: "foreign"}}
	reader := &profilingRunsResourceVersionReader{
		Reader:          fake.NewClientBuilder().WithScheme(scheme).WithObjects(runB, runA, foreign).Build(),
		resourceVersion: "runs-rv",
	}
	query, err := NewKubernetesProfilingRunsQuery(reader, "chill-system")
	if err != nil {
		t.Fatalf("NewKubernetesProfilingRunsQuery() error = %v", err)
	}
	readAt := time.Date(2026, 7, 20, 4, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	query.now = func() time.Time { return readAt }

	got, err := query.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !got.ReadAt.Equal(readAt.UTC()) || got.ResourceVersion != "runs-rv" || got.Namespace != "chill-system" {
		t.Fatalf("snapshot metadata = %v, %q, %q", got.ReadAt, got.ResourceVersion, got.Namespace)
	}
	if len(got.Runs) != 2 || got.Runs[0].Name != "run-a" || got.Runs[1].Name != "run-b" ||
		got.Runs[0].Status.ObservedGeneration != 2 {
		t.Fatalf("Runs = %#v", got.Runs)
	}
}

func TestKubernetesProfilingRunsQueryReadsAllNamespaces(t *testing.T) {
	scheme := inventoryScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Namespace: "z", Name: "run"}},
		&edgev1alpha1.ProfilingRun{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "run"}},
	).Build()
	query, err := NewKubernetesProfilingRunsQuery(reader, "")
	if err != nil {
		t.Fatalf("NewKubernetesProfilingRunsQuery() error = %v", err)
	}

	got, err := query.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(got.Runs) != 2 || got.Runs[0].Namespace != "a" || got.Runs[1].Namespace != "z" {
		t.Fatalf("Runs = %#v", got.Runs)
	}
}

func TestKubernetesProfilingRunsQueryReportsReadFailure(t *testing.T) {
	want := errors.New("API unavailable")
	query, err := NewKubernetesProfilingRunsQuery(&failingSystemReader{err: want}, "chill-system")
	if err != nil {
		t.Fatalf("NewKubernetesProfilingRunsQuery() error = %v", err)
	}
	_, err = query.Read(context.Background())
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "list ProfilingRuns") {
		t.Fatalf("Read() error = %v", err)
	}
}

type profilingRunsResourceVersionReader struct {
	client.Reader
	resourceVersion string
}

func (r *profilingRunsResourceVersionReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	list.(*edgev1alpha1.ProfilingRunList).ResourceVersion = r.resourceVersion
	return nil
}
