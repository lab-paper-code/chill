package evidence

import (
	"context"
	"errors"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBindsEvidenceToRecordedRun(t *testing.T) {
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "active", UID: "run-uid"},
		Status: edgev1alpha1.ProfilingRunStatus{
			Job:      &edgev1alpha1.ProfilingRunObjectReference{Name: "job", UID: "job-uid"},
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion},
			ObservedExecution: &edgev1alpha1.ProfilingRunObservedExecutionStatus{
				Pod:  edgev1alpha1.ProfilingRunObjectReference{Name: "pod", UID: "pod-uid"},
				Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "node-uid"},
			},
		},
	}
	execution := profilerevidence.ExecutionEvidence{
		SchemaVersion: profilerevidence.ExecutionEvidenceSchemaVersion,
		Terminal: profilerevidence.TerminalFacts{
			Job:  profilerevidence.JobTerminalFacts{Namespace: run.Namespace, Name: "job", UID: "other-job-uid"},
			Pod:  profilerevidence.PodTerminalFacts{Namespace: run.Namespace, Name: "pod", UID: "pod-uid"},
			Node: profilerevidence.NodeTerminalFacts{Name: "node", UID: "node-uid"},
		},
	}
	if err := validateExecution(run, execution); err == nil || !strings.Contains(err.Error(), "identities") {
		t.Fatalf("foreign retained evidence accepted: %v", err)
	}
}

func TestAcceptsHistoricalRunConfigWithoutDurationTolerance(t *testing.T) {
	run := &edgev1alpha1.ProfilingRun{}
	config := profilingrun.RunConfig{}
	defaulted := .5
	run.Spec.Measurement.DurationToleranceSeconds = &defaulted
	if err := validateRunConfig(run, config); err != nil {
		t.Fatalf("historical zero tolerance rejected: %v", err)
	}
	config.Measurement.DurationToleranceSeconds = .5
	explicit := .3
	run.Spec.Measurement.DurationToleranceSeconds = &explicit
	if err := validateRunConfig(run, config); err == nil || !strings.Contains(err.Error(), "Run protocol") {
		t.Fatalf("explicit spec tolerance mismatch accepted: %v", err)
	}
}

func TestReadRequiresResultBeforeObjectAccess(t *testing.T) {
	reader := Reader{ReadObject: func(context.Context, evidencestore.ObjectIdentity) ([]byte, error) {
		t.Fatal("missing result reached store")
		return nil, nil
	}}
	if _, err := reader.Read(context.Background(), &edgev1alpha1.BatchEnergyOptimization{}); err == nil || !strings.Contains(err.Error(), "no terminal result") {
		t.Fatalf("missing result accepted: %v", err)
	}
}

func TestReadPreservesStoreError(t *testing.T) {
	missing := errors.New("missing retained object")
	reader := Reader{ReadObject: func(context.Context, evidencestore.ObjectIdentity) ([]byte, error) { return nil, missing }}
	campaign := &edgev1alpha1.BatchEnergyOptimization{Status: edgev1alpha1.BatchEnergyOptimizationStatus{Result: &edgev1alpha1.ProfilingRunEvidenceReference{Store: "evidence", Key: "result", Size: 1}}}
	if _, err := reader.Read(context.Background(), campaign); !errors.Is(err, missing) {
		t.Fatalf("store error lost: %v", err)
	}
}
