package profiler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

const firstExecutionPath = "ort-cpu"

// CandidateSelection names the catalog objects one Run measures. Dataset is
// empty for idle measurement. Node is the Run's target Node; it selects the
// execution declaration bound to that Node's physical device.
type CandidateSelection struct {
	ModelSpec     string
	DeviceClass   string
	Node          string
	ExecutionPath string
	PowerMode     string
	Preprocessor  string
	Dataset       string
}

// PreparationRequest contains caller-selected Run inputs. Resolved endpoint,
// cluster identity, and execution images are deliberately not caller supplied.
type PreparationRequest struct {
	Candidate   CandidateSelection
	TargetNode  string
	Workload    string
	Measurement profilingrun.MeasurementContract
	Metrics     MetricObservationPolicy
}

type MetricObservationPolicy struct {
	IntervalMilliseconds int
	TimeoutMilliseconds  int
	DurationSeconds      int
}

// ResolvedCandidate contains the resolver's selected executable facts and report.
// State carries the selected preprocessor artifact; Dataset is present when
// the resolver found it compatible with the preprocessor input contract.
type ResolvedCandidate struct {
	ReportBytes          []byte
	Inputs               profilingrun.CandidateReportInputs
	Verdict              string
	State                profilingrun.ExecutionState
	Dataset              *profilingrun.DatasetContract
	DeviceArchitecture   string
	DeviceNodeSelector   map[string]string
	DevicePowerModes     []string
	SelectedActuator     *profilingrun.ActuatorTarget
	RuntimeImage         string
	ArtifactImage        string
	ArtifactArchitecture string
	MetricObserverImage  string
}

// NodeSelection binds the requested Node to facts from the exact DeviceClass
// snapshot used by static compatibility evaluation.
type NodeSelection struct {
	Name                string
	DeviceArchitecture  string
	DeviceClassSelector map[string]string
}

type ResolvedMetricSource struct {
	Kind            string
	Endpoint        string
	PowerMetric     string
	MetricLabels    map[string]string
	ThermalEndpoint string
	ThermalMetrics  profilingrun.ThermalMetricContract
}

type ResolvedWorkloadObservation struct {
	Image string
}

type CandidateResolver interface {
	ResolveCandidate(context.Context, CandidateSelection) (ResolvedCandidate, error)
}

type NodeResolver interface {
	ResolveNode(context.Context, NodeSelection) (profilingrun.NodeSnapshot, error)
}

type MetricSourceResolver interface {
	ResolveMetricSource(context.Context, string) (ResolvedMetricSource, error)
}

type WorkloadObservationResolver interface {
	ResolveWorkloadObservation(context.Context) (ResolvedWorkloadObservation, error)
	ResolveMeasurementWorkload(context.Context, string, CandidateSelection) (profilingrun.MeasurementContract, error)
}

type ActuationPolicyResolver interface {
	ResolveActuationPolicy(context.Context, ActuationSelection) (FrozenActuationPolicy, error)
}

type ExecutionPolicyResolver interface {
	Resolve(context.Context, profilingrun.RunConfig) (FrozenExecutionPolicy, error)
}

type PreparedRun struct {
	RunConfig       profilingrun.RunConfig
	ActuationPolicy *FrozenActuationPolicy
}

// ValidateActuationBinding checks the selected policy against the RunConfig
// material selection. Downstream execution consumes the selected policy
// without reselecting it from mutable configuration.
func (r PreparedRun) ValidateActuationBinding() error {
	if r.RunConfig.Actuation == nil {
		if r.ActuationPolicy != nil {
			return errors.New("fixed prepared Run cannot contain an actuation policy")
		}
		return nil
	}
	if r.ActuationPolicy == nil {
		return errors.New("actuated prepared Run requires a selected actuation policy")
	}
	if err := r.ActuationPolicy.Validate(); err != nil {
		return err
	}
	return validateActuationPolicySelection(r.ActuationPolicy.Policy, ActuationSelection{
		DeviceClass: r.RunConfig.State.DeviceClass,
		PowerMode:   r.RunConfig.State.PowerMode,
		Actuator:    r.RunConfig.Actuation.Actuator,
	})
}

// Preparer resolves current facts through narrow ports into one executable
// RunConfig. It performs no Kubernetes or storage operation.
type Preparer struct {
	candidates CandidateResolver
	nodes      NodeResolver
	metrics    MetricSourceResolver
	workloads  WorkloadObservationResolver
	actuation  ActuationPolicyResolver
	execution  ExecutionPolicyResolver
	// ResolveLoadOrigin observes the installation's actual serving gateway.
	ResolveLoadOrigin func(context.Context, string) (string, profilingrun.LoadOriginGatewayPodContract, error)
}

func NewPreparer(
	candidates CandidateResolver,
	nodes NodeResolver,
	metrics MetricSourceResolver,
	workloads WorkloadObservationResolver,
	actuation ActuationPolicyResolver,
	execution ExecutionPolicyResolver,
) (*Preparer, error) {
	if candidates == nil || nodes == nil || metrics == nil || workloads == nil ||
		actuation == nil || execution == nil {
		return nil, errors.New("candidate, Node, metric, workload, actuation and execution resolvers are required")
	}
	return &Preparer{
		candidates: candidates, nodes: nodes, metrics: metrics, workloads: workloads, actuation: actuation, execution: execution,
	}, nil
}

// FreezePhaseDuration expands the existing observation window for every named
// active phase while retaining the caller's observation margin.
func (r *PreparationRequest) FreezePhaseDuration() error {
	if r.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		return nil
	}
	points := r.Measurement.OperatingPoints
	if err := points.Validate(); err != nil {
		return err
	}
	duration := points.PhaseDurationSeconds*(len(points.Utilizations)+2) + points.PostLoadDurationSeconds
	margin := max(0, r.Metrics.DurationSeconds-r.Measurement.DurationSeconds)
	if margin > int(^uint32(0)>>1)-duration {
		return errors.New("active phase observation duration exceeds supported seconds")
	}
	r.Measurement.DurationSeconds = duration
	r.Metrics.DurationSeconds = max(r.Metrics.DurationSeconds, duration+margin)
	return nil
}

func (p *Preparer) Prepare(ctx context.Context, request PreparationRequest) (PreparedRun, error) {
	if err := request.FreezePhaseDuration(); err != nil {
		return PreparedRun{}, err
	}
	if err := request.Validate(); err != nil {
		return PreparedRun{}, err
	}
	if request.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode && request.Measurement.WorkloadUID == "" {
		stop, err := p.workloads.ResolveMeasurementWorkload(ctx, request.Workload, request.Candidate)
		if err != nil {
			return PreparedRun{}, fmt.Errorf("resolve measurement Workload: %w", err)
		}
		request.Measurement.SLOPercentile = stop.SLOPercentile
		request.Measurement.MinServiceSamples = stop.MinServiceSamples
		request.Measurement.SLOTargetLatencyNanos = stop.SLOTargetLatencyNanos
		request.Measurement.WorkloadName = stop.WorkloadName
		request.Measurement.WorkloadUID = stop.WorkloadUID
		request.Measurement.WorkloadResourceVersion = stop.WorkloadResourceVersion
	}
	candidate, err := p.candidates.ResolveCandidate(ctx, request.Candidate)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve static candidate: %w", err)
	}
	if err := validateResolvedCandidate(request.Candidate, candidate); err != nil {
		return PreparedRun{}, err
	}
	node, err := p.nodes.ResolveNode(ctx, NodeSelection{
		Name:                request.TargetNode,
		DeviceArchitecture:  candidate.DeviceArchitecture,
		DeviceClassSelector: candidate.DeviceNodeSelector,
	})
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve target Node: %w", err)
	}
	if candidate.State.Preprocessor.Architecture != node.Architecture {
		return PreparedRun{}, fmt.Errorf("preprocessor artifact architecture %q does not match target Node architecture %q",
			candidate.State.Preprocessor.Architecture, node.Architecture)
	}
	metrics, err := p.metrics.ResolveMetricSource(ctx, request.TargetNode)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve metric source: %w", err)
	}
	workload, err := p.workloads.ResolveWorkloadObservation(ctx)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve workload observation: %w", err)
	}

	actuation, policy, err := p.resolveActuation(ctx, candidate)
	if err != nil {
		return PreparedRun{}, err
	}
	report, err := profilingrun.DecodeCandidateReport(candidate.ReportBytes)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("decode resolved candidate report: %w", err)
	}
	runConfig, err := p.freeze(
		request, candidate, report.RuntimeDelivery(), node, metrics, workload, actuation,
	)
	if err != nil {
		return PreparedRun{}, err
	}
	if runConfig.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		if p.ResolveLoadOrigin == nil {
			return PreparedRun{}, errors.New("active measurement requires a serving gateway load-origin resolver")
		}
		originNode, gateway, err := p.ResolveLoadOrigin(ctx, runConfig.TargetNode.Name)
		if err != nil {
			return PreparedRun{}, fmt.Errorf("resolve serving gateway load origin: %w", err)
		}
		runConfig.LoadOriginNode = originNode
		runConfig.LoadOriginGatewayPod = &gateway
	}
	executionPolicy, err := p.execution.Resolve(ctx, runConfig)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve actor memory envelope: %w", err)
	}
	runConfig.Actor.MemoryRequestBytes = executionPolicy.Policy.RuntimeMemoryRequestBytes
	runConfig.Actor.MemoryLimitBytes = executionPolicy.Policy.RuntimeMemoryLimitBytes
	if runConfig.Measurement.OperatingPoints != nil {
		points := *runConfig.Measurement.OperatingPoints
		if points.QueueBytesCap != 0 && points.QueueBytesCap != min(executionPolicy.Policy.MaxInputBytes, int64(64<<20)) {
			return PreparedRun{}, InvalidExecutionPolicy("selected queue capacity differs from the frozen measurement protocol")
		}
		points.QueueBytesCap = min(executionPolicy.Policy.MaxInputBytes, int64(64<<20))
		if points.FramedDecodedRequestUpperBytes > points.QueueBytesCap {
			return PreparedRun{}, InvalidExecutionPolicy("measurement queue cannot hold one framed request")
		}
		runConfig.Measurement.OperatingPoints = &points
		runConfig.Actor.MaxQueueBytes = int(points.QueueBytesCap)
	}
	if _, err := DeriveExecutionPlan(runConfig, executionPolicy); err != nil {
		return PreparedRun{}, err
	}
	prepared := PreparedRun{
		RunConfig:       runConfig,
		ActuationPolicy: policy,
	}
	return prepared, nil
}

func (p *Preparer) resolveActuation(
	ctx context.Context,
	candidate ResolvedCandidate,
) (*profilingrun.ActuationContract, *FrozenActuationPolicy, error) {
	if candidate.SelectedActuator == nil {
		return nil, nil, nil
	}
	selection := ActuationSelection{
		DeviceClass: candidate.State.DeviceClass,
		PowerMode:   candidate.State.PowerMode,
		Actuator:    *cloneResolvedActuatorTarget(candidate.SelectedActuator),
	}
	policy, err := p.actuation.ResolveActuationPolicy(ctx, selection)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve actuation policy: %w", err)
	}
	if err := validateActuationPolicySelection(policy.Policy, selection); err != nil {
		return nil, nil, fmt.Errorf("validate selected actuation policy: %w", err)
	}
	return &profilingrun.ActuationContract{
		PowerMode:  candidate.State.PowerMode,
		Actuator:   selection.Actuator,
		PolicyName: policy.Policy.Name,
	}, &policy, nil
}

func cloneResolvedActuatorTarget(source *profilingrun.ActuatorTarget) *profilingrun.ActuatorTarget {
	if source == nil {
		return nil
	}
	clone := *source
	if source.JetsonNVPModel != nil {
		target := *source.JetsonNVPModel
		clone.JetsonNVPModel = &target
	}
	if source.NvidiaLockedGPUClock != nil {
		target := *source.NvidiaLockedGPUClock
		if source.NvidiaLockedGPUClock.Mode != nil {
			mode := *source.NvidiaLockedGPUClock.Mode
			target.Mode = &mode
		}
		clone.NvidiaLockedGPUClock = &target
	}
	return &clone
}

func (p *Preparer) freeze(
	request PreparationRequest,
	candidate ResolvedCandidate,
	delivery profilingrun.CandidateRuntimeDelivery,
	node profilingrun.NodeSnapshot,
	metrics ResolvedMetricSource,
	workload ResolvedWorkloadObservation,
	actuation *profilingrun.ActuationContract,
) (profilingrun.RunConfig, error) {
	if node.Name != request.TargetNode {
		return profilingrun.RunConfig{}, errors.New("resolved Node identity does not match requested target")
	}
	measurement := request.Measurement
	if candidate.Dataset != nil {
		dataset := *candidate.Dataset
		measurement.Dataset = &dataset
	}
	config := profilingrun.RunConfig{
		SchemaVersion: profilingrun.RunConfigSchemaVersion,
		Candidate: profilingrun.CandidateIdentity{
			Inputs: candidate.Inputs, ExecutionPath: request.Candidate.ExecutionPath, Verdict: candidate.Verdict,
		},
		State: candidate.State,
		Images: profilingrun.Images{
			Runtime:          candidate.RuntimeImage,
			Artifact:         candidate.ArtifactImage,
			MetricObserver:   candidate.MetricObserverImage,
			WorkloadObserver: workload.Image,
		},
		Delivery:    delivery,
		TargetNode:  node,
		Measurement: measurement,
		Metrics: profilingrun.MetricContract{
			SourceKind: metrics.Kind, NodeName: node.Name, Endpoint: metrics.Endpoint, PowerMetric: metrics.PowerMetric,
			MetricLabels:         maps.Clone(metrics.MetricLabels),
			ThermalEndpoint:      metrics.ThermalEndpoint,
			ThermalMetrics:       metrics.ThermalMetrics,
			IntervalMilliseconds: request.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  request.Metrics.TimeoutMilliseconds, DurationSeconds: request.Metrics.DurationSeconds,
		},
		Workload:  profilingrun.CurrentWorkloadContract(),
		Actuation: actuation,
	}
	config.Actor = config.ActorConfig()
	return config, nil
}

func validateResolvedCandidate(request CandidateSelection, candidate ResolvedCandidate) error {
	if candidate.State.Model != request.ModelSpec || candidate.State.DeviceClass != request.DeviceClass {
		return errors.New("resolved candidate does not match requested catalog objects")
	}
	if candidate.State.PowerMode != request.PowerMode {
		return errors.New("resolved candidate power mode does not match request")
	}
	if candidate.State.Preprocessor.Name != request.Preprocessor {
		return errors.New("resolved candidate preprocessor does not match request")
	}
	if (candidate.Dataset == nil && request.Dataset != "") ||
		(candidate.Dataset != nil && candidate.Dataset.Name != request.Dataset) {
		return errors.New("resolved candidate dataset does not match request")
	}
	if candidate.State.RuntimeFamily != profilingrun.FirstfloorRuntimeFamily ||
		(candidate.State.Backend != profilingrun.FirstfloorORTCPUBackend &&
			candidate.State.Backend != profilingrun.FirstfloorORTCUDABackend &&
			candidate.State.Backend != profilingrun.FirstfloorRKNNLiteBackend) ||
		candidate.State.Firstfloor == nil {
		return errors.New("resolved candidate runtime/backend contract is not supported")
	}
	if strings.TrimSpace(candidate.DeviceArchitecture) == "" || len(candidate.DeviceNodeSelector) == 0 {
		return errors.New("resolved DeviceClass architecture and Node selector are required")
	}
	if len(candidate.DevicePowerModes) == 0 || !slices.Contains(candidate.DevicePowerModes, candidate.State.PowerMode) {
		return errors.New("resolved DeviceClass does not retain the selected power mode")
	}
	if candidate.State.PowerMode == profilingrun.FixedPowerMode {
		if candidate.SelectedActuator != nil {
			return errors.New("resolved fixed power mode cannot contain an actuator target")
		}
	} else {
		if candidate.SelectedActuator == nil {
			return errors.New("resolved actuated power mode requires an actuator target")
		}
		if err := candidate.SelectedActuator.Validate(); err != nil {
			return fmt.Errorf("validate resolved actuator target: %w", err)
		}
	}
	return nil
}

func (r PreparationRequest) Validate() error {
	for name, value := range map[string]string{
		"candidate ModelSpec":     r.Candidate.ModelSpec,
		"candidate DeviceClass":   r.Candidate.DeviceClass,
		"candidate executionPath": r.Candidate.ExecutionPath,
		"candidate powerMode":     r.Candidate.PowerMode,
		"candidate preprocessor":  r.Candidate.Preprocessor,
		"target Node":             r.TargetNode,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if r.Candidate.Node != r.TargetNode {
		return errors.New("candidate Node must be the target Node")
	}
	if r.Metrics.IntervalMilliseconds < 1 || r.Metrics.TimeoutMilliseconds < 1 ||
		r.Metrics.TimeoutMilliseconds >= r.Metrics.IntervalMilliseconds ||
		r.Metrics.DurationSeconds < r.Measurement.DurationSeconds {
		return errors.New("metric observation policy is invalid or does not cover the measurement")
	}
	if r.Measurement.Batch < 1 || r.Measurement.WarmupIterations < 0 ||
		r.Measurement.DurationSeconds < 1 {
		return errors.New("measurement batch, duration, and warm-up iterations must be valid")
	}
	if err := r.Measurement.ValidateDurationTolerance(); err != nil {
		return err
	}
	if r.Measurement.WorkloadMode != profilingrun.IdleModelLoadedWorkloadMode {
		if err := r.Measurement.OperatingPoints.Validate(); err != nil {
			return err
		}
	} else if r.Measurement.OperatingPoints != nil {
		return errors.New("idle measurement cannot contain operatingPoints")
	}
	workloadMode := r.Measurement.WorkloadMode
	if strings.TrimSpace(workloadMode) == "" || strings.TrimSpace(workloadMode) != workloadMode {
		return errors.New("measurement workloadMode is required without surrounding whitespace")
	}
	if r.Measurement.Dataset != nil {
		return errors.New("measurement dataset is frozen from the resolved catalog, not requested")
	}
	if active := workloadMode != profilingrun.IdleModelLoadedWorkloadMode; active != (r.Candidate.Dataset != "") ||
		strings.TrimSpace(r.Candidate.Dataset) != r.Candidate.Dataset {
		return errors.New("active measurement requires a dataset and idle measurement has none")
	}
	if active := workloadMode != profilingrun.IdleModelLoadedWorkloadMode; active != (r.Workload != "") {
		return errors.New("active measurement requires a Workload reference and idle measurement has none")
	}
	return r.validateFrozenWorkload()
}

func (r PreparationRequest) validateFrozenWorkload() error {
	measurement := r.Measurement
	if measurement.WorkloadUID != "" || measurement.WorkloadResourceVersion != "" ||
		measurement.SLOPercentile != 0 || measurement.SLOTargetLatencyNanos != 0 || measurement.MinServiceSamples != 0 {
		minimum, err := profilingrun.MinimumServiceSamples(measurement.SLOPercentile)
		if err != nil || measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode ||
			strings.TrimSpace(measurement.WorkloadUID) == "" ||
			strings.TrimSpace(measurement.WorkloadUID) != measurement.WorkloadUID ||
			measurement.WorkloadName != r.Workload || measurement.SLOTargetLatencyNanos <= 0 ||
			measurement.MinServiceSamples != minimum {
			return errors.New("frozen Workload requires its source UID, SLO percentile, positive target, and derived native sample count")
		}
	}
	return nil
}
