package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const (
	prejobActiveBucket     = "active-bucket"
	prejobResultBucket     = "result-bucket"
	prejobActiveBodyError  = "active-body-error"
	prejobResultBodyError  = "result-body-error"
	prejobActiveMissingKey = "active-missing-key"
	prejobResultMissingKey = "result-missing-key"
)

type prejobPorts struct {
	*frozenWorkloadPreparationPorts
	err error
}

func (p *prejobPorts) Resolve(ctx context.Context, config profilingrun.RunConfig) (profiler.FrozenExecutionPolicy, error) {
	if p.err != nil {
		return profiler.FrozenExecutionPolicy{}, p.err
	}
	policy, err := p.frozenWorkloadPreparationPorts.Resolve(ctx, config)
	// This fixture's recorded plan and reselected policy describe the same queue.
	policy.Policy.MaxInputBytes = p.config.Measurement.OperatingPoints.QueueBytesCap
	policy.Policy.AttemptTimeoutOverheadSeconds = 1235
	policy.Policy.ActiveDeadlineOverheadSeconds = 1265
	policy.Policy.ObserverReadyTimeoutSeconds = 1160
	policy.Policy.ObserverStartOverheadSeconds = 1160
	return policy, err
}

type prejobReadStore struct {
	*fakeCurvePlanStore
	err             error
	failureLocation evidencestore.ObjectLocation
	failureBody     io.ReadCloser
	failureError    error
	closed          bool
}

func (s *prejobReadStore) OpenLocation(ctx context.Context, location evidencestore.ObjectLocation) (io.ReadCloser, error) {
	if location == s.failureLocation {
		return s.failureBody, s.failureError
	}
	if s.err != nil {
		return nil, s.err
	}
	return s.fakeCurvePlanStore.OpenLocation(ctx, location)
}

func TestInspectPrejobFailureUsesActualFrozenChildPreparation(t *testing.T) {
	for _, duration := range []int{2027, 1792, 1970} {
		for _, defect := range []string{"", "job", "foreign-owner", "api", "store", "unknown", "no-bucket", "result", "missing-plan", "malformed-plan", "stale-idle", "invalid-idle", "invalid-active", "started", "node-uid", "policy-invalid", "active-evidence", prejobActiveBucket, prejobResultBucket, prejobActiveBodyError, prejobResultBodyError, prejobActiveMissingKey, prejobResultMissingKey} {
			t.Run(fmt.Sprintf("%d/%s", duration, defect), func(t *testing.T) {
				ctx := context.Background()
				parent, _, active, store, objects, ports, _ := newPrejobFixture(t, duration, defect)
				scheme := runtime.NewScheme()
				for _, add := range []func(*runtime.Scheme) error{edge.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme} {
					if add(scheme) != nil {
						t.Fatal("scheme")
					}
				}
				native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
				var reader client.Reader = native
				if defect == "api" {
					reader = prejobFailReader{Reader: native}
				}
				preparer, err := profiler.NewPreparer(ports, ports, ports, ports, ports, ports, ports.ResolveLoadOrigin)
				if err != nil {
					t.Fatal(err)
				}
				plans, err := NewCurvePlanSource(reader, ports, preparer, store, CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 64 << 20})
				if err != nil {
					t.Fatal(err)
				}
				resolver, err := profilercontroller.NewRunConfigResolver(preparer, ports, reader, plans.ReadRecorded)
				if err != nil {
					t.Fatal(err)
				}
				retained, err := evidencekube.NewRetainedLoader(reader, func(storekube.Binding, storekube.Credentials) (evidencestore.LocationReader, error) {
					return store, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				before, _ := json.Marshal(objects)
				storedBefore := map[evidencestore.ObjectLocation][]byte{}
				for k, v := range store.objects {
					storedBefore[k] = append([]byte(nil), v...)
				}
				expectEligible := defect == "" || defect == prejobActiveMissingKey || defect == prejobResultMissingKey
				if expectEligible {
					_, resolutionErr := resolver.Resolve(ctx, active)
					var capacity *profiler.MetricCapacityError
					reason, terminal := profiler.TerminalResolutionReason(resolutionErr)
					if !terminal || reason != profiler.ResolutionReasonExecutionPolicyInvalid || !errors.As(resolutionErr, &capacity) {
						t.Fatalf("active child did not reach metric capacity admission: reason=%s terminal=%t error=%T: %v",
							reason, terminal, resolutionErr, resolutionErr)
					}
				}
				proof, err := InspectPrejobFailure(ctx, reader, store, plans, resolver, func(ctx context.Context, run *edge.ProfilingRun, identity evidencestore.ObjectIdentity) (profilerevidence.VerifiedIdleEvidence, error) {
					verified, err := retained.LoadIdle(ctx, run, identity)
					return verified.ForDerivation(), err
				}, "evidence", client.ObjectKeyFromObject(parent), parent.UID)
				if expectEligible {
					if err != nil || !proof.Eligible || proof.Reason != profiler.ResolutionReasonExecutionPolicyInvalid || len(proof.IdleRuns) != 3 || proof.Child.UID != active.UID {
						t.Fatalf("actual eligibility: %+v, %v", proof, err)
					}
				} else if err == nil || proof.Eligible {
					t.Fatalf("accepted %s: %+v", defect, proof)
				}
				assertPrejobAbsenceBoundary(t, defect, store, proof)
				after, _ := json.Marshal(objects)
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(storedBefore, store.objects) || store.puts != 0 {
					t.Fatal("inspection mutated authority or evidence")
				}
			})
		}
	}
}

type prejobFailReader struct{ client.Reader }

func (prejobFailReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("sensitive API failure")
}

func newPrejobFixture(t *testing.T, duration int, defect string) (*edge.BatchEnergyOptimization, *corev1.Node, *edge.ProfilingRun, *prejobReadStore, []client.Object, *prejobPorts, evidencestore.ObjectLocation) {
	t.Helper()
	config := curveFixtureRunConfig(t)
	config.State.PowerMode = profilingrun.FixedPowerMode
	config.Metrics.DurationSeconds = duration
	idle := config
	idle.LoadOriginNode, idle.LoadOriginGatewayPod = "", nil
	idle.Measurement = profilingrun.MeasurementContract{Batch: 1, SettlingDurationSeconds: 60, DurationSeconds: 30,
		WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode, StartupWarmupIterations: config.Measurement.StartupWarmupIterations, WarmupBatchSizes: config.Measurement.WarmupBatchSizes}
	idle.Metrics.DurationSeconds = 31
	spec, err := projection.ProjectCurvePlanSpec("prejob", config, idle, derivation.CurveConversionContract{}, []int{1})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := derivation.NewCurvePlan(spec)
	if err != nil {
		t.Fatal(err)
	}
	parent := testParent()
	parent.Name, parent.UID, parent.Generation, parent.ResourceVersion = "prejob", "prejob-uid", 1, "10"
	parent.Spec.State = edge.MeasurementStateSpec{TargetNode: config.TargetNode.Name, Candidate: edge.ProfilingRunCandidateSpec{
		ModelSpec: config.State.Model, DeviceClass: config.State.DeviceClass, ExecutionPath: config.Candidate.ExecutionPath,
		PowerMode: config.State.PowerMode, Preprocessor: config.State.Preprocessor.Name}}
	parent.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}
	parent.Spec.TrialsPerBatch = 3
	parent.Status = edge.BatchEnergyOptimizationStatus{ObservedGeneration: 1, Phase: "Measuring", CompletedTrials: 3,
		Conditions: []metav1.Condition{{Type: batchEnergyOptimizationConditionDone, ObservedGeneration: 1, Status: metav1.ConditionUnknown, Reason: "TrialInFlight"}}}
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: config.TargetNode.Name, UID: types.UID(config.TargetNode.UID), ResourceVersion: "11"},
		Spec: corev1.NodeSpec{Taints: []corev1.Taint{{Key: profilingrun.MeasurementTaintKey, Value: string(parent.UID), Effect: corev1.TaintEffectNoSchedule}}}}
	active, err := BuildRun(parent, campaign.Measurement{WorkloadMode: campaign.WorkloadModeActive, Batch: 1}, 1, config.State.Backend)
	if err != nil {
		t.Fatal(err)
	}
	applyFrozenRunProtocol(active, plan, campaign.WorkloadModeActive)
	active.UID, active.Generation, active.ResourceVersion = "active-uid", 1, "12"
	store := &prejobReadStore{fakeCurvePlanStore: newFakeCurvePlanStore()}
	planBody, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	planLocation := evidencestore.ObjectLocation{Store: "evidence", Key: "batch-energy-optimizations/prejob-uid/curve-plan.json"}
	store.objects[planLocation] = planBody
	objects := []client.Object{parent, node, active, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: "credentials"}, Data: map[string][]byte{"access-key": []byte("test-access"), "secret-key": []byte("test-secret")}}}
	for attempt := 1; attempt <= 3; attempt++ {
		run, err := BuildRun(parent, campaign.Measurement{WorkloadMode: campaign.WorkloadModeIdle, Batch: 1}, attempt, config.State.Backend)
		if err != nil {
			t.Fatal(err)
		}
		applyFrozenRunProtocol(run, plan, campaign.WorkloadModeIdle)
		run.UID, run.Generation, run.ResourceVersion = types.UID(fmt.Sprintf("idle-%d", attempt)), 1, fmt.Sprint(20+attempt)
		verified := curveFixtureIdleEvidence(t, idle, fmt.Sprintf("idle-pod-%d", attempt))
		execution, raw, err := verified.Records()
		if err != nil {
			t.Fatal(err)
		}
		rawBody, err := raw.CanonicalJSON()
		if err != nil {
			t.Fatal(err)
		}
		executionBody, err := execution.CanonicalIdleJSON(raw)
		if err != nil {
			t.Fatal(err)
		}
		executionKey := fmt.Sprintf("idle-%d.json", attempt)
		store.objects[execution.RawMeasurement.Location()] = rawBody
		store.objects[evidencestore.ObjectLocation{Store: "evidence", Key: executionKey}] = executionBody
		run.Status = edge.ProfilingRunStatus{ObservedGeneration: 1,
			Job:               &edge.ProfilingRunObjectReference{Name: execution.Terminal.Job.Name, UID: execution.Terminal.Job.UID},
			ObservedExecution: &edge.ProfilingRunObservedExecutionStatus{Pod: edge.ProfilingRunObjectReference{Name: execution.Terminal.Pod.Name, UID: execution.Terminal.Pod.UID}, Node: edge.ProfilingRunObjectReference{Name: node.Name, UID: string(node.UID)}},
			Evidence:          &edge.ProfilingRunEvidenceReference{Store: "evidence", Key: executionKey, Size: int64(len(executionBody)), SchemaVersion: execution.SchemaVersion},
			EvidenceStore:     &edge.ProfilingRunEvidenceStoreBinding{Store: "evidence", Endpoint: "http://store:9000", Bucket: "profiling", Region: "us-east-1", CredentialsSecretName: "credentials"},
			Conditions:        []metav1.Condition{{Type: edge.ProfilingRunConditionExecutionComplete, Status: metav1.ConditionTrue, ObservedGeneration: 1}, {Type: edge.ProfilingRunConditionEvidenceVerified, Status: metav1.ConditionTrue, ObservedGeneration: 1}}}
		if defect == "stale-idle" && attempt == 1 {
			run.Status.ObservedGeneration = 0
		}
		if defect == "invalid-idle" && attempt == 1 {
			metav1.SetMetaDataAnnotation(&run.ObjectMeta, profilingrun.InvalidAnnotation, "")
		}
		objects = append(objects, run)
	}
	report, err := os.ReadFile("../../profiler/testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	ports := &prejobPorts{frozenWorkloadPreparationPorts: &frozenWorkloadPreparationPorts{config: config, report: report}}
	switch defect {
	case "job":
		name, err := profilercontroller.ExecutionJobName(active)
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: parent.Namespace, Name: name, UID: "job", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(active, edge.GroupVersion.WithKind("ProfilingRun"))}}})
	case "foreign-owner":
		active.OwnerReferences[0].UID = "foreign"
	case "store":
		store.err = errors.New("sensitive store failure")
	case "unknown":
		ports.err = errors.New("unknown policy failure")
	case "policy-invalid":
		ports.err = profiler.InvalidExecutionPolicy("selected policy unavailable")
	case "active-evidence":
		store.objects[evidencestore.ObjectLocation{Store: "evidence", Key: profilercontroller.ExecutionEvidenceObjectKeys(active.UID)[0]}] = []byte("retained")
	case "no-bucket":
		store.err = &smithy.GenericAPIError{Code: "NoSuchBucket", Message: "sensitive bucket"}
	case "result":
		store.objects[evidencestore.ObjectLocation{Store: "evidence", Key: "batch-energy-optimizations/prejob-uid/result.json"}] = []byte("published")
	case "missing-plan":
		delete(store.objects, planLocation)
	case "malformed-plan":
		store.objects[planLocation] = []byte("{}")
	case "started":
		active.Status.Job = &edge.ProfilingRunObjectReference{Name: "old", UID: "old"}
	case "node-uid":
		node.UID = "replacement"
	case "invalid-active":
		metav1.SetMetaDataAnnotation(&active.ObjectMeta, profilingrun.InvalidAnnotation, "")
	case prejobActiveBucket, prejobActiveBodyError, prejobActiveMissingKey, prejobResultBucket, prejobResultBodyError, prejobResultMissingKey:
		setPrejobAbsenceReply(store, active, defect)
	}
	return parent, node, active, store, objects, ports, planLocation
}

type prejobContradictoryBody struct{ closed *bool }

func (b prejobContradictoryBody) Read([]byte) (int, error) { return 0, io.EOF }
func (b prejobContradictoryBody) Close() error             { *b.closed = true; return nil }

func setPrejobAbsenceReply(store *prejobReadStore, active *edge.ProfilingRun, defect string) {
	key := profilercontroller.ExecutionEvidenceObjectKeys(active.UID)[0]
	switch defect {
	case prejobResultBucket, prejobResultBodyError, prejobResultMissingKey:
		key = "batch-energy-optimizations/prejob-uid/result.json"
	}
	code := "NoSuchKey"
	if defect == prejobActiveBucket || defect == prejobResultBucket {
		code = "NoSuchBucket"
	}
	store.failureLocation = evidencestore.ObjectLocation{Store: "evidence", Key: key}
	store.failureError = fmt.Errorf("read object: %w: %w", evidencestore.ErrNotFound, &smithy.GenericAPIError{Code: code})
	if defect == prejobActiveBodyError || defect == prejobResultBodyError {
		store.failureBody = prejobContradictoryBody{closed: &store.closed}
	}
}

func assertPrejobAbsenceBoundary(t *testing.T, defect string, store *prejobReadStore, proof PrejobFailureInspection) {
	t.Helper()
	switch defect {
	case prejobActiveBucket, prejobActiveBodyError, prejobResultBucket, prejobResultBodyError:
		if !proof.PlanFound || len(proof.IdleRuns) != 3 || proof.PublishedResultAbsent {
			t.Fatalf("absence failure bypassed the retained evidence boundary: %+v", proof)
		}
	}
	if (defect == prejobActiveBodyError || defect == prejobResultBodyError) && !store.closed {
		t.Fatal("contradictory response body was not closed")
	}
}
