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
	"github.com/lab-paper-code/chill/internal/batchsaturation"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/profilederivation"
	"github.com/lab-paper-code/chill/internal/profiler"
	"github.com/lab-paper-code/chill/internal/profiler/admission"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
	"github.com/lab-paper-code/chill/internal/profilingrun"
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

// CurvePlanSource resolves one BatchSaturation's immutable curve plan without
// creating either of the Run objects used to project its active and idle
// intents.
type CurvePlanSource struct {
	reader   client.Reader
	resolver profiler.CandidateResolver
	preparer CurvePlanPreparer
	store    CurvePlanStore
	options  CurvePlanSourceOptions
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
	return &CurvePlanSource{
		reader: reader, resolver: resolver, preparer: preparer, store: store, options: options,
	}, nil
}

// Resolve recomputes the selected plan and records it immutably. When the
// object already exists, the stored plan remains authoritative and is returned
// with its observed reference only after it agrees semantically with the
// recomputed plan.
func (s *CurvePlanSource) Resolve(
	ctx context.Context,
	parent *edgev1alpha1.BatchSaturation,
) (profilederivation.CurvePlan, batchsaturation.StoredObjectReference, error) {
	if parent == nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, errors.New("a parent BatchSaturation is required")
	}
	if parent.UID == "" {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, errors.New("BatchSaturation UID is required")
	}

	selection := profiler.CandidateSelection{
		ModelSpec:     parent.Spec.State.Candidate.ModelSpec,
		DeviceClass:   parent.Spec.State.Candidate.DeviceClass,
		ExecutionPath: parent.Spec.State.Candidate.ExecutionPath,
		PowerMode:     parent.Spec.State.Candidate.PowerMode,
	}
	resolved, err := s.resolver.ResolveCandidate(ctx, selection)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("resolve curve-plan candidate: %w", err)
	}
	report, err := profilingrun.DecodeCandidateReport(resolved.ReportBytes)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("decode curve-plan candidate report: %w", err)
	}
	if report.ArtifactCapability == nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, errors.New(
			"a batch-saturation campaign requires an artifact that declares its batch sizes; " +
				"a runtime that consumes batch as a call argument is not supported here",
		)
	}
	plannedBatchSizes := make([]int, len(report.ArtifactCapability.DeclaredBatchSizes))
	for index, batchSize := range report.ArtifactCapability.DeclaredBatchSizes {
		plannedBatchSizes[index] = int(batchSize)
	}
	sort.Ints(plannedBatchSizes)

	active, err := s.prepare(ctx, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeActive,
		Batch:        plannedBatchSizes[0],
	})
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("prepare active curve-plan intent: %w", err)
	}
	if active.Intent.State.Artifact != report.Selection.Artifact {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, errors.New("prepared active intent artifact disagrees with the resolved candidate report")
	}
	idle, err := s.prepare(ctx, parent, batchsaturation.Measurement{
		WorkloadMode: batchsaturation.WorkloadModeIdle,
		Batch:        plannedBatchSizes[0],
	})
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("prepare idle curve-plan intent: %w", err)
	}
	conversion, err := s.resolveConversion(ctx, parent.Spec.State.Candidate.ModelSpec, report)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, err
	}
	spec, err := admission.ProjectCurvePlanSpec(
		parent.Name,
		active.Intent,
		idle.Intent,
		*report.ArtifactCapability,
		conversion,
		plannedBatchSizes,
	)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("project curve-plan specification: %w", err)
	}
	plan, err := profilederivation.NewCurvePlan(spec)
	if err != nil {
		return profilederivation.CurvePlan{}, batchsaturation.StoredObjectReference{}, fmt.Errorf("construct curve plan: %w", err)
	}
	return s.record(ctx, parent.UID, plan)
}

func (s *CurvePlanSource) prepare(
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
	return s.preparer.Prepare(ctx, requested.Preparation)
}

func (s *CurvePlanSource) resolveConversion(
	ctx context.Context,
	modelSpecName string,
	report profilingrun.CandidateReport,
) (profilederivation.CurveConversionContract, error) {
	model := &edgev1alpha1.ModelSpec{}
	if err := s.reader.Get(ctx, types.NamespacedName{Name: modelSpecName}, model); err != nil {
		return profilederivation.CurveConversionContract{}, fmt.Errorf("get ModelSpec %q for conversion provenance: %w", modelSpecName, err)
	}
	input := report.Inputs.ModelSpec
	if model.Name != input.Name || string(model.UID) != input.UID || model.ResourceVersion != input.ResourceVersion {
		return profilederivation.CurveConversionContract{}, errors.New("conversion ModelSpec does not match the resolved candidate report identity")
	}
	var selected *edgev1alpha1.ModelArtifact
	for index := range model.Spec.Artifacts {
		artifact := &model.Spec.Artifacts[index]
		if artifact.Name != report.Selection.Artifact {
			continue
		}
		if selected != nil {
			return profilederivation.CurveConversionContract{}, fmt.Errorf(
				"ModelSpec %q contains duplicate artifact %q", model.Name, artifact.Name,
			)
		}
		selected = artifact
	}
	if selected == nil {
		return profilederivation.CurveConversionContract{}, fmt.Errorf(
			"ModelSpec %q does not contain selected artifact %q", model.Name, report.Selection.Artifact,
		)
	}
	if selected.Derivation == nil {
		return profilederivation.CurveConversionContract{}, errors.New("resolve conversion requires a derived artifact")
	}
	if selected.Derivation.Input == nil {
		return profilederivation.CurveConversionContract{}, errors.New(
			"a batch-saturation campaign requires a derived artifact that declares its input tensor contract; " +
				"a derived artifact without that contract is not supported here",
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
