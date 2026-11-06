package controller

import (
	"context"
	"errors"
	"slices"
	"strconv"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	"github.com/lab-paper-code/chill/internal/profiling/profiler/projection"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func invalidAuthority(message string) error {
	return profiler.NewTerminalResolutionError(profiler.ResolutionReasonInvalidRunSpec, errors.New(message))
}

func admitRunRoot(annotations map[string]string) error {
	granted, _, refused := profilingrun.Admission(annotations)
	if refused != "" {
		return profiler.NewTerminalResolutionError(refused, errors.New("measurement admission refused"))
	}
	if !granted {
		return errRunAdmissionPending
	}
	return nil
}

func (r *RunConfigResolver) authorizedRequest(ctx context.Context, run *edgev1alpha1.ProfilingRun) (profiler.PreparationRequest, error) {
	owner := metav1.GetControllerOf(run)
	if owner == nil {
		if err := admitRunRoot(run.Annotations); err != nil {
			return profiler.PreparationRequest{}, err
		}
		request, err := ProjectRequest(run.Spec)
		if err != nil {
			return profiler.PreparationRequest{}, invalidAuthority(err.Error())
		}
		return request.Preparation, nil
	}
	if owner.APIVersion != edgev1alpha1.GroupVersion.String() || owner.Name == "" || owner.UID == "" {
		return profiler.PreparationRequest{}, invalidAuthority("invalid measurement root identity")
	}
	key := types.NamespacedName{Namespace: run.Namespace, Name: owner.Name}
	switch owner.Kind {
	case "BatchEnergyOptimization":
		return r.authorizedCampaignRequest(ctx, run, owner, key)
	case "ModelLoadedIdleBaseline":
		return r.authorizedIdleRequest(ctx, run, owner, key)
	default:
		return profiler.PreparationRequest{}, invalidAuthority("unsupported measurement root")
	}
}

// grantReader separates reader failures from grant refusals.
type grantReader struct {
	client.Reader
	readError error
}

func (r *grantReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	r.readError = r.Reader.Get(ctx, key, object, options...)
	return r.readError
}

func grantedAuthorityNode(ctx context.Context, reader client.Reader, root types.UID, name, uid string) error {
	tracked := &grantReader{Reader: reader}
	_, err := profilingrun.GrantedNode(ctx, tracked, root, name, uid)
	if err != nil && tracked.readError == nil {
		return invalidAuthority(err.Error())
	}
	return err
}

func (r *RunConfigResolver) authorizedCampaignRequest(ctx context.Context, run *edgev1alpha1.ProfilingRun, owner *metav1.OwnerReference, key types.NamespacedName) (profiler.PreparationRequest, error) {
	root := &edgev1alpha1.BatchEnergyOptimization{}
	if err := r.reader.Get(ctx, key, root); err != nil {
		if apierrors.IsNotFound(err) {
			return profiler.PreparationRequest{}, invalidAuthority("measurement root does not exist")
		}
		return profiler.PreparationRequest{}, err
	}
	if root.UID != owner.UID {
		return profiler.PreparationRequest{}, invalidAuthority("measurement root UID mismatch")
	}
	if err := admitRunRoot(root.Annotations); err != nil {
		return profiler.PreparationRequest{}, err
	}
	plan, err := r.readPlan(ctx, root.UID)
	if err != nil {
		if apierrors.IsNotFound(err) || errors.Is(err, evidencestore.ErrNotFound) {
			return profiler.PreparationRequest{}, invalidAuthority("recorded curve plan does not exist")
		}
		return profiler.PreparationRequest{}, err
	}
	shared := plan.Shared()
	trial := run.Spec.CampaignTrial
	if trial == nil || trial.Attempt < 1 || !slices.Contains(plan.PlannedBatchSizes(), int(trial.Batch)) {
		return profiler.PreparationRequest{}, invalidAuthority("child trial is outside recorded curve plan")
	}
	mode := campaign.WorkloadModeIdle
	protocol := shared.IdleProtocol
	batch := profilingrun.IdleExecutionBatch(shared.Backend, int(trial.Batch))
	if run.Spec.Measurement.WorkloadMode == profilingrun.EmpiricalCadenceFixedBatchWorkloadMode {
		mode = campaign.WorkloadModeActive
		protocol = shared.ActiveProtocol
		batch = int(trial.Batch)
	} else if run.Spec.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		return profiler.PreparationRequest{}, invalidAuthority("invalid campaign child mode")
	}
	if err := grantedAuthorityNode(ctx, r.reader, root.UID, root.Spec.State.TargetNode, shared.NodeUID); err != nil {
		return profiler.PreparationRequest{}, err
	}
	settling := int32(protocol.SettlingDurationSeconds)
	tolerance := protocol.DurationToleranceSeconds
	spec := edgev1alpha1.ProfilingRunSpec{MeasurementStateSpec: root.Spec.State,
		Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{Batch: int32(batch), WorkloadMode: protocol.WorkloadMode,
			WarmupIterations: int32(protocol.WarmupIterations), StartupWarmupIterations: int32(protocol.StartupWarmupIterations),
			SettlingDurationSeconds: &settling, DurationSeconds: int32(protocol.DurationSeconds), DurationToleranceSeconds: &tolerance,
			Workload: protocol.WorkloadName},
		MetricObservation: edgev1alpha1.ProfilingRunMetricObservationSpec{IntervalMilliseconds: int32(protocol.MetricIntervalMilliseconds), TimeoutMilliseconds: int32(protocol.MetricTimeoutMilliseconds), DurationSeconds: int32(protocol.MetricDurationSeconds)}}
	if protocol.Dataset != nil {
		spec.Measurement.Dataset = protocol.Dataset.Name
	}
	for _, k := range shared.Actor.WarmupBatchSizes {
		spec.Measurement.WarmupBatchSizes = append(spec.Measurement.WarmupBatchSizes, int32(k))
	}
	request, err := ProjectRequest(spec)
	if err != nil {
		return profiler.PreparationRequest{}, invalidAuthority(err.Error())
	}
	if mode == campaign.WorkloadModeActive {
		projection.ApplyFrozenActiveProtocol(&request.Preparation.Measurement, protocol)
		request.Preparation.Workload = request.Preparation.Measurement.WorkloadName
	}
	return request.Preparation, nil
}

func (r *RunConfigResolver) authorizedIdleRequest(ctx context.Context, run *edgev1alpha1.ProfilingRun, owner *metav1.OwnerReference, key types.NamespacedName) (profiler.PreparationRequest, error) {
	root := &edgev1alpha1.ModelLoadedIdleBaseline{}
	if err := r.reader.Get(ctx, key, root); err != nil {
		if apierrors.IsNotFound(err) {
			return profiler.PreparationRequest{}, invalidAuthority("idle measurement root does not exist")
		}
		return profiler.PreparationRequest{}, err
	}
	if root.UID != owner.UID {
		return profiler.PreparationRequest{}, invalidAuthority("idle measurement root UID mismatch")
	}
	if err := admitRunRoot(root.Annotations); err != nil {
		return profiler.PreparationRequest{}, err
	}
	attempt, err := strconv.Atoi(run.Labels["edge.dacs.io/attempt"])
	if err != nil || attempt < 1 || attempt > int(root.Spec.AttemptLimit) || run.Spec.CampaignTrial != nil || run.Spec.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		return profiler.PreparationRequest{}, invalidAuthority("invalid idle baseline child trial")
	}
	node := &corev1.Node{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: root.Spec.State.TargetNode}, node); err != nil {
		return profiler.PreparationRequest{}, err
	}
	if err := grantedAuthorityNode(ctx, r.reader, root.UID, node.Name, string(node.UID)); err != nil {
		return profiler.PreparationRequest{}, err
	}
	settling := root.Spec.Protocol.SettlingDurationSeconds
	spec := edgev1alpha1.ProfilingRunSpec{MeasurementStateSpec: root.Spec.State,
		Measurement: edgev1alpha1.ProfilingRunMeasurementSpec{Batch: root.Spec.Batch, WorkloadMode: profilingrun.IdleModelLoadedWorkloadMode,
			SettlingDurationSeconds: &settling, DurationSeconds: root.Spec.Protocol.DurationSeconds, DurationToleranceSeconds: root.Spec.Protocol.DurationToleranceSeconds}, MetricObservation: root.Spec.Protocol.MetricObservation}
	request, err := ProjectRequest(spec)
	if err != nil {
		return profiler.PreparationRequest{}, invalidAuthority(err.Error())
	}
	return request.Preparation, nil
}
