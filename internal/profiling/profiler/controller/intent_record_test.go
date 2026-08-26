package controller

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

const (
	testSystemNamespace = "chill-system"
	testFactsUID        = "facts-uid"
)

type concurrentIntentRecordClient struct {
	client.Client
	record            *corev1.ConfigMap
	getAfterCreateErr error
	createCalls       int
}

func (c *concurrentIntentRecordClient) Get(
	ctx context.Context,
	key client.ObjectKey,
	object client.Object,
	options ...client.GetOption,
) error {
	if c.createCalls > 0 && c.getAfterCreateErr != nil {
		return c.getAfterCreateErr
	}
	return c.Client.Get(ctx, key, object, options...)
}

func (c *concurrentIntentRecordClient) Create(
	ctx context.Context,
	object client.Object,
	_ ...client.CreateOption,
) error {
	c.createCalls++
	if err := c.Client.Create(ctx, c.record.DeepCopy()); err != nil {
		return err
	}
	return apierrors.NewAlreadyExists(schema.GroupResource{Resource: "configmaps"}, object.GetName())
}

func TestIntentRecordsCreateReuseAndRestartLoad(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	frozen := testFrozenRun(t)
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}

	record, created, err := records.Ensure(ctx, run, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if !created || record.Immutable == nil || !*record.Immutable ||
		len(record.BinaryData) != 4 || len(record.Annotations) != 0 {
		t.Fatalf("created frozen intent record is incomplete: %#v", record)
	}
	if len(record.OwnerReferences) != 1 || record.OwnerReferences[0].UID != run.UID ||
		record.OwnerReferences[0].Controller == nil || !*record.OwnerReferences[0].Controller {
		t.Fatalf("frozen intent owner = %#v", record.OwnerReferences)
	}

	if _, created, err = records.Ensure(ctx, run, frozen); err != nil || created {
		t.Fatalf("second ensure should reuse exact record: created=%v err=%v", created, err)
	}
	restarted, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := restarted.Load(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	loadedSource, loadedPolicy, _ := loaded.AdmissionPolicy.Records()
	frozenSource, frozenPolicy, _ := frozen.AdmissionPolicy.Records()
	if !reflect.DeepEqual(loaded.Prepared.Intent, frozen.Prepared.Intent) ||
		!reflect.DeepEqual(loadedSource, frozenSource) ||
		!reflect.DeepEqual(loadedPolicy, frozenPolicy) {
		t.Fatal("restarted manager did not recover exact frozen inputs")
	}
	nonCanonical := record.DeepCopy()
	nonCanonical.BinaryData[intentRecordIntentKey] = append(
		[]byte(" \n"), nonCanonical.BinaryData[intentRecordIntentKey]...,
	)
	if _, err := decodeIntentRecord(run, nonCanonical); err != nil {
		t.Fatalf("structurally valid intent encoding was rejected: %v", err)
	}
}

func TestPreparationFromIntentRestoresCPUQuotaSelection(t *testing.T) {
	allocatable := testFrozenIntent(t)
	if request := preparationFromIntent(allocatable); request.CPUQuota != "" {
		t.Fatalf("allocatable policy restored CPU quota %q", request.CPUQuota)
	}

	configured := allocatable
	configured.CPU.Policy = profilingrun.ConfiguredCPUQuotaPolicy
	configured.CPU.Limit = "2"
	configured.CPU.ORTIntraOpThreads = 2
	if request := preparationFromIntent(configured); request.CPUQuota != "2" {
		t.Fatalf("configured policy restored CPU quota %q", request.CPUQuota)
	}
}

func TestIntentRecordsRetainExactActuationPolicy(t *testing.T) {
	ctx := context.Background()
	run, frozen := testActuatedRunAndFrozen(t)
	scheme := intentRecordTestScheme(t)
	records, err := NewIntentRecords(fake.NewClientBuilder().WithScheme(scheme).Build(), scheme)
	if err != nil {
		t.Fatal(err)
	}
	record, created, err := records.Ensure(ctx, run, frozen)
	if err != nil {
		t.Fatal(err)
	}
	if !created || len(record.BinaryData) != 5 || len(record.BinaryData[intentRecordActuationPolicyKey]) == 0 ||
		len(record.Annotations) != 0 {
		t.Fatalf("actuated frozen record = %#v", record)
	}
	loaded, err := records.Load(ctx, run)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Prepared.ActuationPolicy == nil || loaded.Prepared.Intent.Actuation == nil {
		t.Fatalf("loaded actuation binding = %#v", loaded.Prepared)
	}
	if err := loaded.Prepared.ValidateActuationBinding(); err != nil {
		t.Fatal(err)
	}

	delete(record.BinaryData, intentRecordActuationPolicyKey)
	if _, err := decodeIntentRecord(run, record); err == nil || !strings.Contains(err.Error(), "unexpected data keys") {
		t.Fatalf("actuated record without policy was accepted: %v", err)
	}
}

func TestIntentRecordsNeverOverwriteConflictingRecord(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	frozen := testFrozenRun(t)
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	record, _, err := records.Ensure(ctx, run, frozen)
	if err != nil {
		t.Fatal(err)
	}

	retained := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: record.Namespace, Name: record.Name}
	if err := kubeClient.Get(ctx, key, retained); err != nil {
		t.Fatal(err)
	}
	retained.BinaryData[intentRecordCandidateKey] = []byte(`{"scope":"foreign"}`)
	if err := kubeClient.Update(ctx, retained); err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.Ensure(ctx, run, frozen); err == nil {
		t.Fatal("conflicting frozen intent record was accepted")
	}
	after := &corev1.ConfigMap{}
	if err := kubeClient.Get(ctx, key, after); err != nil {
		t.Fatal(err)
	}
	var afterCandidate map[string]any
	if err := json.Unmarshal(after.BinaryData[intentRecordCandidateKey], &afterCandidate); err != nil ||
		afterCandidate["scope"] != "foreign" {
		t.Fatalf("conflicting frozen intent record was overwritten: %#v, %v", afterCandidate, err)
	}
}

func TestIntentRecordsRecoverConcurrentlyCreatedRecord(t *testing.T) {
	apiErr := errors.New("ConfigMap read temporarily unavailable")
	for _, test := range []struct {
		name       string
		mutate     func(*corev1.ConfigMap)
		getErr     error
		wantError  string
		wantAPIErr bool
	}{
		{name: "exact"},
		{
			name: "conflict",
			mutate: func(record *corev1.ConfigMap) {
				immutable := false
				record.Immutable = &immutable
			},
			wantError: "frozen intent ConfigMap is not immutable",
		},
		{
			name:       "second get failure",
			getErr:     apiErr,
			wantError:  "get concurrently created frozen intent ConfigMap",
			wantAPIErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			run := testRun()
			frozen := testFrozenRun(t)
			scheme := intentRecordTestScheme(t)
			concurrent, err := buildIntentRecord(run, frozen, scheme)
			if err != nil {
				t.Fatal(err)
			}
			concurrent.UID = "intent-record-uid"
			if test.mutate != nil {
				test.mutate(concurrent)
			}
			baseClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			kubeClient := &concurrentIntentRecordClient{
				Client: baseClient, record: concurrent, getAfterCreateErr: test.getErr,
			}
			records, err := NewIntentRecords(kubeClient, scheme)
			if err != nil {
				t.Fatal(err)
			}

			recovered, created, ensureErr := records.Ensure(ctx, run, frozen)
			if test.wantError != "" {
				if ensureErr == nil || !strings.Contains(ensureErr.Error(), test.wantError) || recovered != nil || created {
					t.Fatalf("concurrent record failure: created=%v record=%#v err=%v", created, recovered, ensureErr)
				}
				if test.wantAPIErr && !errors.Is(ensureErr, apiErr) {
					t.Fatalf("concurrent record API error = %v, want wrapped %v", ensureErr, apiErr)
				}
			} else if ensureErr != nil || created || recovered == nil || recovered.UID != concurrent.UID {
				t.Fatalf("exact concurrent record recovery: created=%v record=%#v err=%v", created, recovered, ensureErr)
			}
			if kubeClient.createCalls != 1 {
				t.Fatalf("create calls = %d, want 1", kubeClient.createCalls)
			}
		})
	}
}

func TestIntentRecordsRejectForeignSameNamedRecord(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	frozen := testFrozenRun(t)
	scheme := intentRecordTestScheme(t)
	foreign, err := buildIntentRecord(run, frozen, scheme)
	if err != nil {
		t.Fatal(err)
	}
	foreign.OwnerReferences[0].UID = "other-run"
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.Ensure(ctx, run, frozen); err == nil || !strings.Contains(err.Error(), "ownership mismatch") {
		t.Fatalf("expected foreign owner rejection, got %v", err)
	}
}

func TestIntentRecordsRejectPreparedRunSpecDrift(t *testing.T) {
	run := testRun()
	frozen := testFrozenRun(t)
	frozen.Prepared.Intent.Measurement.Batch = 2
	scheme := intentRecordTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	records, err := NewIntentRecords(kubeClient, scheme)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.Ensure(context.Background(), run, frozen); err == nil ||
		!strings.Contains(err.Error(), "does not match immutable ProfilingRun spec") {
		t.Fatalf("expected spec drift rejection, got %v", err)
	}
}

func TestIntentRecordsRejectAdmissionPolicyDrift(t *testing.T) {
	run := testRun()
	frozen := testFrozenRun(t)
	frozen.AdmissionPolicy = configuredAdmissionPolicy(
		t,
		"different-policy",
		frozen.Prepared.Intent.Measurement.WorkloadMode,
	)
	scheme := intentRecordTestScheme(t)
	records, err := NewIntentRecords(fake.NewClientBuilder().WithScheme(scheme).Build(), scheme)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := records.Ensure(context.Background(), run, frozen); err == nil ||
		!strings.Contains(err.Error(), "does not match immutable ProfilingRun spec") {
		t.Fatalf("expected policy drift rejection, got %v", err)
	}
}

func TestIntentRecordsRejectIncompletePayloadSets(t *testing.T) {
	run := testRun()
	frozen := testFrozenRun(t)
	scheme := intentRecordTestScheme(t)

	for _, keys := range [][]string{
		{intentRecordAdmissionPolicyKey, intentRecordExecutionPolicyKey},
		{intentRecordExecutionPolicyKey},
	} {
		record, err := buildIntentRecord(run, frozen, scheme)
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range keys {
			delete(record.BinaryData, key)
		}
		if _, err := decodeIntentRecord(run, record); err == nil ||
			!strings.Contains(err.Error(), "unexpected data keys") {
			t.Fatalf("payload set without %v was accepted: %v", keys, err)
		}
	}
}

func TestIntentRecordNameIsStableAndRunUIDScoped(t *testing.T) {
	run := testRun()
	first, err := intentRecordName(run)
	if err != nil {
		t.Fatal(err)
	}
	second, err := intentRecordName(run.DeepCopy())
	if err != nil {
		t.Fatal(err)
	}
	other := run.DeepCopy()
	other.UID = "other-run-uid"
	third, err := intentRecordName(other)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first == third || len(first) > 63 {
		t.Fatalf("unexpected deterministic names: %q %q %q", first, second, third)
	}
}

func testPreparedRun(t *testing.T) profiler.PreparedRun {
	t.Helper()
	intent := testFrozenIntent(t)
	candidate, err := os.ReadFile("testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	return profiler.PreparedRun{Intent: intent, CandidateReportBytes: candidate}
}

func testActuatedRunAndFrozen(t *testing.T) (*edgev1alpha1.ProfilingRun, FrozenRun) {
	t.Helper()
	run := testRun()
	run.Spec.Candidate.PowerMode = testSelectablePowerMode
	prepared := testPreparedRun(t)
	prepared.Intent.SchemaVersion = profilingrun.SchemaVersion
	prepared.Intent.State.PowerMode = run.Spec.Candidate.PowerMode
	target := profilingrun.ActuatorTarget{
		Kind:           profilingrun.ActuatorKindJetsonNVPModel,
		JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 1},
	}
	selection := profiler.ActuationSelection{
		DeviceClass: prepared.Intent.State.DeviceClass,
		PowerMode:   prepared.Intent.State.PowerMode,
		Actuator:    target,
	}
	policySpec := profiler.ActuationPolicySpec{
		Name: "lattepanda-15w-v1", DeviceClass: selection.DeviceClass,
		PowerMode: selection.PowerMode, ActuatorKind: selection.Actuator.Kind,
		SettlingDurationSeconds: 30, MaxActuationDurationSeconds: 300,
	}
	policy, err := profiler.FreezeConfiguredActuationPolicy(
		actuationPolicyCatalogForSpecs(t, policySpec),
		selection,
		profiler.ActuationPolicySource{
			Kind: profiler.ConfigMapActuationPolicySourceKind, Namespace: testSystemNamespace,
			Name: "chill-profiler-facts", UID: testFactsUID, ResourceVersion: "42",
			Key: "actuation-policies.yaml",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Intent.Actuation = &profilingrun.ActuationContract{
		PowerMode: selection.PowerMode, Actuator: target, PolicyName: policy.Policy.Name,
	}
	report, err := profilingrun.DecodeCandidateReport(prepared.CandidateReportBytes)
	if err != nil {
		t.Fatal(err)
	}
	report.SchemaVersion = profilingrun.CandidateReportSchemaVersion
	report.Selection.PowerMode = selection.PowerMode
	report.Selection.Actuator = &target
	report.DeviceClassFacts.PowerModes = []string{profilingrun.FixedPowerMode, selection.PowerMode}
	prepared.CandidateReportBytes, err = json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Intent.Candidate.Inputs = report.Inputs
	prepared.ActuationPolicy = &policy
	return run, frozenRunForPrepared(t, prepared)
}

func actuationPolicyCatalogForSpecs(t *testing.T, specs ...profiler.ActuationPolicySpec) []byte {
	t.Helper()
	raw, err := yaml.Marshal(profiler.ActuationPolicyCatalog{
		SchemaVersion: profiler.ActuationPolicyCatalogSchemaVersion,
		Policies:      specs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func testFrozenRun(t *testing.T) FrozenRun {
	t.Helper()
	return frozenRunForPrepared(t, testPreparedRun(t))
}

func intentRecordTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}
