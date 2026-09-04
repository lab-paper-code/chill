package kube

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEvidencePassportQueryFollowsRunReferenceAndPreservesSources(t *testing.T) {
	t.Parallel()
	run, identity, execution := passportFixture()
	measurement := profilerevidence.RawMeasurement{PodUID: "pod-uid"}
	loader := &recordingEvidenceLoader{execution: execution, measurement: measurement}
	query, err := NewKubernetesEvidencePassportQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(run).Build(),
		loader,
	)
	if err != nil {
		t.Fatal(err)
	}
	readAt := time.Date(2026, 7, 20, 6, 0, 0, 0, time.FixedZone("KST", 9*60*60))
	query.now = func() time.Time { return readAt }

	got, err := query.Read(context.Background(), run.Namespace, run.Name)
	if err != nil {
		t.Fatal(err)
	}
	if loader.calls != 1 || loader.reference != identity {
		t.Fatalf("loader calls/reference = %d, %#v", loader.calls, loader.reference)
	}
	if !got.ReadAt.Equal(readAt.UTC()) || got.Run.UID != run.UID ||
		got.Run.Status.ObservedGeneration != 1 || got.Measurement.PodUID != "pod-uid" {
		t.Fatalf("passport = %#v", got)
	}

	got.Run.Status.ObservedGeneration = 2
	if run.Status.ObservedGeneration != 1 {
		t.Fatal("query returned a mutable alias of the Kubernetes object")
	}
}

func TestEvidencePassportQueryRejectsUnavailableEvidenceReference(t *testing.T) {
	t.Parallel()
	validRun, _, _ := passportFixture()
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ProfilingRun)
		match  string
	}{
		{
			name: "missing evidence reference",
			mutate: func(run *edgev1alpha1.ProfilingRun) {
				run.Status.Evidence = nil
			},
			match: "verified evidence is not available",
		},
		{
			name: "unsupported schema",
			mutate: func(run *edgev1alpha1.ProfilingRun) {
				run.Status.Evidence.SchemaVersion = "future"
			},
			match: "unsupported evidence schemaVersion",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			run := validRun.DeepCopy()
			test.mutate(run)
			query, err := NewKubernetesEvidencePassportQuery(
				fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(run).Build(),
				&recordingEvidenceLoader{},
			)
			if err != nil {
				t.Fatal(err)
			}
			_, err = query.Read(context.Background(), run.Namespace, run.Name)
			if err == nil || !strings.Contains(err.Error(), test.match) {
				t.Fatalf("Read() error = %v, want %q", err, test.match)
			}
		})
	}
}

func TestEvidencePassportQueryPreservesEvidenceLoadFailure(t *testing.T) {
	t.Parallel()
	run, _, _ := passportFixture()
	want := errors.New("object unavailable")
	query, err := NewKubernetesEvidencePassportQuery(
		fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(run).Build(),
		&recordingEvidenceLoader{err: want},
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = query.Read(context.Background(), run.Namespace, run.Name)
	if !errors.Is(err, want) {
		t.Fatalf("Read() error = %v", err)
	}
}

func TestNewEvidencePassportQueryValidatesDependenciesAndIdentity(t *testing.T) {
	t.Parallel()
	loader := &recordingEvidenceLoader{}
	reader := fake.NewClientBuilder().WithScheme(inventoryScheme(t)).Build()
	if _, err := NewKubernetesEvidencePassportQuery(nil, loader); err == nil {
		t.Fatal("nil Kubernetes reader was accepted")
	}
	if _, err := NewKubernetesEvidencePassportQuery(reader, nil); err == nil {
		t.Fatal("nil evidence loader was accepted")
	}
	query, err := NewKubernetesEvidencePassportQuery(reader, loader)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Read(context.Background(), " ", "run"); err == nil {
		t.Fatal("invalid namespace was accepted")
	}
}

type recordingEvidenceLoader struct {
	reference   evidencestore.ObjectIdentity
	execution   profilerevidence.ExecutionEvidence
	measurement profilerevidence.RawMeasurement
	err         error
	calls       int
}

func (l *recordingEvidenceLoader) Load(
	_ context.Context,
	_ *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (VerifiedEvidenceView, error) {
	l.calls++
	l.reference = reference
	if l.err != nil {
		return nil, l.err
	}
	return recordingEvidenceView{execution: l.execution, measurement: l.measurement}, nil
}

type recordingEvidenceView struct {
	execution   profilerevidence.ExecutionEvidence
	measurement profilerevidence.RawMeasurement
}

func (v recordingEvidenceView) Records() (
	profilerevidence.ExecutionEvidence,
	profilerevidence.RawMeasurement,
	error,
) {
	return v.execution, v.measurement, nil
}

func passportFixture() (
	*edgev1alpha1.ProfilingRun,
	evidencestore.ObjectIdentity,
	profilerevidence.ExecutionEvidence,
) {
	identity := evidencestore.ObjectIdentity{
		Store: "chill-evidence", Key: "profiling-runs/run/execution.json",
		Size: 1024,
	}
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: consoleTestNamespace, Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			ObservedGeneration: 1,
			Job:                &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key,
				Size: identity.Size, SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: consoleTestNamespace, Name: "job", UID: "job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: consoleTestNamespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
	}
	return run, identity, execution
}
