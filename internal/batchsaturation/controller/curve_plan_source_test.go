package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profilingrun"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCurvePlanSourceRecordsRecoversAndRejectsDrift(t *testing.T) {
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

	first, firstReference, transition, err := source.Resolve(context.Background(), parent)
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
	wantKey := "batch-saturations/parent-uid/curve-plan.json"
	if store.puts != 1 || store.opens != 0 || store.lastIdentity.Key != wantKey {
		t.Fatalf("first record calls = puts %d, opens %d, identity %+v", store.puts, store.opens, store.lastIdentity)
	}
	storedPayload := store.objects[store.lastIdentity.Location()]
	if firstReference.Store != storeName || firstReference.Key != wantKey ||
		firstReference.SchemaVersion != "chill.dacs.io/curve-plan.v1alpha4" ||
		firstReference.Size != int64(len(storedPayload)) {
		t.Fatalf("created curve-plan reference = %+v, payload bytes = %d", firstReference, len(storedPayload))
	}
	if len(preparer.requests) != 2 || preparer.requests[0].Measurement.Batch != 1 ||
		preparer.requests[0].Measurement.WorkloadMode != profilingrun.SaturatedSerialFixedBatchWorkloadMode ||
		preparer.requests[1].Measurement.Batch != 1 ||
		preparer.requests[1].Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		t.Fatalf("projected preparation requests = %+v", preparer.requests)
	}

	// Recovery returns the stored object's observed size, not the recomputed
	// canonical length.
	storedPayload = append([]byte(" \n"), storedPayload...)
	store.objects[store.lastIdentity.Location()] = storedPayload
	_, recoveredReference, _, err := source.Resolve(context.Background(), parent)
	if err != nil {
		t.Fatalf("recover existing curve plan: %v", err)
	}
	if store.puts != 2 || store.opens != 1 || recoveredReference.Store != storeName ||
		recoveredReference.Key != wantKey ||
		recoveredReference.SchemaVersion != "chill.dacs.io/curve-plan.v1alpha4" ||
		recoveredReference.Size != int64(len(storedPayload)) ||
		recoveredReference.Size == firstReference.Size {
		t.Fatalf("recovery = reference %+v, puts %d, opens %d", recoveredReference, store.puts, store.opens)
	}

	preparer.nodeUID = "replacement-node-uid"
	if _, _, _, err := source.Resolve(context.Background(), parent); err == nil ||
		!strings.Contains(err.Error(), "stored curve plan disagrees") {
		t.Fatalf("recomputed plan drift error = %v", err)
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
			name: "runtime consumes batch as call argument",
			mutateReport: func(report *profilingrun.CandidateReport) {
				report.ArtifactCapability = nil
			},
			want: "requires an artifact that declares its batch sizes",
		},
		{
			name: "derived artifact has no input tensor contract",
			mutateModel: func(model *edgev1alpha1.ModelSpec) {
				model.Spec.Artifacts[0].Derivation.Input = nil
			},
			want: "requires a derived artifact that declares its input tensor contract",
		},
		{
			name: "conversion receives a canonical artifact",
			mutateModel: func(model *edgev1alpha1.ModelSpec) {
				model.Spec.Artifacts[0].Derivation = nil
			},
			want: "resolve conversion requires a derived artifact",
		},
		{
			name:             "prepared artifact disagrees with report",
			preparedArtifact: "different-artifact",
			want:             "prepared active intent artifact disagrees",
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
		})
	}
}

func curvePlanCandidateReport(
	t *testing.T,
	parent *edgev1alpha1.BatchSaturation,
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
			InputSampleShape: []int32{3, 224, 224}, InputLayout: "NCHW", InputDataType: "float32",
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
			Derivation: &edgev1alpha1.ModelArtifactDerivation{
				SourceArtifact: "canonical-onnx", TargetPlatform: "cpu",
				DeclaredBatchSizes: []int32{1, 2, 4},
				Input: &edgev1alpha1.ModelArtifactInputContract{
					InputSampleShape: []int32{3, 224, 224}, Layout: "NCHW", DataType: "float32",
				},
				Conversion: edgev1alpha1.ModelArtifactConversion{Tool: "onnx-tool", Version: "1.2.3"},
			},
		}}},
	}
}

type fakeCurvePlanModelReader struct {
	model *edgev1alpha1.ModelSpec
}

func (r *fakeCurvePlanModelReader) Get(
	_ context.Context,
	key client.ObjectKey,
	object client.Object,
	_ ...client.GetOption,
) error {
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

func (*fakeCurvePlanModelReader) List(
	context.Context,
	client.ObjectList,
	...client.ListOption,
) error {
	return errors.New("unexpected List call")
}

type fakeCurvePlanCandidateResolver struct {
	reportBytes []byte
}

func (r *fakeCurvePlanCandidateResolver) ResolveCandidate(
	_ context.Context,
	_ profiler.CandidateSelection,
) (profiler.ResolvedCandidate, error) {
	return profiler.ResolvedCandidate{ReportBytes: append([]byte(nil), r.reportBytes...)}, nil
}

type fakeCurvePlanPreparer struct {
	report           profilingrun.CandidateReport
	reportBytes      []byte
	nodeUID          string
	preparedArtifact string
	requests         []profiler.PreparationRequest
}

func (p *fakeCurvePlanPreparer) Prepare(
	_ context.Context,
	request profiler.PreparationRequest,
) (profiler.PreparedRun, error) {
	p.requests = append(p.requests, request)
	artifact := p.report.Selection.Artifact
	if p.preparedArtifact != "" {
		artifact = p.preparedArtifact
	}
	intent := profilingrun.Intent{
		SchemaVersion: profilingrun.SchemaVersion,
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
			Endpoint: "http://edge-metric.monitoring.svc:9102/metrics", PowerMetric: "power_watts",
			IntervalMilliseconds: request.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  request.Metrics.TimeoutMilliseconds,
			DurationSeconds:      request.Metrics.DurationSeconds,
		},
		Workload: profilingrun.CurrentWorkloadContract(),
	}
	if err := intent.Validate(); err != nil {
		return profiler.PreparedRun{}, fmt.Errorf("invalid prepared intent fixture: %w", err)
	}
	return profiler.PreparedRun{
		Intent: intent, CandidateReportBytes: append([]byte(nil), p.reportBytes...),
	}, nil
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
