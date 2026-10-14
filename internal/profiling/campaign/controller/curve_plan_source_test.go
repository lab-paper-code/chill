package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCurvePlanSourceRecordsAndRecoversFrozenPlan(t *testing.T) {
	const storeName = "evidence"

	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	reportBytes, report := curvePlanCandidateReport(t, parent)
	modelReader := &fakeCurvePlanModelReader{model: curvePlanModel(report)}
	resolver := &fakeCurvePlanCandidateResolver{reportBytes: reportBytes}
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid",
	}
	store := newFakeCurvePlanStore()
	source, err := NewCurvePlanSource(
		modelReader, resolver, preparer, store,
		CurvePlanSourceOptions{Store: storeName, MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatalf("construct curve-plan source: %v", err)
	}

	first, _, transition, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatalf("record curve plan: %v", err)
	}
	if transition != nil {
		t.Fatalf("fixed campaign projected transition material: %+v", transition)
	}
	wantBatches := []int{1, 2, 4}
	shared := first.Shared()
	if !slices.Equal(shared.PlannedBatchSizes, wantBatches) ||
		shared.Conversion.Tool != "onnx-tool" || shared.Conversion.ToolVersion != "1.2.3" ||
		shared.Conversion.TargetPlatform != "cpu" {
		t.Fatalf("recorded curve plan = %+v", shared)
	}
	wantKey := "batch-energy-optimizations/parent-uid/curve-plan.json"
	if store.puts != 1 || store.opens != 1 || store.lastIdentity.Key != wantKey {
		t.Fatalf("first record calls = puts %d, opens %d, identity %+v", store.puts, store.opens, store.lastIdentity)
	}
	storedPayload := store.objects[store.lastIdentity.Location()]
	if len(preparer.requests) != 2 || preparer.requests[0].Measurement.Batch != 1 ||
		preparer.requests[0].Measurement.WorkloadMode != profilingrun.SaturatedSerialFixedBatchWorkloadMode ||
		preparer.requests[1].Measurement.Batch != 1 ||
		preparer.requests[1].Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		t.Fatalf("projected preparation requests = %+v", preparer.requests)
	}

	// Recovery returns the stored plan and its observed size without
	// projecting current facts, even when the live projector is unavailable
	// and unrelated facts changed.
	storedPayload = append([]byte(" \n"), storedPayload...)
	store.objects[store.lastIdentity.Location()] = storedPayload
	modelReader.err = errors.New("model reader unavailable")
	modelReader.runs = []edgev1alpha1.ProfilingRun{{ObjectMeta: ownedByCampaign(parent)}}
	preparer.nodeUID = "replacement-node-uid"
	recovered, recoveredReference, recoveredTransition, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatalf("recover existing curve plan: %v", err)
	}
	if recoveredTransition != nil || !reflect.DeepEqual(recovered.Shared(), first.Shared()) ||
		store.puts != 1 || store.opens != 2 || len(preparer.requests) != 2 ||
		recoveredReference.Store != storeName || recoveredReference.Key != wantKey ||
		recoveredReference.SchemaVersion != first.SchemaVersion() ||
		recoveredReference.Size != int64(len(storedPayload)) {
		t.Fatalf("recovery = reference %+v, puts %d, opens %d, prepares %d",
			recoveredReference, store.puts, store.opens, len(preparer.requests))
	}
}

func TestCurvePlanSourceRecoversTransitionFromOwnedNodeActuation(t *testing.T) {
	parent := testParent()
	reportBytes, report := curvePlanCandidateReport(t, parent)
	modelReader := &fakeCurvePlanModelReader{model: curvePlanModel(report)}
	preparer := &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"}
	store := newFakeCurvePlanStore()
	plan, _, err := (&CurvePlanProjector{
		reader:   modelReader,
		resolver: &fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer: preparer,
	}).Project(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store.objects[evidencestore.ObjectLocation{
		Store: "evidence", Key: "batch-energy-optimizations/parent-uid/curve-plan.json",
	}] = payload
	source, err := NewCurvePlanSource(
		modelReader, &fakeCurvePlanCandidateResolver{reportBytes: reportBytes}, preparer, store,
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	prepared := len(preparer.requests)
	modelReader.err = errors.New("model reader unavailable")
	modelReader.transitions = []edgev1alpha1.NodeActuation{{ObjectMeta: ownedByCampaign(parent), Spec: edgev1alpha1.NodeActuationSpec{
		Node:        edgev1alpha1.ProfilingRunObjectReference{Name: parent.Spec.State.TargetNode, UID: "node-uid"},
		DeviceClass: parent.Spec.State.Candidate.DeviceClass, PowerMode: parent.Spec.State.Candidate.PowerMode,
		PolicyName: "policy", SettlingDurationSeconds: 5, MaxActuationDurationSeconds: 60,
	}}}

	_, _, transition, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	if transition == nil || transition.Node.UID != plan.Shared().NodeUID || transition.PolicyName != "policy" ||
		transition.SettlingDurationSeconds != 5 || transition.MaxActuationDurationSeconds != 60 ||
		len(preparer.requests) != prepared || store.puts != 0 {
		t.Fatalf("recovered transition = %+v, puts %d", transition, store.puts)
	}
}

func ownedByCampaign(parent *edgev1alpha1.BatchEnergyOptimization) metav1.ObjectMeta {
	return metav1.ObjectMeta{OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(
		parent, edgev1alpha1.GroupVersion.WithKind("BatchEnergyOptimization"),
	)}}
}

func TestCurvePlanSourceRecoveryRequiresOwnershipAndFrozenNode(t *testing.T) {
	parent := testParent()
	reportBytes, report := curvePlanCandidateReport(t, parent)
	modelReader := &fakeCurvePlanModelReader{model: curvePlanModel(report)}
	preparer := &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"}
	store := newFakeCurvePlanStore()
	plan, _, err := (&CurvePlanProjector{
		reader:   modelReader,
		resolver: &fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer: preparer,
	}).Project(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := plan.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	store.objects[evidencestore.ObjectLocation{
		Store: "evidence", Key: "batch-energy-optimizations/parent-uid/curve-plan.json",
	}] = payload
	source, err := NewCurvePlanSource(
		modelReader, &fakeCurvePlanCandidateResolver{reportBytes: reportBytes}, preparer, store,
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}
	foreign := parent.DeepCopy()
	foreign.UID = "stale-parent-uid"
	transitionSpec := func(nodeUID string) edgev1alpha1.NodeActuationSpec {
		return edgev1alpha1.NodeActuationSpec{
			Node:        edgev1alpha1.ProfilingRunObjectReference{Name: parent.Spec.State.TargetNode, UID: nodeUID},
			DeviceClass: parent.Spec.State.Candidate.DeviceClass, PowerMode: parent.Spec.State.Candidate.PowerMode,
			PolicyName: "policy", SettlingDurationSeconds: 5, MaxActuationDurationSeconds: 60,
		}
	}

	tests := []struct {
		name        string
		transitions []edgev1alpha1.NodeActuation
		runs        []edgev1alpha1.ProfilingRun
		want        string
	}{
		{
			name:        "unowned transition",
			transitions: []edgev1alpha1.NodeActuation{{Spec: transitionSpec(preparer.nodeUID)}},
			want:        "NodeActuation",
		},
		{
			name:        "transition owned by stale parent UID",
			transitions: []edgev1alpha1.NodeActuation{{ObjectMeta: ownedByCampaign(foreign), Spec: transitionSpec(preparer.nodeUID)}},
			want:        "NodeActuation",
		},
		{
			name: "stale predecessor Run",
			runs: []edgev1alpha1.ProfilingRun{{ObjectMeta: ownedByCampaign(foreign)}},
			want: "ProfilingRun",
		},
		{
			name:        "owned transition Node UID drift",
			transitions: []edgev1alpha1.NodeActuation{{ObjectMeta: ownedByCampaign(parent), Spec: transitionSpec("other-node-uid")}},
			want:        "does not match the frozen curve plan",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			modelReader.transitions, modelReader.runs = test.transitions, test.runs
			if _, _, _, err := source.Resolve(context.Background(), parent); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
		})
	}

	// Before any child exists, only the active transition is prepared for the
	// frozen plan's first batch; the model is not read and no plan is rewritten.
	modelReader.transitions, modelReader.runs = nil, nil
	modelReader.err = errors.New("model reader unavailable")
	prepared := len(preparer.requests)
	if _, _, _, err := source.Resolve(context.Background(), parent); err != nil {
		t.Fatalf("resolve before children exist: %v", err)
	}
	if len(preparer.requests) != prepared+1 ||
		preparer.requests[prepared].Measurement.WorkloadMode != profilingrun.SaturatedSerialFixedBatchWorkloadMode ||
		preparer.requests[prepared].Measurement.Batch != plan.PlannedBatchSizes()[0] || store.puts != 0 {
		t.Fatalf("pre-child preparation = %+v, puts %d", preparer.requests[prepared:], store.puts)
	}
}

func TestBindTransitionToPlanRejectsNodeUIDMismatch(t *testing.T) {
	parent := testParent()
	reportBytes, report := curvePlanCandidateReport(t, parent)
	plan, _, err := (&CurvePlanProjector{
		reader:   &fakeCurvePlanModelReader{model: curvePlanModel(report)},
		resolver: &fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer: &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"},
	}).Project(context.Background(), parent)
	if err != nil {
		t.Fatal(err)
	}
	material := &TransitionMaterial{Node: edgev1alpha1.ProfilingRunObjectReference{Name: "node", UID: "other-node-uid"}}
	if _, err := bindTransitionToPlan(plan, material); err == nil {
		t.Fatal("transition for a different Node UID was accepted")
	}
	material.Node.UID = "node-uid"
	if got, err := bindTransitionToPlan(plan, material); err != nil || got != material {
		t.Fatalf("matching transition = %v, %v", got, err)
	}
}

func TestCurvePlanSourceRejectsInvalidStoredPlanWithoutProjecting(t *testing.T) {
	parent := testParent()
	reportBytes, report := curvePlanCandidateReport(t, parent)
	preparer := &fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"}
	store := newFakeCurvePlanStore()
	store.objects[evidencestore.ObjectLocation{
		Store: "evidence", Key: "batch-energy-optimizations/parent-uid/curve-plan.json",
	}] = []byte(`{"schemaVersion":"unknown","shared":{}}`)
	source, err := NewCurvePlanSource(
		&fakeCurvePlanModelReader{model: curvePlanModel(report)},
		&fakeCurvePlanCandidateResolver{reportBytes: reportBytes}, preparer, store,
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, _, _, err = source.Resolve(context.Background(), parent)
	if err == nil || errors.Is(err, evidencestore.ErrNotFound) ||
		!strings.Contains(err.Error(), "decode existing curve plan") {
		t.Fatalf("invalid stored plan error = %v", err)
	}
	if store.puts != 0 || len(preparer.requests) != 0 {
		t.Fatalf("invalid stored plan wrote %d objects and prepared %d Runs", store.puts, len(preparer.requests))
	}
}

func TestCurvePlanSourceUsesCanonicalArtifactDeclaredInputAndCampaignBatchSizes(t *testing.T) {
	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	parent.Spec.BatchSizes = []int32{1, 2, 4, 8, 16, 32}
	_, report := curvePlanCandidateReport(t, parent)
	report.Selection.Artifact = "canonical-onnx"
	report.ArtifactSupply.Path = "/artifact/canonical.onnx"
	report.ArtifactCapability = &profilingrun.CandidateArtifactCapability{
		Tensors: []modelcompat.InputTensor{
			{Name: "canonical-input", Datatype: "FP32", SampleShape: []int32{3, 192, 256}},
		},
	}
	reportBytes := encodeCurvePlanCandidateReport(t, report)
	model := curvePlanModel(report)
	model.Spec.Artifacts[0].Derivation = nil
	model.Spec.Artifacts[0].Input = &edgev1alpha1.ModelArtifactInputContract{
		Tensors: []edgev1alpha1.ModelInputTensor{
			{Name: "canonical-input", Datatype: "FP32", SampleShape: []int32{3, 192, 256}},
		},
	}
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid",
	}
	source, err := NewCurvePlanSource(
		&fakeCurvePlanModelReader{model: model},
		&fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer,
		newFakeCurvePlanStore(),
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatalf("construct curve-plan source: %v", err)
	}

	plan, _, _, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatalf("resolve canonical-artifact curve plan: %v", err)
	}
	shared := plan.Shared()
	if !slices.Equal(shared.PlannedBatchSizes, []int{1, 2, 4, 8, 16, 32}) {
		t.Fatalf("planned batch sizes = %v", shared.PlannedBatchSizes)
	}
	if shared.Conversion.TargetPlatform != "" || shared.Conversion.Tool != "" ||
		shared.Conversion.ToolVersion != "" {
		t.Fatalf("canonical artifact conversion contract = %+v", shared.Conversion)
	}
	if !reflect.DeepEqual(shared.Tensor.Tensors, report.ArtifactCapability.Tensors) {
		t.Fatalf("canonical artifact tensor contract = %+v", shared.Tensor)
	}
	if len(preparer.requests) != 2 || preparer.requests[0].Measurement.Batch != 1 ||
		preparer.requests[1].Measurement.Batch != 1 {
		t.Fatalf("canonical artifact preparation requests = %+v", preparer.requests)
	}
}

func TestCurvePlanSourceRejectsArtifactWithoutDeclaredInputContract(t *testing.T) {
	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	parent.Spec.BatchSizes = []int32{1, 2}
	_, report := curvePlanCandidateReport(t, parent)
	report.Selection.Artifact = "undeclared-input"
	report.ArtifactCapability.DeclaredBatchSizes = nil
	report.ArtifactCapability.TargetPlatform = ""
	reportBytes := encodeCurvePlanCandidateReport(t, report)
	model := curvePlanModel(report)
	model.Spec.Artifacts[0].Derivation = nil
	model.Spec.Artifacts[0].Input = nil
	source, err := NewCurvePlanSource(
		&fakeCurvePlanModelReader{model: model},
		&fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		&fakeCurvePlanPreparer{report: report, reportBytes: reportBytes, nodeUID: "node-uid"},
		newFakeCurvePlanStore(),
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatalf("construct curve-plan source: %v", err)
	}

	_, _, _, err = source.Resolve(context.Background(), parent)
	var missing *ArtifactInputContractMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("Resolve() error = %v, want ArtifactInputContractMissingError", err)
	}
	if missing.Artifact != "undeclared-input" {
		t.Fatalf("missing input contract error = %v, artifact = %q", err, missing.Artifact)
	}
}

func TestCurvePlanSourceRejectsCampaignBatchSizesForDerivedArtifact(t *testing.T) {
	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	parent.Spec.BatchSizes = []int32{1, 2, 4}
	reportBytes, report := curvePlanCandidateReport(t, parent)
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid",
	}
	source, err := NewCurvePlanSource(
		&fakeCurvePlanModelReader{model: curvePlanModel(report)},
		&fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
		preparer,
		newFakeCurvePlanStore(),
		CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
	)
	if err != nil {
		t.Fatalf("construct curve-plan source: %v", err)
	}

	if _, _, _, err := source.Resolve(context.Background(), parent); err == nil ||
		!strings.Contains(err.Error(), "must be absent when the selected artifact declares batch sizes") {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(preparer.requests) != 0 {
		t.Fatalf("conflicting sources prepared Runs: %+v", preparer.requests)
	}
}

func TestCurvePlanProjectorPreservesPhaseOrderAndErrorIdentity(t *testing.T) {
	parent := testParent()
	parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
	reportBytes, report := curvePlanCandidateReport(t, parent)
	calls := []string{}
	modelReader := &fakeCurvePlanModelReader{model: curvePlanModel(report), calls: &calls}
	resolver := &fakeCurvePlanCandidateResolver{reportBytes: reportBytes, calls: &calls}
	preparer := &fakeCurvePlanPreparer{
		report: report, reportBytes: reportBytes, nodeUID: "node-uid", calls: &calls,
	}
	projector, err := NewCurvePlanProjector(modelReader, resolver, preparer)
	if err != nil {
		t.Fatal(err)
	}

	if _, _, err := projector.Project(context.Background(), parent); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{
		"resolve-candidate",
		"prepare:" + profilingrun.SaturatedSerialFixedBatchWorkloadMode,
		"prepare:" + profilingrun.IdleModelLoadedWorkloadMode,
		"read-model",
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("Project() calls = %v, want %v", calls, wantCalls)
	}

	calls = nil
	modelErr := errors.New("model read failed")
	modelReader.err = modelErr
	_, _, err = projector.Project(context.Background(), parent)
	wantError := fmt.Sprintf(
		"get ModelSpec %q for curve-plan artifact: %s", parent.Spec.State.Candidate.ModelSpec, modelErr,
	)
	if !errors.Is(err, modelErr) || err.Error() != wantError {
		t.Fatalf("Project() error = %v, want %q wrapping %q", err, wantError, modelErr)
	}
	if !slices.Equal(calls, wantCalls) {
		t.Fatalf("failed Project() calls = %v, want %v", calls, wantCalls)
	}

	calls = nil
	_, _, err = projector.Project(context.Background(), nil)
	if err == nil || err.Error() != "a parent BatchEnergyOptimization is required" {
		t.Fatalf("Project(nil) error = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("Project(nil) crossed resolver boundary: %v", calls)
	}
}

func TestCurvePlanSourceRejectsUnsupportedArtifactBoundaries(t *testing.T) {
	tests := []struct {
		name             string
		mutateReport     func(*profilingrun.CandidateReport)
		mutateModel      func(*edgev1alpha1.ModelSpec)
		preparedArtifact string
		want             string
	}{
		{
			name:             "prepared artifact disagrees with report",
			preparedArtifact: "different-artifact",
			want:             "prepared active RunConfig artifact disagrees",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := testParent()
			parent.Spec.State.Candidate.PowerMode = profilingrun.FixedPowerMode
			_, report := curvePlanCandidateReport(t, parent)
			model := curvePlanModel(report)
			if test.mutateReport != nil {
				test.mutateReport(&report)
			}
			if test.mutateModel != nil {
				test.mutateModel(model)
			}
			reportBytes := encodeCurvePlanCandidateReport(t, report)
			preparer := &fakeCurvePlanPreparer{
				report: report, reportBytes: reportBytes, nodeUID: "node-uid",
				preparedArtifact: test.preparedArtifact,
			}
			source, err := NewCurvePlanSource(
				&fakeCurvePlanModelReader{model: model},
				&fakeCurvePlanCandidateResolver{reportBytes: reportBytes},
				preparer,
				newFakeCurvePlanStore(),
				CurvePlanSourceOptions{Store: "evidence", MaxObjectBytes: 1 << 20},
			)
			if err != nil {
				t.Fatalf("construct curve-plan source: %v", err)
			}
			if _, _, _, err := source.Resolve(context.Background(), parent); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
			if test.preparedArtifact != "" && len(preparer.requests) != 1 {
				t.Fatalf("active artifact mismatch prepared %d Runs, want 1", len(preparer.requests))
			}
		})
	}
}

func curvePlanCandidateReport(
	t *testing.T,
	parent *edgev1alpha1.BatchEnergyOptimization,
) ([]byte, profilingrun.CandidateReport) {
	t.Helper()
	report := profilingrun.CandidateReport{
		SchemaVersion: profilingrun.CandidateReportSchemaVersion,
		Scope:         "StaticCompatibility",
		Inputs: profilingrun.CandidateReportInputs{
			ModelSpec: profilingrun.CandidateReportObjectInput{
				Name: parent.Spec.State.Candidate.ModelSpec, UID: "model-uid", ResourceVersion: "1",
			},
			DeviceClass: profilingrun.CandidateReportObjectInput{
				Name: parent.Spec.State.Candidate.DeviceClass, UID: "class-uid", ResourceVersion: "2",
			},
			RuntimeDeclaration: profilingrun.CandidateReportRuntimeInput{
				Namespace: "chill-system", Name: "facts", UID: "facts-uid", ResourceVersion: "3",
				Key: "execution-facts.yaml", Image: "runtime@sha256:aaaaaaaa",
			},
		},
		Selection: profilingrun.CandidateReportSelection{
			ExecutionPath: parent.Spec.State.Candidate.ExecutionPath,
			Artifact:      "derived-onnx",
			PowerMode:     profilingrun.FixedPowerMode,
		},
		DeviceClassFacts: profilingrun.CandidateReportDeviceClassFacts{
			Architecture: "amd64",
			NodeSelector: map[string]string{"edge.dacs.io/device-class": parent.Spec.State.Candidate.DeviceClass},
			PowerModes:   []string{profilingrun.FixedPowerMode},
		},
		ArtifactSupply: profilingrun.CandidateReportArtifactSupply{
			Image: "artifact@sha256:bbbbbbbb", Architecture: "amd64", Path: "/artifact/derived.onnx",
		},
		ArtifactCapability: &profilingrun.CandidateArtifactCapability{
			TargetPlatform: "cpu", DeclaredBatchSizes: []int32{4, 1, 2},
			Tensors: []modelcompat.InputTensor{
				{Name: "input", Datatype: "FP32", SampleShape: []int32{3, 224, 224}},
			},
		},
		MetricObserverImage: "metric@sha256:cccccccc",
		Verdict:             "Compatible",
		Reasons:             []profilingrun.CandidateReportReason{},
	}
	return encodeCurvePlanCandidateReport(t, report), report
}

func encodeCurvePlanCandidateReport(t *testing.T, report profilingrun.CandidateReport) []byte {
	t.Helper()
	payload, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := profilingrun.DecodeCandidateReport(payload); err != nil {
		t.Fatalf("invalid candidate-report fixture: %v", err)
	}
	return payload
}

func curvePlanModel(report profilingrun.CandidateReport) *edgev1alpha1.ModelSpec {
	return &edgev1alpha1.ModelSpec{
		ObjectMeta: metav1.ObjectMeta{
			Name: report.Inputs.ModelSpec.Name, UID: "model-uid", ResourceVersion: "1",
		},
		Spec: edgev1alpha1.ModelSpecSpec{Artifacts: []edgev1alpha1.ModelArtifact{{
			Name: report.Selection.Artifact, Format: "onnx",
			Input: &edgev1alpha1.ModelArtifactInputContract{
				Tensors: []edgev1alpha1.ModelInputTensor{
					{Name: "input", Datatype: "FP32", SampleShape: []int32{3, 224, 224}},
				},
			},
			Derivation: &edgev1alpha1.ModelArtifactDerivation{
				SourceArtifact: "canonical-onnx", TargetPlatform: "cpu",
				DeclaredBatchSizes: []int32{1, 2, 4},
				Conversion:         edgev1alpha1.ModelArtifactConversion{Tool: "onnx-tool", Version: "1.2.3"},
			},
		}}},
	}
}

type fakeCurvePlanModelReader struct {
	model       *edgev1alpha1.ModelSpec
	calls       *[]string
	err         error
	transitions []edgev1alpha1.NodeActuation
	runs        []edgev1alpha1.ProfilingRun
}

func (r *fakeCurvePlanModelReader) Get(
	_ context.Context,
	key client.ObjectKey,
	object client.Object,
	_ ...client.GetOption,
) error {
	if r.calls != nil {
		*r.calls = append(*r.calls, "read-model")
	}
	if r.err != nil {
		return r.err
	}
	model, ok := object.(*edgev1alpha1.ModelSpec)
	if !ok {
		return fmt.Errorf("unexpected object type %T", object)
	}
	if key.Name != r.model.Name || key.Namespace != "" {
		return fmt.Errorf("unexpected ModelSpec key %s", key)
	}
	r.model.DeepCopyInto(model)
	return nil
}

func (r *fakeCurvePlanModelReader) List(
	_ context.Context,
	list client.ObjectList,
	_ ...client.ListOption,
) error {
	switch typed := list.(type) {
	case *edgev1alpha1.NodeActuationList:
		typed.Items = r.transitions
	case *edgev1alpha1.ProfilingRunList:
		typed.Items = r.runs
	default:
		return fmt.Errorf("unexpected List type %T", list)
	}
	return nil
}

type fakeCurvePlanCandidateResolver struct {
	reportBytes []byte
	calls       *[]string
}

func (r *fakeCurvePlanCandidateResolver) ResolveCandidate(
	_ context.Context,
	_ profiler.CandidateSelection,
) (profiler.ResolvedCandidate, error) {
	if r.calls != nil {
		*r.calls = append(*r.calls, "resolve-candidate")
	}
	return profiler.ResolvedCandidate{ReportBytes: append([]byte(nil), r.reportBytes...)}, nil
}

type fakeCurvePlanPreparer struct {
	report           profilingrun.CandidateReport
	reportBytes      []byte
	nodeUID          string
	preparedArtifact string
	requests         []profiler.PreparationRequest
	calls            *[]string
}

func (p *fakeCurvePlanPreparer) Prepare(
	_ context.Context,
	request profiler.PreparationRequest,
) (profiler.PreparedRun, error) {
	p.requests = append(p.requests, request)
	if p.calls != nil {
		*p.calls = append(*p.calls, "prepare:"+request.Measurement.WorkloadMode)
	}
	artifact := p.report.Selection.Artifact
	if p.preparedArtifact != "" {
		artifact = p.preparedArtifact
	}
	config := profilingrun.RunConfig{
		SchemaVersion: profilingrun.RunConfigSchemaVersion,
		Candidate: profilingrun.CandidateIdentity{
			Inputs: p.report.Inputs, ExecutionPath: request.Candidate.ExecutionPath, Verdict: p.report.Verdict,
		},
		State: profilingrun.ExecutionState{
			DeviceClass: request.Candidate.DeviceClass, Model: request.Candidate.ModelSpec,
			Artifact: artifact, ArtifactPath: p.report.ArtifactSupply.Path,
			RuntimeFamily: "onnxruntime", Backend: "CPUExecutionProvider", PowerMode: request.Candidate.PowerMode,
		},
		Images: profilingrun.Images{
			Runtime: p.report.Inputs.RuntimeDeclaration.Image, Artifact: p.report.ArtifactSupply.Image,
			MetricObserver: p.report.MetricObserverImage, WorkloadObserver: "workload@sha256:dddddddd",
		},
		Delivery: profilingrun.CandidateRuntimeDelivery{
			ArtifactCapability: p.report.ArtifactCapability,
		},
		TargetNode: profilingrun.NodeSnapshot{
			Name: request.TargetNode, UID: p.nodeUID, ResourceVersion: "10",
			Architecture: p.report.DeviceClassFacts.Architecture, AllocatableCPU: "4",
		},
		CPU: profilingrun.CPUContract{
			Policy: "AllocatableCPUQuota", PolicyVersion: "v1", Request: request.CPURequest, Limit: "4",
			ORTIntraOpThreads: 4, ORTInterOpThreads: 1,
		},
		Measurement: request.Measurement,
		Metrics: profilingrun.MetricContract{
			SourceKind: "edge-metrics", NodeName: request.TargetNode,
			Endpoint: "http://central-meter.test:18766/prometheus?hostname=node", PowerMetric: "power_watts",
			MetricLabels:    map[string]string{"hostname": "node", "device_type": "test_device"},
			ThermalEndpoint: "http://edge-metric.monitoring.svc:9102/metrics",
			ThermalMetrics: profilingrun.ThermalMetricContract{
				ObservationAvailable: "thermal_observation_available", Active: "thermal_active",
				CPUCoreThrottleEvents: "thermal_cpu_core_events", CPUPackageThrottleEvents: "thermal_cpu_package_events",
				Latched: "thermal_latched", MaximumTemperatureCelsius: "thermal_max_celsius",
			},
			IntervalMilliseconds: request.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  request.Metrics.TimeoutMilliseconds,
			DurationSeconds:      request.Metrics.DurationSeconds,
		},
		Workload: profilingrun.CurrentWorkloadContract(),
	}
	if config.State.PowerMode != profilingrun.FixedPowerMode {
		config.Actuation = &profilingrun.ActuationContract{
			PowerMode: config.State.PowerMode,
			Actuator: profilingrun.ActuatorTarget{
				Kind:           profilingrun.ActuatorKindJetsonNVPModel,
				JetsonNVPModel: &profilingrun.JetsonNVPModelTarget{ID: 2},
			},
			PolicyName: "policy",
		}
	}
	if err := config.Validate(); err != nil {
		return profiler.PreparedRun{}, fmt.Errorf("invalid prepared config fixture: %w", err)
	}
	prepared := profiler.PreparedRun{RunConfig: config}
	if config.Actuation != nil {
		policy := actuatedPreparedRun(testParent()).ActuationPolicy
		policy.Policy.Name = config.Actuation.PolicyName
		policy.Policy.DeviceClass = config.State.DeviceClass
		policy.Policy.PowerMode = config.State.PowerMode
		prepared.ActuationPolicy = policy
	}
	return prepared, nil
}

type fakeCurvePlanStore struct {
	objects        map[evidencestore.ObjectLocation][]byte
	puts           int
	opens          int
	lastIdentity   evidencestore.ObjectIdentity
	receiptMutator func(evidencestore.Receipt) evidencestore.Receipt
}

func newFakeCurvePlanStore() *fakeCurvePlanStore {
	return &fakeCurvePlanStore{objects: map[evidencestore.ObjectLocation][]byte{}}
}

func (s *fakeCurvePlanStore) PutImmutable(
	_ context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	s.puts++
	s.lastIdentity = identity
	payload, err := io.ReadAll(content)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	if int64(len(payload)) != identity.Size {
		return evidencestore.Receipt{}, evidencestore.ErrContentMismatch
	}
	outcome := evidencestore.CreateOutcomeCreated
	if _, exists := s.objects[identity.Location()]; exists {
		outcome = evidencestore.CreateOutcomeExisting
	} else {
		s.objects[identity.Location()] = bytes.Clone(payload)
	}
	receipt := evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: outcome,
	}
	if s.receiptMutator != nil {
		receipt = s.receiptMutator(receipt)
	}
	return receipt, nil
}

func (s *fakeCurvePlanStore) OpenLocation(
	_ context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	s.opens++
	payload, exists := s.objects[location]
	if !exists {
		return nil, evidencestore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(payload)), nil
}
