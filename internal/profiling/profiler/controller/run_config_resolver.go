package controller

import (
	"context"
	"errors"
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type RunPreparer interface {
	Prepare(context.Context, profiler.PreparationRequest) (profiler.PreparedRun, error)
}

var errRunAdmissionPending = errors.New("measurement admission pending")

// RunConfigResolver authenticates the request before resolving executable state.
type RunConfigResolver struct {
	preparer  RunPreparer
	execution profiler.ExecutionPolicyResolver
	reader    client.Reader
}

func NewRunConfigResolver(preparer RunPreparer, execution profiler.ExecutionPolicyResolver, reader client.Reader) (*RunConfigResolver, error) {
	if preparer == nil || execution == nil || reader == nil {
		return nil, errors.New("Run preparer, execution-policy resolver and authoritative reader are required")
	}
	return &RunConfigResolver{preparer: preparer, execution: execution, reader: reader}, nil
}

func (r *RunConfigResolver) Resolve(ctx context.Context, run *edgev1alpha1.ProfilingRun) (ResolvedRun, error) {
	if run == nil {
		return ResolvedRun{}, errors.New("ProfilingRun is required")
	}
	request, err := r.authorizedRequest(ctx, run)
	if err != nil {
		return ResolvedRun{}, err
	}
	prepared, err := r.preparer.Prepare(ctx, request)
	if err != nil {
		return ResolvedRun{}, fmt.Errorf("resolve RunConfig: %w", err)
	}
	executionPolicy, err := r.execution.Resolve(ctx, prepared.RunConfig)
	if err != nil {
		return ResolvedRun{}, fmt.Errorf("resolve execution policy: %w", err)
	}
	resolved := ResolvedRun{Prepared: prepared, ExecutionPolicy: executionPolicy}
	if err := resolved.Validate(); err != nil {
		return ResolvedRun{}, err
	}
	if err := validateSelectedExecutionPolicy(prepared, executionPolicy); err != nil {
		return ResolvedRun{}, err
	}
	return resolved, nil
}
