package kube

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

func TestNewKubernetesSystemQueryRequiresReader(t *testing.T) {
	if _, err := NewKubernetesSystemQuery(nil); err == nil {
		t.Fatal("NewKubernetesSystemQuery() error = nil")
	}
}

func TestKubernetesSystemQueryReadsAuthorities(t *testing.T) {
	scheme := inventoryScheme(t)
	systemB := &edgev1alpha1.ChillSystem{ObjectMeta: metav1.ObjectMeta{Name: "system-b"}}
	systemA := &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{Name: "system-a", UID: "system-uid", Generation: 3},
		Spec: edgev1alpha1.ChillSystemSpec{
			ManagementNamespace: "chill-system",
			Profiler:            edgev1alpha1.ChillProfilerSpec{Enabled: true},
		},
		Status: edgev1alpha1.ChillSystemStatus{
			ObservedGeneration: 3,
			Conditions: []metav1.Condition{{
				Type: edgev1alpha1.ChillSystemConditionProfilerAvailable, Status: metav1.ConditionTrue,
			}},
		},
	}
	reader := &systemResourceVersionReader{
		Reader:          fake.NewClientBuilder().WithScheme(scheme).WithObjects(systemB, systemA).Build(),
		resourceVersion: "systems-rv",
	}
	query, err := NewKubernetesSystemQuery(reader)
	if err != nil {
		t.Fatalf("NewKubernetesSystemQuery() error = %v", err)
	}
	readAt := time.Date(2026, 7, 20, 3, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	query.now = func() time.Time { return readAt }

	got, err := query.Read(context.Background())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !got.ReadAt.Equal(readAt.UTC()) || got.ResourceVersion != "systems-rv" {
		t.Fatalf("snapshot metadata = %v, %q", got.ReadAt, got.ResourceVersion)
	}
	if len(got.Systems) != 2 || got.Systems[0].Name != "system-a" || got.Systems[1].Name != "system-b" {
		t.Fatalf("Systems = %#v", got.Systems)
	}
	if got.Systems[0].Status.ObservedGeneration != 3 ||
		len(got.Systems[0].Status.Conditions) != 1 {
		t.Fatalf("system-a status = %#v", got.Systems[0].Status)
	}
}

func TestKubernetesSystemQueryReportsReadFailure(t *testing.T) {
	want := errors.New("API unavailable")
	query, err := NewKubernetesSystemQuery(&failingSystemReader{err: want})
	if err != nil {
		t.Fatalf("NewKubernetesSystemQuery() error = %v", err)
	}
	_, err = query.Read(context.Background())
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "list ChillSystems") {
		t.Fatalf("Read() error = %v", err)
	}
}

type systemResourceVersionReader struct {
	client.Reader
	resourceVersion string
}

func (r *systemResourceVersionReader) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	if err := r.Reader.List(ctx, list, opts...); err != nil {
		return err
	}
	list.(*edgev1alpha1.ChillSystemList).ResourceVersion = r.resourceVersion
	return nil
}

type failingSystemReader struct {
	err error
}

func (r *failingSystemReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return nil
}

func (r *failingSystemReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return r.err
}
