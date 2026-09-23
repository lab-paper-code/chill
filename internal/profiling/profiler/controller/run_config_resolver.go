package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

type RunPreparer interface {
	Prepare(context.Context, profiler.PreparationRequest) (profiler.PreparedRun, error)
}

type ExecutionPolicyResolver interface {
	Resolve(context.Context, profilingrun.RunConfig) (profiler.FrozenExecutionPolicy, error)
}

// RunConfigResolver resolves the requested ProfilingRun into the exact
// executable configuration used to create a Job. It does not persist a second
// Kubernetes object; the created Job becomes the recoverable execution record.
type RunConfigResolver struct {
	preparer  RunPreparer
	execution ExecutionPolicyResolver
}

func NewRunConfigResolver(
	preparer RunPreparer,
	execution ExecutionPolicyResolver,
) (*RunConfigResolver, error) {
	if preparer == nil || execution == nil {
		return nil, errors.New("Run preparer, execution-policy resolver are required")
	}
	return &RunConfigResolver{preparer: preparer, execution: execution}, nil
}

func (r *RunConfigResolver) Resolve(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
) (ResolvedRun, error) {
	if run == nil {
		return ResolvedRun{}, errors.New("ProfilingRun is required")
	}
	request, err := ProjectRequest(run.Spec)
	if err != nil {
		return ResolvedRun{}, profiler.NewTerminalResolutionError(profiler.ResolutionReasonInvalidRunSpec, err)
	}
	prepared, err := r.preparer.Prepare(ctx, request.Preparation)
	if err != nil {
		return ResolvedRun{}, fmt.Errorf("resolve RunConfig: %w", err)
	}
	executionPolicy, err := r.execution.Resolve(ctx, prepared.RunConfig)
	if err != nil {
		return ResolvedRun{}, fmt.Errorf("resolve execution policy: %w", err)
	}
	resolved := ResolvedRun{
		Prepared: prepared, ExecutionPolicy: executionPolicy,
	}
	if err := validateResolvedRunForRun(run, resolved); err != nil {
		return ResolvedRun{}, err
	}
	return resolved, nil
}
