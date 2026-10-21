package controller

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	batchenergyoptimization "github.com/lab-paper-code/chill/internal/profiling/campaign"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CurvePlanPreparer freezes the active and idle RunConfigs used to project a
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

// CurvePlanProjector resolves the immutable plan one BatchEnergyOptimization selects.
// It reads the facts that plan freezes and writes nothing, so a caller that
// only asks whether a RunConfig resolves needs no Evidence Store authority.
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

// CurvePlanSource resolves one BatchEnergyOptimization's immutable curve plan without
// creating either of the Run objects used to project its active and idle
// RunConfigs.
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
// caller that only needs to know whether the RunConfig resolves does not write to
// the Evidence Store.
func (p *CurvePlanProjector) Project(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
) (profilederivation.CurvePlan, *TransitionMaterial, error) {
	if parent == nil {
		return profilederivation.CurvePlan{}, nil, errors.New("a parent BatchEnergyOptimization is required")
	}

	report, plannedBatchSizes, err := p.resolveCandidateReportAndBatches(ctx, parent)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, err
	}
	active, idle, transition, err := p.prepareCurveRunConfigsAndTransition(ctx, parent, report, plannedBatchSizes)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, err
	}
	artifact, err := p.resolveArtifact(ctx, parent.Spec.State.Candidate.ModelSpec, report)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, err
	}
	conversion, err := resolveConversion(artifact, report)
	if err != nil {
		return profilederivation.CurvePlan{}, nil, err
	}

	spec, err := projection.ProjectCurvePlanSpec(
		parent.Name,
		active.RunConfig,
		idle.RunConfig,
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

func (p *CurvePlanProjector) resolveCandidateReportAndBatches(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
) (profilingrun.CandidateReport, []int, error) {
	selection := profiler.CandidateSelection{
		ModelSpec:     parent.Spec.State.Candidate.ModelSpec,
		DeviceClass:   parent.Spec.State.Candidate.DeviceClass,
		Node:          parent.Spec.State.TargetNode,
		ExecutionPath: parent.Spec.State.Candidate.ExecutionPath,
		PowerMode:     parent.Spec.State.Candidate.PowerMode,
		Preprocessor:  parent.Spec.State.Candidate.Preprocessor,
		Dataset:       parent.Spec.ActiveProtocol.Dataset,
	}
	resolved, err := p.resolver.ResolveCandidate(ctx, selection)
	if err != nil {
		return profilingrun.CandidateReport{}, nil, fmt.Errorf("resolve curve-plan candidate: %w", err)
	}
	report, err := profilingrun.DecodeCandidateReport(resolved.ReportBytes)
	if err != nil {
		return profilingrun.CandidateReport{}, nil, fmt.Errorf("decode curve-plan candidate report: %w", err)
	}
	plannedBatchSizes, err := resolvePlannedBatchSizes(parent.Spec.BatchSizes, report.ArtifactCapability)
	if err != nil {
		return profilingrun.CandidateReport{}, nil, err
	}
	return report, plannedBatchSizes, nil
}

func (p *CurvePlanProjector) prepareCurveRunConfigsAndTransition(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
	report profilingrun.CandidateReport,
	plannedBatchSizes []int,
) (profiler.PreparedRun, profiler.PreparedRun, *TransitionMaterial, error) {
	active, err := p.prepare(ctx, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive,
		Batch:        plannedBatchSizes[0],
	})
	if err != nil {
		return profiler.PreparedRun{}, profiler.PreparedRun{}, nil, fmt.Errorf("prepare active curve-plan RunConfig: %w", err)
	}
	if active.RunConfig.State.Artifact != report.Selection.Artifact {
		return profiler.PreparedRun{}, profiler.PreparedRun{}, nil,
			errors.New("prepared active RunConfig artifact disagrees with the resolved candidate report")
	}
	transition, err := projectTransitionMaterial(active)
	if err != nil {
		return profiler.PreparedRun{}, profiler.PreparedRun{}, nil, fmt.Errorf("project campaign transition: %w", err)
	}
	idle, err := p.prepare(ctx, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeIdle,
		Batch:        plannedBatchSizes[0],
	})
	if err != nil {
		return profiler.PreparedRun{}, profiler.PreparedRun{}, nil, fmt.Errorf("prepare idle curve-plan RunConfig: %w", err)
	}
	return active, idle, transition, nil
}

// Resolve returns the plan recorded for the campaign. The stored plan is read
// and decoded once and is never compared with current facts. Only when no plan
// is stored yet is the plan projected once and recorded immutably.
//
// The curve plan carries no actuation policy, so transition material comes
// from the campaign's own frozen state: the owned NodeActuation when one
// exists, no transition when Runs exist without one (they are only created
// after the transition), and otherwise, before either exists, the current
// projection of the one transition the campaign still has to create.
func (s *CurvePlanSource) Resolve(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, *TransitionMaterial, error) {
	if parent == nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, errors.New("a parent BatchEnergyOptimization is required")
	}
	if parent.UID == "" {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, errors.New("BatchEnergyOptimization UID is required")
	}
	location := s.location(parent.UID)
	stored, reference, err := s.readStored(ctx, location)
	if errors.Is(err, evidencestore.ErrNotFound) {
		plan, transition, projectErr := s.projector.Project(ctx, parent)
		if projectErr != nil {
			return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, projectErr
		}
		recorded, recordedReference, recordErr := s.record(ctx, location, plan)
		if recordErr != nil {
			return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, recordErr
		}
		transition, bindErr := bindTransitionToPlan(recorded, transition)
		if bindErr != nil {
			return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, bindErr
		}
		return recorded, recordedReference, transition, nil
	}
	if err != nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, err
	}
	transition, err := s.recoverTransition(ctx, parent, stored)
	if err != nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, nil, err
	}
	return stored, reference, transition, nil
}

func (s *CurvePlanSource) location(parentUID types.UID) evidencestore.ObjectLocation {
	return evidencestore.ObjectLocation{
		Store: s.options.Store,
		Key:   fmt.Sprintf("batch-energy-optimizations/%s/curve-plan.json", parentUID),
	}
}

// readStored returns the stored plan and its observed reference. A missing
// object is returned as evidencestore.ErrNotFound; any other failure,
// including undecodable stored evidence, is a distinct error.
func (s *CurvePlanSource) readStored(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, error) {
	storedBytes, err := readStoredObject(
		ctx, s.store, location, s.options.MaxObjectBytes, "curve-plan object",
	)
	if errors.Is(err, evidencestore.ErrNotFound) {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, err
	}
	if err != nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, fmt.Errorf("read existing curve plan: %w", err)
	}
	stored, err := profilederivation.DecodeCurvePlan(storedBytes)
	if err != nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, fmt.Errorf("decode existing curve plan: %w", err)
	}
	return stored, batchenergyoptimization.StoredObjectReference{
		Store: location.Store, Key: location.Key,
		SchemaVersion: stored.SchemaVersion(), Size: int64(len(storedBytes)),
	}, nil
}

// recoverTransition returns the transition material for a campaign whose plan
// is already frozen. An owned NodeActuation is the frozen authority; owned Runs
// without one need no transition. Only before either exists is the one
// transition prepared from the current policy for the frozen plan's first batch.
// Every material is bound to the frozen plan's Node UID.
func (s *CurvePlanSource) recoverTransition(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
	plan profilederivation.CurvePlan,
) (*TransitionMaterial, error) {
	matching := []client.ListOption{
		client.InNamespace(parent.Namespace),
		client.MatchingLabels{labelBatchEnergyOptimization: parent.Name},
	}
	transitions := &edgev1alpha1.NodeActuationList{}
	if err := s.projector.reader.List(ctx, transitions, matching...); err != nil {
		return nil, fmt.Errorf("list BatchEnergyOptimization NodeActuations: %w", err)
	}
	if len(transitions.Items) > 1 {
		return nil, errors.New("a BatchEnergyOptimization cannot own more than one transition")
	}
	if len(transitions.Items) == 1 {
		transition := &transitions.Items[0]
		if !controlledByParent(parent, transition) {
			return nil, fmt.Errorf("NodeActuation %q is not controller-owned by this BatchEnergyOptimization", transition.Name)
		}
		spec := transition.Spec
		return bindTransitionToPlan(plan, &TransitionMaterial{
			Node: spec.Node, DeviceClass: spec.DeviceClass, PowerMode: spec.PowerMode,
			Actuator: spec.Actuator, PolicyName: spec.PolicyName,
			SettlingDurationSeconds:     spec.SettlingDurationSeconds,
			MaxActuationDurationSeconds: spec.MaxActuationDurationSeconds,
		})
	}
	runs := &edgev1alpha1.ProfilingRunList{}
	if err := s.projector.reader.List(ctx, runs, matching...); err != nil {
		return nil, fmt.Errorf("list BatchEnergyOptimization ProfilingRuns: %w", err)
	}
	valid := 0
	for index := range runs.Items {
		if !controlledByParent(parent, &runs.Items[index]) {
			return nil, fmt.Errorf("ProfilingRun %q is not controller-owned by this BatchEnergyOptimization", runs.Items[index].Name)
		}
		// A Run marked measurement-invalid is not evidence.
		if _, invalid := runs.Items[index].Annotations[profilingrun.InvalidAnnotation]; !invalid {
			valid++
		}
	}
	if valid > 0 {
		return nil, nil
	}
	batches := plan.PlannedBatchSizes()
	if len(batches) == 0 {
		return nil, errors.New("frozen curve plan has no planned batch sizes")
	}
	active, err := s.projector.prepare(ctx, parent, batchenergyoptimization.Measurement{
		WorkloadMode: batchenergyoptimization.WorkloadModeActive,
		Batch:        batches[0],
	})
	if err != nil {
		return nil, fmt.Errorf("prepare active curve-plan RunConfig: %w", err)
	}
	material, err := projectTransitionMaterial(active)
	if err != nil {
		return nil, fmt.Errorf("project campaign transition: %w", err)
	}
	return bindTransitionToPlan(plan, material)
}

// bindTransitionToPlan rejects transition material aimed at a Node other than
// the one the frozen plan was measured on.
func bindTransitionToPlan(
	plan profilederivation.CurvePlan,
	material *TransitionMaterial,
) (*TransitionMaterial, error) {
	if material != nil && material.Node.UID != plan.Shared().NodeUID {
		return nil, errors.New("transition Node UID does not match the frozen curve plan")
	}
	return material, nil
}

func resolvePlannedBatchSizes(
	campaign []int32,
	capability *profilingrun.CandidateArtifactCapability,
) ([]int, error) {
	if capability != nil && len(capability.DeclaredBatchSizes) != 0 {
		if len(campaign) != 0 {
			return nil, errors.New("BatchEnergyOptimization batchSizes must be absent when the selected artifact declares batch sizes")
		}
		planned := make([]int, len(capability.DeclaredBatchSizes))
		for index, batchSize := range capability.DeclaredBatchSizes {
			planned[index] = int(batchSize)
		}
		sort.Ints(planned)
		return planned, nil
	}
	if len(campaign) == 0 {
		return nil, errors.New("BatchEnergyOptimization batchSizes are required when the selected artifact declares no batch sizes")
	}
	planned := make([]int, len(campaign))
	for index, batchSize := range campaign {
		if batchSize < 1 {
			return nil, errors.New("BatchEnergyOptimization batchSizes must be positive")
		}
		if index > 0 && batchSize <= campaign[index-1] {
			return nil, errors.New("BatchEnergyOptimization batchSizes must be strictly increasing")
		}
		planned[index] = int(batchSize)
	}
	return planned, nil
}

func (p *CurvePlanProjector) prepare(
	ctx context.Context,
	parent *edgev1alpha1.BatchEnergyOptimization,
	measurement batchenergyoptimization.Measurement,
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
		if report.ArtifactCapability != nil && (report.ArtifactCapability.TargetPlatform != "" || len(report.ArtifactCapability.DeclaredBatchSizes) != 0) {
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
	location evidencestore.ObjectLocation,
	plan profilederivation.CurvePlan,
) (profilederivation.CurvePlan, batchenergyoptimization.StoredObjectReference, error) {
	payload, err := plan.CanonicalJSON()
	if err != nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, fmt.Errorf("encode curve plan: %w", err)
	}
	identity := evidencestore.ObjectIdentity{
		Store: location.Store, Key: location.Key, Size: int64(len(payload)),
	}
	reference := batchenergyoptimization.StoredObjectReference{
		Store: identity.Store, Key: identity.Key,
		SchemaVersion: plan.SchemaVersion(), Size: identity.Size,
	}
	receipt, err := s.store.PutImmutable(ctx, identity, bytes.NewReader(payload))
	if err != nil {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, fmt.Errorf("record curve plan: %w", err)
	}
	if err := receipt.Validate(); err != nil || receipt.Object != identity {
		return profilederivation.CurvePlan{}, batchenergyoptimization.StoredObjectReference{}, errors.New("curve-plan writer returned an invalid receipt")
	}
	if receipt.Outcome == evidencestore.CreateOutcomeCreated {
		return plan, reference, nil
	}

	// A concurrent writer won the create; its plan is authoritative.
	return s.readStored(ctx, identity.Location())
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
