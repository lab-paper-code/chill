package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	projection "github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
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

func TestRunErrorCausesSurviveTrialWrapping(t *testing.T) {
	const differentReference = "other"
	cases := []struct {
		name   string
		change func(*edgev1alpha1.ProfilingRun)
		want   error
	}{
		{"absent evidence", func(run *edgev1alpha1.ProfilingRun) { run.Status.Evidence = nil }, ErrRunEvidenceAbsent},
		{"different store", func(run *edgev1alpha1.ProfilingRun) { run.Status.Evidence.Store = differentReference }, ErrRunEvidenceMismatch},
		{"different key", func(run *edgev1alpha1.ProfilingRun) { run.Status.Evidence.Key = differentReference }, ErrRunEvidenceMismatch},
		{"different size", func(run *edgev1alpha1.ProfilingRun) { run.Status.Evidence.Size++ }, ErrRunEvidenceMismatch},
		{"different schema", func(run *edgev1alpha1.ProfilingRun) { run.Status.Evidence.SchemaVersion = differentReference }, ErrRunEvidenceMismatch},
		{"unsupported mode", func(run *edgev1alpha1.ProfilingRun) { run.Spec.Measurement.WorkloadMode = "unsupported" }, ErrUnsupportedWorkloadMode},
		{"active protocol", func(run *edgev1alpha1.ProfilingRun) { run.Spec.Measurement.DurationSeconds++ }, ErrActiveProtocolMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cr, run, ref := completedRunFixture()
			if err := validateRun(cr, run, ref); err != nil {
				t.Fatalf("valid Run rejected: %v", err)
			}
			tc.change(run)
			scheme := runtime.NewScheme()
			if err := edgev1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			reader := Reader{Runs: fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()}
			err := reader.readCompletedTrial(context.Background(), cr, ref).err
			if !errors.Is(err, tc.want) || err.Error() != "completed Run ns/run: "+tc.want.Error() {
				t.Fatalf("wrong wrapped cause or changed diagnostic: %v, want %v", err, tc.want)
			}
			for _, other := range []error{ErrRunEvidenceAbsent, ErrRunEvidenceMismatch, ErrUnsupportedWorkloadMode, ErrActiveProtocolMismatch} {
				if other != tc.want && errors.Is(err, other) {
					t.Fatalf("distinct causes conflated: %v matches %v", tc.want, other)
				}
			}
		})
	}
}

func TestUnsupportedWorkloadModePrecedesReferenceModeMismatch(t *testing.T) {
	cr, run, ref := completedRunFixture()
	ref.Measurement.WorkloadMode = campaign.WorkloadModeIdle
	run.Spec.Measurement.WorkloadMode = "unsupported"
	if err := validateRun(cr, run, ref); !errors.Is(err, ErrUnsupportedWorkloadMode) {
		t.Fatalf("idle reference masked unsupported Run mode: %v", err)
	}
	run.Status.Conditions = nil
	run.Status.Evidence = nil
	if err := validateRun(cr, run, ref); !errors.Is(err, ErrUnsupportedWorkloadMode) {
		t.Fatalf("completion or evidence masked unsupported Run mode: %v", err)
	}
	run.Spec.Measurement.WorkloadMode = "saturated-serial-fixed-batch"
	if err := validateRun(cr, run, ref); err == nil || err.Error() != "Run workload mode contradicts completed trial reference" {
		t.Fatalf("supported mode mismatch lost its diagnostic: %v", err)
	}
}

func TestRunCompletionPrecedesEvidenceAndProtocol(t *testing.T) {
	cr, run, ref := completedRunFixture()
	run.Spec.Measurement.DurationSeconds++
	run.Status.Evidence = nil
	for index := range run.Status.Conditions {
		t.Run(run.Status.Conditions[index].Type, func(t *testing.T) {
			run.Status.Conditions[index].ObservedGeneration--
			err := validateRun(cr, run, ref)
			if err == nil || err.Error() != "Run has no current completed execution and verified evidence" {
				t.Fatalf("completion did not precede absent evidence and invalid protocol: %v", err)
			}
			run.Status.Conditions[index].ObservedGeneration++
		})
	}
	if err := validateRun(cr, run, ref); !errors.Is(err, ErrRunEvidenceAbsent) {
		t.Fatalf("evidence did not precede protocol: %v", err)
	}
	run.Status.Evidence = &edgev1alpha1.ProfilingRunEvidenceReference{Store: ref.Evidence.Store, Key: "other", Size: ref.Evidence.Size, SchemaVersion: ref.Evidence.SchemaVersion}
	if err := validateRun(cr, run, ref); !errors.Is(err, ErrRunEvidenceMismatch) {
		t.Fatalf("evidence mismatch did not precede protocol: %v", err)
	}
	run.Status.Evidence.Key = ref.Evidence.Key
	if err := validateRun(cr, run, ref); !errors.Is(err, ErrActiveProtocolMismatch) {
		t.Fatalf("protocol mismatch was not retained: %v", err)
	}
}

func completedRunFixture() (*edgev1alpha1.BatchEnergyOptimization, *edgev1alpha1.ProfilingRun, campaign.CompletedTrialReference) {
	cr := &edgev1alpha1.BatchEnergyOptimization{ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "campaign", UID: "campaign-uid"}}
	run := &edgev1alpha1.ProfilingRun{
		ObjectMeta: metav1.ObjectMeta{Namespace: "ns", Name: "run", UID: "run-uid", Generation: 2,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(cr, edgev1alpha1.GroupVersion.WithKind("BatchEnergyOptimization"))}},
		Spec: edgev1alpha1.ProfilingRunSpec{Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{Batch: 1, WorkloadMode: "saturated-serial-fixed-batch"}},
		Status: edgev1alpha1.ProfilingRunStatus{
			Evidence: &edgev1alpha1.ProfilingRunEvidenceReference{Store: "store", Key: "execution", Size: 1, SchemaVersion: "schema"},
			Conditions: []metav1.Condition{
				{Type: edgev1alpha1.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionTrue, ObservedGeneration: 2},
				{Type: edgev1alpha1.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionTrue, ObservedGeneration: 2},
			},
		},
	}
	ref := campaign.CompletedTrialReference{
		Run:         campaign.RunReference{Namespace: run.Namespace, Name: run.Name, UID: string(run.UID)},
		Measurement: campaign.ResultMeasurement{Batch: 1, WorkloadMode: campaign.WorkloadModeActive},
		Evidence:    campaign.StoredObjectReference{Store: "store", Key: "execution", Size: 1, SchemaVersion: "schema"},
	}
	return cr, run, ref
}

func TestLoadPlanSeparatesStatusAcceptanceFromIdentityBinding(t *testing.T) {
	payload, err := os.ReadFile("../../profiler/evidence/testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var active profilingrun.RunConfig
	if err := json.Unmarshal(payload, &active); err != nil {
		t.Fatal(err)
	}
	active.State.Firstfloor = &modelcompat.FirstfloorSettings{MaxBatchSize: 1, MaxQueueSize: 32}
	idle := active
	idle.Measurement = profilingrun.MeasurementContract{Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30, WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode}
	spec, err := projection.ProjectCurvePlanSpec("reader-plan", active, idle, derivation.CurveConversionContract{}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := derivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	planBytes, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	state := campaign.ResultMeasurementState{TargetNode: active.TargetNode.Name, ModelSpec: active.Candidate.Inputs.ModelSpec.Name,
		DeviceClass: active.State.DeviceClass, ExecutionPath: active.Candidate.ExecutionPath, PowerMode: active.State.PowerMode}
	planRef := campaign.StoredObjectReference{Store: "store", Key: "plan", Size: int64(len(planBytes)), SchemaVersion: plan.SchemaVersion()}
	result, err := campaign.NewResultRecord(campaign.ResultRecordSpec{Outcome: "MinimumFound", MeasurementState: state, CurvePlan: planRef, Detail: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	base := &edgev1alpha1.BatchEnergyOptimization{
		Spec: edgev1alpha1.BatchEnergyOptimizationSpec{State: edgev1alpha1.MeasurementStateSpec{
			TargetNode: state.TargetNode, Candidate: edgev1alpha1.ProfilingRunCandidateSpec{ModelSpec: state.ModelSpec, DeviceClass: state.DeviceClass, ExecutionPath: state.ExecutionPath, PowerMode: state.PowerMode}}},
		Status: edgev1alpha1.BatchEnergyOptimizationStatus{Outcome: string(result.Spec().Outcome),
			Result: &edgev1alpha1.ProfilingRunEvidenceReference{Store: "store", Key: "result", Size: 1, SchemaVersion: result.SchemaVersion()}},
	}
	reads := 0
	reader := Reader{ReadObject: func(_ context.Context, identity evidencestore.ObjectIdentity) ([]byte, error) {
		reads++
		if identity != storedObjectIdentity(planRef) {
			t.Fatalf("wrong frozen plan identity: %+v", identity)
		}
		return planBytes, nil
	}}
	for _, field := range []string{"matching", "outcome", "schema"} {
		t.Run(field, func(t *testing.T) {
			cr := base.DeepCopy()
			if field == "outcome" {
				cr.Status.Outcome = ""
			}
			if field == "schema" {
				cr.Status.Result.SchemaVersion = "different"
			}
			before := reads
			retained, err := reader.ReadPlan(context.Background(), cr, result)
			if field == "matching" {
				if err != nil || retained.PlanIdentity != storedObjectIdentity(planRef) {
					t.Fatalf("valid status rejected: %+v %v", retained, err)
				}
			} else if err == nil || err.Error() != "terminal result schema or outcome contradicts campaign status" || reads != before {
				t.Fatalf("ReadPlan changed status acceptance or read ordering: %v, reads %d -> %d", err, before, reads)
			}
			retained, err = reader.LoadPlan(context.Background(), cr, result)
			if err != nil || retained.ResultIdentity != apiObjectIdentity(cr.Status.Result) || retained.PlanIdentity != storedObjectIdentity(planRef) || !retained.Result.SemanticallyEqual(result) {
				t.Fatalf("LoadPlan applied status acceptance or lost identity: %+v %v", retained, err)
			}
			cr.Spec.State.TargetNode = "different-node"
			before = reads
			if _, err := reader.LoadPlan(context.Background(), cr, result); err == nil || reads != before {
				t.Fatalf("LoadPlan skipped measurement identity binding: %v", err)
			}
		})
	}
}
