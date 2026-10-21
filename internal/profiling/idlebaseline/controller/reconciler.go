package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	idlebaseline "github.com/lab-paper-code/chill/internal/profiling/idlebaseline"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	idleBaselineKind = "ModelLoadedIdleBaseline"
	admissionWait    = 5 * time.Second
)

type BaselineDeriver interface {
	Derive(
		context.Context,
		[]edgev1alpha1.ProfilingRun,
		profilederivation.PointPolicy,
	) (profilederivation.IdleBaseline, idlebaseline.FinalEvidence, error)
}

type BaselineResultWriter interface {
	Load(
		context.Context,
		*edgev1alpha1.ModelLoadedIdleBaseline,
	) (edgev1alpha1.ProfilingRunEvidenceReference, bool, error)
	Write(
		context.Context,
		*edgev1alpha1.ModelLoadedIdleBaseline,
		idlebaseline.Result,
	) (edgev1alpha1.ProfilingRunEvidenceReference, error)
}

type Reconciler struct {
	client   client.Client
	preparer profilercontroller.RunPreparer
	derive   BaselineDeriver
	results  BaselineResultWriter
	now      func() time.Time
}

func NewReconciler(
	kube client.Client,
	preparer profilercontroller.RunPreparer,
	derive BaselineDeriver,
	results BaselineResultWriter,
) (*Reconciler, error) {
	if kube == nil || preparer == nil || derive == nil || results == nil {
		return nil, errors.New(
			"idle baseline Kubernetes client, Run preparer, deriver, and result writer are required",
		)
	}
	return &Reconciler{
		client: kube, preparer: preparer, derive: derive, results: results, now: time.Now,
	}, nil
}

func (r *Reconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	parent := &edgev1alpha1.ModelLoadedIdleBaseline{}
	if err := r.client.Get(ctx, request.NamespacedName, parent); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if parent.DeletionTimestamp != nil {
		return ctrl.Result{}, nil
	}
	if terminalStatusCurrent(parent) {
		return ctrl.Result{}, nil
	}
	policy, err := pointPolicy(parent.Spec.PointPolicy)
	if err != nil {
		return ctrl.Result{}, err
	}
	runs, err := r.listRuns(ctx, parent)
	if err != nil {
		return ctrl.Result{}, err
	}
	accepted, pending := projectRuns(runs)
	status := edgev1alpha1.ModelLoadedIdleBaselineStatus{
		ObservedGeneration: parent.Generation,
		Phase:              edgev1alpha1.ModelLoadedIdleBaselinePhaseMeasuring,
		AttemptedRuns:      int32(len(runs)),
		CompletedRuns:      int32(len(accepted)),
	}
	if len(runs) == 0 {
		// The baseline has not started until it reported Measuring: wait for
		// ChillServer's admission before the first NodeActuation or Run.
		if parent.Status.Phase != edgev1alpha1.ModelLoadedIdleBaselinePhaseMeasuring {
			granted, _, refused := profilingrun.Admission(parent.Annotations)
			if refused != "" {
				// The status has no reason field; the reason stays in the admission annotation.
				status.Phase = edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient
				return ctrl.Result{}, r.patchStatus(ctx, parent, status)
			}
			if !granted {
				return ctrl.Result{RequeueAfter: admissionWait}, nil
			}
		}
		established, failed, stateErr := r.ensureRequiredState(ctx, parent)
		if stateErr != nil {
			return ctrl.Result{}, stateErr
		}
		if failed {
			status.Phase = edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient
			return ctrl.Result{}, r.patchStatus(ctx, parent, status)
		}
		if !established {
			return ctrl.Result{}, r.patchStatus(ctx, parent, status)
		}
		if err := r.patchStatus(ctx, parent, status); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.ensureRun(ctx, parent, 1)
	}

	if len(accepted) >= policy.MinimumTrials {
		if len(pending) != 0 {
			return ctrl.Result{}, errors.New("idle baseline reached its accepted target with a pending Run")
		}
		if reference, found, loadErr := r.results.Load(ctx, parent); loadErr != nil {
			return ctrl.Result{}, loadErr
		} else if found {
			status.Phase = edgev1alpha1.ModelLoadedIdleBaselinePhaseComplete
			status.Result = &reference
			return ctrl.Result{}, r.patchStatus(ctx, parent, status)
		}
		baseline, references, deriveErr := r.derive.Derive(ctx, accepted, policy)
		if deriveErr != nil {
			return ctrl.Result{}, deriveErr
		}
		result, resultErr := idlebaseline.NewResult(measurementState(parent), baseline, references)
		if resultErr != nil {
			return ctrl.Result{}, fmt.Errorf("construct idle baseline result: %w", resultErr)
		}
		reference, writeErr := r.results.Write(ctx, parent, result)
		if writeErr != nil {
			return ctrl.Result{}, writeErr
		}
		status.Phase = edgev1alpha1.ModelLoadedIdleBaselinePhaseComplete
		status.Result = &reference
		return ctrl.Result{}, r.patchStatus(ctx, parent, status)
	}
	if len(pending) > 1 {
		return ctrl.Result{}, errors.New("ModelLoadedIdleBaseline owns more than one pending Run")
	}
	if len(pending) == 1 {
		return ctrl.Result{}, r.patchStatus(ctx, parent, status)
	}
	if int32(len(runs)) >= parent.Spec.AttemptLimit {
		status.Phase = edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient
		return ctrl.Result{}, r.patchStatus(ctx, parent, status)
	}
	if err := r.patchStatus(ctx, parent, status); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.ensureRun(ctx, parent, int32(len(runs)+1))
}

func (r *Reconciler) listRuns(
	ctx context.Context,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
) ([]edgev1alpha1.ProfilingRun, error) {
	list := &edgev1alpha1.ProfilingRunList{}
	if err := r.client.List(ctx, list, client.InNamespace(parent.Namespace), client.MatchingLabels{
		labelIdleBaselineUID: string(parent.UID),
	}); err != nil {
		return nil, fmt.Errorf("list ModelLoadedIdleBaseline ProfilingRuns: %w", err)
	}
	runs := append([]edgev1alpha1.ProfilingRun(nil), list.Items...)
	sort.Slice(runs, func(i, j int) bool {
		left, _ := strconv.Atoi(runs[i].Labels[labelAttempt])
		right, _ := strconv.Atoi(runs[j].Labels[labelAttempt])
		return left < right
	})
	for index := range runs {
		attempt := int32(index + 1)
		expected, err := buildRun(parent, attempt)
		if err != nil {
			return nil, err
		}
		if err := validateOwnedRun(parent, &runs[index], expected); err != nil {
			return nil, err
		}
	}
	return runs, nil
}

func projectRuns(runs []edgev1alpha1.ProfilingRun) (
	accepted []edgev1alpha1.ProfilingRun,
	pending []edgev1alpha1.ProfilingRun,
) {
	for index := range runs {
		run := runs[index]
		if run.Status.Evidence != nil {
			accepted = append(accepted, run)
			continue
		}
		if !runTerminal(run.Status.Conditions) {
			pending = append(pending, run)
		}
	}
	return accepted, pending
}

func runTerminal(conditions []metav1.Condition) bool {
	for _, condition := range conditions {
		if condition.Status != metav1.ConditionFalse ||
			(condition.Type != "ExecutionComplete" &&
				condition.Type != edgev1alpha1.ProfilingRunConditionEvidenceVerified) {
			continue
		}
		switch condition.Reason {
		case "InvalidRunSpec", "ExecutionPolicyInvalid", "ActuationPolicyInvalid",
			"ExecutionLost", "JobFailed", "RequiredStateNotConfirmed", "EvidenceInvalid":
			return true
		}
	}
	return false
}

func (r *Reconciler) ensureRun(
	ctx context.Context,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
	attempt int32,
) error {
	desired, err := buildRun(parent, attempt)
	if err != nil {
		return err
	}
	existing := &edgev1alpha1.ProfilingRun{}
	key := client.ObjectKeyFromObject(desired)
	if err := r.client.Get(ctx, key, existing); err == nil {
		return validateOwnedRun(parent, existing, desired)
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("get idle baseline ProfilingRun %q: %w", desired.Name, err)
	}
	if err := r.client.Create(ctx, desired); err != nil {
		return fmt.Errorf("create idle baseline ProfilingRun %q: %w", desired.Name, err)
	}
	return nil
}

func validateOwnedRun(
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
	actual, expected *edgev1alpha1.ProfilingRun,
) error {
	owner := metav1.GetControllerOf(actual)
	if owner == nil || owner.Kind != idleBaselineKind ||
		owner.APIVersion != edgev1alpha1.GroupVersion.String() || owner.UID != parent.UID {
		return fmt.Errorf("ProfilingRun %q is not controller-owned by this ModelLoadedIdleBaseline", actual.Name)
	}
	if actual.Namespace != expected.Namespace || actual.Name != expected.Name ||
		!reflect.DeepEqual(actual.Spec, expected.Spec) ||
		actual.Labels[labelIdleBaselineUID] != expected.Labels[labelIdleBaselineUID] ||
		actual.Labels[labelAttempt] != expected.Labels[labelAttempt] {
		return fmt.Errorf("ProfilingRun %q differs from its idle baseline request", actual.Name)
	}
	return nil
}

func pointPolicy(spec edgev1alpha1.ModelLoadedIdleBaselinePointPolicySpec) (profilederivation.PointPolicy, error) {
	confidence, err := strconv.ParseFloat(spec.ConfidenceLevel, 64)
	if err != nil || math.IsNaN(confidence) || math.IsInf(confidence, 0) || confidence <= 0 || confidence >= 1 ||
		strings.TrimSpace(spec.ConfidenceLevel) != spec.ConfidenceLevel {
		return profilederivation.PointPolicy{}, errors.New("idle baseline confidence level must be a clean decimal in (0, 1)")
	}
	return profilederivation.PointPolicy{
		Version: spec.Version, MinimumTrials: int(spec.MinimumRuns), ConfidenceLevel: confidence,
	}, nil
}

func measurementState(parent *edgev1alpha1.ModelLoadedIdleBaseline) idlebaseline.MeasurementState {
	return idlebaseline.MeasurementState{
		TargetNode:    parent.Spec.State.TargetNode,
		ModelSpec:     parent.Spec.State.Candidate.ModelSpec,
		DeviceClass:   parent.Spec.State.Candidate.DeviceClass,
		ExecutionPath: parent.Spec.State.Candidate.ExecutionPath,
		PowerMode:     parent.Spec.State.Candidate.PowerMode,
	}
}

func terminalStatusCurrent(parent *edgev1alpha1.ModelLoadedIdleBaseline) bool {
	if parent.Status.ObservedGeneration != parent.Generation {
		return false
	}
	switch parent.Status.Phase {
	case edgev1alpha1.ModelLoadedIdleBaselinePhaseComplete:
		return parent.Status.Result != nil
	case edgev1alpha1.ModelLoadedIdleBaselinePhaseInsufficient:
		return parent.Status.Result == nil
	default:
		return false
	}
}

func (r *Reconciler) patchStatus(
	ctx context.Context,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
	status edgev1alpha1.ModelLoadedIdleBaselineStatus,
) error {
	if reflect.DeepEqual(parent.Status, status) {
		return nil
	}
	base := parent.DeepCopy()
	desired := parent.DeepCopy()
	desired.Status = status
	if err := r.client.Status().Patch(ctx, desired, client.MergeFrom(base)); err != nil {
		return fmt.Errorf("patch ModelLoadedIdleBaseline status: %w", err)
	}
	return nil
}
