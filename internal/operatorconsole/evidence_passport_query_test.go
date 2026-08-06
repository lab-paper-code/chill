package operatorconsole

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
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
		got.Execution.IntentDigest != run.Status.IntentDigest || got.Measurement.PodUID != "pod-uid" {
		t.Fatalf("passport = %#v", got)
	}

	got.Run.Status.IntentDigest = "changed"
	if run.Status.IntentDigest == "changed" {
		t.Fatal("query returned a mutable alias of the Kubernetes object")
	}
}

func TestEvidencePassportQueryRejectsUnavailableOrContradictoryEvidence(t *testing.T) {
	t.Parallel()
	validRun, _, validExecution := passportFixture()
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ProfilingRun, *profilerevidence.ExecutionEvidence)
		match  string
	}{
		{
			name: "missing evidence reference",
			mutate: func(run *edgev1alpha1.ProfilingRun, _ *profilerevidence.ExecutionEvidence) {
				run.Status.Evidence = nil
			},
			match: "verified evidence is not available",
		},
		{
			name: "unsupported schema",
			mutate: func(run *edgev1alpha1.ProfilingRun, _ *profilerevidence.ExecutionEvidence) {
				run.Status.Evidence.SchemaVersion = "future"
			},
			match: "unsupported evidence schemaVersion",
		},
		{
			name: "intent mismatch",
			mutate: func(_ *edgev1alpha1.ProfilingRun, execution *profilerevidence.ExecutionEvidence) {
				execution.IntentDigest = "sha256:" + strings.Repeat("c", 64)
			},
			match: "evidence intent does not match",
		},
		{
			name: "Job mismatch",
			mutate: func(_ *edgev1alpha1.ProfilingRun, execution *profilerevidence.ExecutionEvidence) {
				execution.Terminal.Job.UID = "other"
			},
			match: "evidence Job does not match",
		},
		{
			name: "Pod mismatch",
			mutate: func(_ *edgev1alpha1.ProfilingRun, execution *profilerevidence.ExecutionEvidence) {
				execution.Terminal.Pod.UID = "other"
			},
			match: "evidence Pod or Node does not match",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			run := validRun.DeepCopy()
			execution := validExecution
			test.mutate(run, &execution)
			query, err := NewKubernetesEvidencePassportQuery(
				fake.NewClientBuilder().WithScheme(inventoryScheme(t)).WithObjects(run).Build(),
				&recordingEvidenceLoader{execution: execution},
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
) (profilerevidence.ExecutionEvidence, profilerevidence.RawMeasurement, error) {
	l.calls++
	l.reference = reference
	return l.execution, l.measurement, l.err
}

func passportFixture() (
	*edgev1alpha1.ProfilingRun,
	evidencestore.ObjectIdentity,
	profilerevidence.ExecutionEvidence,
) {
	digest := "sha256:" + strings.Repeat("a", 64)
	identity := evidencestore.ObjectIdentity{
		Store: "chill-evidence", Key: "profiling-runs/run/execution.json",
		Digest: "sha256:" + strings.Repeat("b", 64), Size: 1024,
	}
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "run", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			IntentDigest: digest,
			Job:          &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			ActualExecution: &edgev1alpha1.ProfilingRunActualExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{
				Store: identity.Store, Key: identity.Key, Digest: identity.Digest,
				Size: identity.Size, SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		IntentDigest:  digest,
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: "chill-system", Name: "job", UID: "job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: "chill-system", Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
	}
	return run, identity, execution
}
