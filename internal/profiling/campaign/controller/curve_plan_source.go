package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	"github.com/lab-paper-code/chill/internal/profiling/run"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CurvePlanPreparer freezes the active and idle Run intents used to project a
// curve plan. The production Profiler preparer satisfies this port.
type CurvePlanPreparer interface {
	Prepare(context.Context, profiler.PreparationRequest) (profiler.PreparedRun, error)
}

// CurvePlanStore provides immutable creation and restart recovery for one
// recorded curve plan.
type CurvePlanStore interface {
	evidencestore.ImmutableWriter
	evidencestore.LocationReader
}

// CurvePlanSourceOptions configures immutable plan storage and bounded recovery.
type CurvePlanSourceOptions struct {
	Store          string
	MaxObjectBytes int64
}

// ArtifactInputContractMissingError identifies a selected artifact that cannot
// supply the tensor contract required by an immutable curve plan.
type ArtifactInputContractMissingError struct {
	Artifact string
}

func (e *ArtifactInputContractMissingError) Error() string {
	return fmt.Sprintf("artifact %q declares no input tensor contract", e.Artifact)
}

// CurvePlanProjector resolves the immutable plan one BatchSaturation selects.
// It reads the facts that plan freezes and writes nothing, so a caller that
// only asks whether an intent resolves needs no Evidence Store authority.
type CurvePlanProjector struct {
	reader   client.Reader
	resolver profiler.CandidateResolver
	preparer CurvePlanPreparer
}

func NewCurvePlanProjector(
	reader client.Reader,
	resolver profiler.CandidateResolver,
	preparer CurvePlanPreparer,
) (*CurvePlanProjector, error) {
	if reader == nil || resolver == nil || preparer == nil {
		return nil, errors.New("ModelSpec reader, candidate resolver, and Run preparer are required")
	}
	return &CurvePlanProjector{reader: reader, resolver: resolver, preparer: preparer}, nil
}

// CurvePlanSource resolves one BatchSaturation's immutable curve plan without
// creating either of the Run objects used to project its active and idle
// intents.
type CurvePlanSource struct {
	projector *CurvePlanProjector
	store     CurvePlanStore
	options   CurvePlanSourceOptions
}

func NewCurvePlanSource(
	reader client.Reader,
	resolver profiler.CandidateResolver,
	preparer CurvePlanPreparer,
	store CurvePlanStore,
	options CurvePlanSourceOptions,
) (*CurvePlanSource, error) {
	if reader == nil || resolver == nil || preparer == nil || store == nil {
		return nil, errors.New("ModelSpec reader, candidate resolver, Run preparer, and curve-plan store are required")
	}
	if strings.TrimSpace(options.Store) == "" || strings.TrimSpace(options.Store) != options.Store {
		return nil, errors.New("curve-plan logical store is required without surrounding whitespace")
	}
	if options.MaxObjectBytes < 1 {
		return nil, errors.New("maximum curve-plan object size must be positive")
	}
	projector, err := NewCurvePlanProjector(reader, resolver, preparer)
	if err != nil {
		return nil, err
	}
	return &CurvePlanSource{
		projector: projector, store: store, options: options,
	}, nil
}

// Project resolves the plan a campaign selects without recording it, so a
// caller that only needs to know whether the intent resolves does not write to
// the Evidence Store.
func (p *CurvePlanProjector) Project(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
) (profilederivation.CurvePlan, *TransitionMaterial, error) {
	if parent == nil {
		return profilederivation.CurvePlan{}, nil, errors.New("a parent BatchSaturation is required")
	}

	selection := profiler.CandidateSelection{
		ModelSpec:     parent.Spec.State.Candidate.ModelSpec,
		DeviceClass:   parent.Spec.State.Candidate.DeviceClass,
		ExecutionPath: parent.Spec.State.Candidate.ExecutionPath,
		PowerMode:     parent.Spec.State.Candidate.PowerMode,
	}
	resolved, err := p.resolver.ResolveCandidate(ctx, selection)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, fmt.Errorf("resolve curve-plan candidate: %w", err)
	}
	report, err := profilingrun.DecodeCandidateReport(resolved.ReportBytes)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, fmt.Errorf("decode curve-plan candidate report: %w", err)
	}
	plannedBatchSizes, err := resolvePlannedBatchSizes(parent.Spec.BatchSizes, report.ArtifactCapability)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, err
	}

	active, err := p.prepare(ctx, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive,
		Batch:        plannedBatchSizes[0],
	})
	if err != nil {
		return profilederivation.CurvePlan{}, nil, fmt.Errorf("prepare active curve-plan intent: %w", err)
	}
	if active.Intent.State.Artifact != report.Selection.Artifact {
		return profilederivation.CurvePlan{}, nil, errors.New("prepared active intent artifact disagrees with the resolved candidate report")
	}
	transition, err := projectTransitionMaterial(active)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, fmt.Errorf("project campaign transition: %w", err)
	}
	idle, err := p.prepare(ctx, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeIdle,
		Batch:        plannedBatchSizes[0],
	})
	if err != nil {
		return profilederivation.CurvePlan{}, nil, fmt.Errorf("prepare idle curve-plan intent: %w", err)
	}
	artifact, err := p.resolveArtifact(ctx, parent.Spec.State.Candidate.ModelSpec, report)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, err
	}
	conversion, err := resolveConversion(artifact, report)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, err
	}
	if artifact.Input == nil {
		return profilederivation.CurvePlan{}, nil,
			&ArtifactInputContractMissingError{Artifact: artifact.Name}
	}
	capability := profilingrun.CandidateArtifactCapability{
		InputSampleShape: append([]int32(nil), artifact.Input.InputSampleShape...),
		InputLayout:      artifact.Input.Layout,
		InputDataType:    artifact.Input.DataType,
	}
	if report.ArtifactCapability != nil {
		capability.TargetPlatform = report.ArtifactCapability.TargetPlatform
		capability.DeclaredBatchSizes = append(
			[]int32(nil), report.ArtifactCapability.DeclaredBatchSizes...,
		)
	}
	spec, err := admission.ProjectCurvePlanSpec(
		parent.Name,
		active.Intent,
		idle.Intent,
		capability,
		conversion,
		plannedBatchSizes,
	)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, fmt.Errorf("project curve-plan specification: %w", err)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, fmt.Errorf("construct curve plan: %w", err)
	}
	return plan, transition, nil
}

// Resolve recomputes the selected plan and records it immutably. When the
// object already exists, the stored plan remains authoritative and is returned
// with its observed reference only after it agrees semantically with the
// recomputed plan.
func (s *CurvePlanSource) Resolve(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
) (profilederivation.CurvePlan, batchsaturation.StoredObjectReference, *TransitionMaterial, error) {
	if parent == nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, nil, errors.New("a parent BatchSaturation is required")
	}
	if parent.UID == "" {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, nil, errors.New("BatchSaturation UID is required")
	}
	plan, transition, err := s.projector.Project(ctx, parent)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, nil, err
	}
	recorded, reference, err := s.record(ctx, parent.UID, plan)
	return recorded, reference, transition, err
}

func resolvePlannedBatchSizes(
	campaign []int32,
	capability *profilingrun.CandidateArtifactCapability,
) ([]int, error) {
	if capability != nil {
		if len(campaign) != 0 {
			return nil, errors.New("BatchSaturation batchSizes must be absent when the selected artifact declares batch sizes")
		}
		planned := make([]int, len(capability.DeclaredBatchSizes))
		for index, batchSize := range capability.DeclaredBatchSizes {
			planned[index] = int(batchSize)
		}
		sort.Ints(planned)
		return planned, nil
	}
	if len(campaign) == 0 {
		return nil, errors.New("BatchSaturation batchSizes are required when the selected artifact declares no batch sizes")
	}
	planned := make([]int, len(campaign))
	for index, batchSize := range campaign {
		if batchSize < 1 {
			return nil, errors.New("BatchSaturation batchSizes must be positive")
		}
		if index > 0 && batchSize <= campaign[index-1] {
			return nil, errors.New("BatchSaturation batchSizes must be strictly increasing")
		}
		planned[index] = int(batchSize)
	}
	return planned, nil
}

func (p *CurvePlanProjector) prepare(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
	measurement batchsaturation.Measurement,
) (profiler.PreparedRun, error) {
	run, err := BuildRun(parent, measurement, 1)
	if err != nil {
		return profiler.PreparedRun{}, err
	}
	requested, err := profilercontroller.ProjectRequest(run.Spec)
	if err != nil {
		return profiler.PreparedRun{}, err
	}
	return p.preparer.Prepare(ctx, requested.Preparation)
}

func (p *CurvePlanProjector) resolveArtifact(
	ctx context.Context,
	modelSpecName string,
	report profilingrun.CandidateReport,
) (edgev1alpha1.ModelArtifact, error) {
	model := &edgev1alpha1.ModelSpec{}
	if err := p.reader.Get(ctx, types.NamespacedName{Name: modelSpecName}, model); err != nil {
		return edgev1alpha1.ModelArtifact{}, fmt.Errorf("get ModelSpec %q for curve-plan artifact: %w", modelSpecName, err)
	}
	input := report.Inputs.ModelSpec
	if model.Name != input.Name || string(model.UID) != input.UID || model.ResourceVersion != input.ResourceVersion {
		return edgev1alpha1.ModelArtifact{}, errors.New("curve-plan ModelSpec does not match the resolved candidate report identity")
	}
	var selected *edgev1alpha1.ModelArtifact
	for index := range model.Spec.Artifacts {
		artifact := &model.Spec.Artifacts[index]
		if artifact.Name != report.Selection.Artifact {
			continue
		}
		if selected != nil {
			return edgev1alpha1.ModelArtifact{}, fmt.Errorf(
				"ModelSpec %q contains duplicate artifact %q", model.Name, artifact.Name,
			)
		}
		selected = artifact
	}
	if selected == nil {
		return edgev1alpha1.ModelArtifact{}, fmt.Errorf(
			"ModelSpec %q does not contain selected artifact %q", model.Name, report.Selection.Artifact,
		)
	}
	return *selected, nil
}

func resolveConversion(
	selected edgev1alpha1.ModelArtifact,
	report profilingrun.CandidateReport,
) (profilederivation.CurveConversionContract, error) {
	if selected.Derivation == nil {
		if report.ArtifactCapability != nil {
			return profilederivation.CurveConversionContract{}, errors.New(
				"canonical artifact disagrees with the candidate report artifact capability",
			)
		}
		return profilederivation.CurveConversionContract{}, nil
	}
	if report.ArtifactCapability == nil {
		return profilederivation.CurveConversionContract{}, errors.New(
			"derived artifact has no candidate report artifact capability",
		)
	}
	return profilederivation.CurveConversionContract{
		TargetPlatform: report.ArtifactCapability.TargetPlatform,
		Tool:           selected.Derivation.Conversion.Tool,
		ToolVersion:    selected.Derivation.Conversion.Version,
	}, nil
}

func (s *CurvePlanSource) record(
	ctx context.Context,
	parentUID types.UID,
	plan profilederivation.CurvePlan,
) (profilederivation.CurvePlan, batchsaturation.StoredObjectReference, error) {
	payload, err := plan.CanonicalJSON()
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("encode curve plan: %w", err)
	}
	identity := evidencestore.ObjectIdentity{
		Store: s.options.Store,
		Key:   fmt.Sprintf("batch-saturations/%s/curve-plan.json", parentUID),
		Size:  int64(len(payload)),
	}
	reference := batchsaturation.StoredObjectReference{
		Store: identity.Store, Key: identity.Key,
		SchemaVersion: plan.SchemaVersion(), Size: identity.Size,
	}
	receipt, err := s.store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("record curve plan: %w", err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, errors.New("curve-plan writer returned an invalid receipt")
	}
	if receipt.Outcome == evidencestore.CreateOutcomeCreated {
		return plan, reference, nil
	}

	storedBytes, err := readStoredObject(
		ctx, s.store, identity.Location(), s.options.MaxObjectBytes, "curve-plan object",
	)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("read existing curve plan: %w", err)
	}
	stored, err := profilederivation.DecodeCurvePlan(storedBytes)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("decode existing curve plan: %w", err)
	}
	if !reflect.DeepEqual(stored.Shared(), plan.Shared()) {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, errors.New("stored curve plan disagrees with the recomputed plan")
	}
	return stored, batchsaturation.StoredObjectReference{
		Store: identity.Store, Key: identity.Key,
		SchemaVersion: stored.SchemaVersion(), Size: int64(len(storedBytes)),
	}, nil
}

func readStoredObject(
	ctx context.Context,
	reader evidencestore.LocationReader,
	location evidencestore.ObjectLocation,
	maximumBytes int64,
	objectName string,
) ([]byte, error) {
	stream, err := reader.OpenLocation(ctx, location)
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(payload)) > maximumBytes {
		return nil, fmt.Errorf("%s exceeds %d-byte limit", objectName, maximumBytes)
	}
	return payload, nil
}
