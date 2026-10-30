package kube

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

func TestModelCompatibilityQueryValidatesSelectionAndPreservesFailures(t *testing.T) {
	if _, err := NewKubernetesModelCompatibilityQuery(nil); err == nil {
		t.Fatal("nil resolver accepted")
	}
	selection := operatorconsole.ModelCompatibilitySelection{ModelSpec: "model", ExecutionPath: "ort-cpu", DeviceClass: "class", Node: "node"}
	calls := 0
	wantError := errors.New("fact lookup failed")
	query, err := NewKubernetesModelCompatibilityQuery(func(_ context.Context, got operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error) {
		calls++
		if got != selection {
			t.Fatalf("selection changed: %+v", got)
		}
		return operatorconsole.ModelCompatibilitySnapshot{}, wantError
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []operatorconsole.ModelCompatibilitySelection{
		{}, {ModelSpec: "Bad", ExecutionPath: "ort-cpu", DeviceClass: "class", Node: "node"},
		{ModelSpec: "model", ExecutionPath: "ort-cpu", DeviceClass: "class", Node: " node"},
		{ModelSpec: "model", ExecutionPath: "ort-cpu", DeviceClass: "bad/class", Node: "node"},
		{ModelSpec: "model", ExecutionPath: " ort-cpu", DeviceClass: "class", Node: "node"},
	} {
		if _, err := query.Read(context.Background(), invalid); !apierrors.IsBadRequest(err) {
			t.Fatalf("invalid selection accepted: %+v, %v", invalid, err)
		}
	}
	if calls != 0 {
		t.Fatal("invalid query reached facts")
	}
	if _, err := query.Read(context.Background(), selection); !errors.Is(err, wantError) || calls != 1 {
		t.Fatalf("lookup error changed: %v, calls=%d", err, calls)
	}
	query.resolve = func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error) {
		return operatorconsole.ModelCompatibilitySnapshot{Verdict: "Unknown"}, nil
	}
	readAt := time.Date(2026, 10, 30, 12, 0, 0, 123, time.FixedZone("KST", 9*60*60))
	query.now = func() time.Time { return readAt }
	got, err := query.Read(context.Background(), selection)
	if err != nil || got.Selection != selection || got.ReadAt.Location() != time.UTC || !got.ReadAt.Equal(readAt) {
		t.Fatalf("snapshot = %+v, %v", got, err)
	}
}
