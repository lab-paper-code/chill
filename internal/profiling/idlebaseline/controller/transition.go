package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/metadata"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

// ensureRequiredState establishes the parent state before its first Run: it
// writes the power mode on the Node granted to the baseline and reports the
// agent's progress. failed is a failed apply or one past the policy bound.
func (r *Reconciler) ensureRequiredState(
	ctx context.Context,
	parent *edgev1alpha1.ModelLoadedIdleBaseline,
) (established, failed bool, err error) {
	prototype, err := buildRun(parent, 1)
	if err != nil {
		return false, false, err
	}
	requested, err := profilercontroller.ProjectRequest(prototype.Spec)
	if err != nil {
		return false, false, err
	}
	prepared, err := r.preparer.Prepare(ctx, requested.Preparation)
	if err != nil {
		return false, false, fmt.Errorf("prepare idle baseline state: %w", err)
	}
	if err := prepared.ValidateActuationBinding(); err != nil {
		return false, false, fmt.Errorf("validate idle baseline actuation binding: %w", err)
	}
	if prepared.RunConfig.Actuation == nil {
		return true, false, nil
	}
	target := prepared.RunConfig.TargetNode
	if target.Name != parent.Spec.State.TargetNode || target.UID == "" ||
		prepared.RunConfig.State.DeviceClass != parent.Spec.State.Candidate.DeviceClass ||
		prepared.RunConfig.State.PowerMode != parent.Spec.State.Candidate.PowerMode {
		return false, false, errors.New("prepared actuation does not match the idle baseline state")
	}
	node, err := profilingrun.GrantedNode(ctx, r.client, parent.UID, target.Name, target.UID)
	if err != nil {
		return false, false, err
	}
	mode := prepared.RunConfig.Actuation.PowerMode
	if node.Labels[metadata.PowerMode] != mode {
		return false, false, metadata.RequestPowerMode(ctx, r.client, node, mode, r.now())
	}
	maxDuration := time.Duration(prepared.ActuationPolicy.Policy.MaxActuationDurationSeconds) * time.Second
	done, outcome := metadata.PowerModeOutcome(node, mode, maxDuration, r.now())
	return done, outcome != nil, nil
}
