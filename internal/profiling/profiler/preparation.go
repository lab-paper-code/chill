package profiler

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	firstCPUPolicy        = "AllocatableCPUQuota"
	firstCPUPolicyVersion = "v1"
	firstExecutionPath    = "ort-cpu"
	firstRuntimeFamily    = "onnxruntime"
	firstRuntimeBackend   = "CPUExecutionProvider"
)

var ErrCPURequestExceedsAllocatable = errors.New("CPU request exceeds selected Node allocatable CPU")

type CandidateSelection struct {
	ModelSpec     string
	DeviceClass   string
	ExecutionPath string
	PowerMode     string
}

// PreparationRequest contains caller-selected Run inputs. Resolved endpoint,
// cluster identity, and execution images are deliberately not caller supplied.
type PreparationRequest struct {
	Candidate   CandidateSelection
	TargetNode  string
	CPURequest  string
	Measurement profilingrun.MeasurementContract
	Metrics     MetricObservationPolicy
}

type MetricObservationPolicy struct {
	IntervalMilliseconds int
	TimeoutMilliseconds  int
	DurationSeconds      int
}

// ResolvedCandidate is one already-evaluated static execution path and its
// exact report. The preparer independently verifies these facts against the
// report before freezing them.
type ResolvedCandidate struct {
	ReportBytes          []byte
	Inputs               profilingrun.CandidateReportInputs
	Verdict              string
	State                profilingrun.ExecutionState
	DeviceArchitecture   string
	DeviceNodeSelector   map[string]string
	DevicePowerModes     []string
	SelectedActuator     *profilingrun.ActuatorTarget
	RuntimeImage         string
	ArtifactImage        string
	ArtifactArchitecture string
	ArtifactPath         string
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
	ThermalMetrics  profilingrun.ThermalMetricContract
	TransitionPower *ResolvedTransitionPower
}

type ResolvedTransitionPower struct {
	Endpoint    string
	PowerMetric string
	Labels      map[string]string
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
}

type ActuationPolicyResolver interface {
	ResolveActuationPolicy(context.Context, ActuationSelection) (FrozenActuationPolicy, error)
}

type PreparedRun struct {
	Intent               profilingrun.Intent
	CandidateReportBytes []byte
	ActuationPolicy      *FrozenActuationPolicy
}

// ValidateActuationBinding checks the configured policy against the frozen
// material selection. Downstream execution consumes the selected policy
// without reselecting it from mutable configuration.
func (r PreparedRun) ValidateActuationBinding() error {
	if r.Intent.Actuation == nil {
		if r.ActuationPolicy != nil {
			return errors.New("fixed prepared Run cannot contain an actuation policy")
		}
		return nil
	}
	if r.ActuationPolicy == nil {
		return errors.New("actuated prepared Run requires a frozen actuation policy")
	}
	if err := r.ActuationPolicy.Validate(); err != nil {
		return err
	}
	return validateActuationPolicySelection(r.ActuationPolicy.Policy, ActuationSelection{
		DeviceClass: r.Intent.State.DeviceClass,
		PowerMode:   r.Intent.State.PowerMode,
		Actuator:    r.Intent.Actuation.Actuator,
	})
}

// Preparer resolves current facts through narrow ports and freezes one Run
// intent. It performs no Kubernetes or storage operation.
type Preparer struct {
	candidates CandidateResolver
	nodes      NodeResolver
	metrics    MetricSourceResolver
	workloads  WorkloadObservationResolver
	actuation  ActuationPolicyResolver
}

func NewPreparer(
	candidates CandidateResolver,
	nodes NodeResolver,
	metrics MetricSourceResolver,
	workloads WorkloadObservationResolver,
	actuation ActuationPolicyResolver,
) (*Preparer, error) {
	if candidates == nil || nodes == nil || metrics == nil || workloads == nil || actuation == nil {
		return nil, errors.New("candidate, Node, metric-source, workload-observation, and actuation-policy resolvers are required")
	}
	return &Preparer{
		candidates: candidates, nodes: nodes, metrics: metrics, workloads: workloads, actuation: actuation,
	}, nil
}

func (p *Preparer) Prepare(ctx context.Context, request PreparationRequest) (PreparedRun, error) {
	if err := request.Validate(); err != nil {
		return PreparedRun{}, err
	}
	candidate, err := p.candidates.ResolveCandidate(ctx, request.Candidate)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve static candidate: %w", err)
	}
	if err := validateResolvedCandidate(request.Candidate, candidate); err != nil {
		return PreparedRun{}, err
	}
	if err := validateCandidateMaterialBinding(candidate); err != nil {
		return PreparedRun{}, fmt.Errorf("validate resolved candidate material: %w", err)
	}
	node, err := p.nodes.ResolveNode(ctx, NodeSelection{
		Name:                request.TargetNode,
		DeviceArchitecture:  candidate.DeviceArchitecture,
		DeviceClassSelector: candidate.DeviceNodeSelector,
	})
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve target Node: %w", err)
	}
	if err := validateCPURequestFitsNode(request.CPURequest, node.AllocatableCPU); err != nil {
		return PreparedRun{}, err
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
	intent, err := p.freeze(request, candidate, node, metrics, workload, actuation)
	if err != nil {
		return PreparedRun{}, err
	}
	if err := profilingrun.ValidateCandidateReport(candidate.ReportBytes, intent); err != nil {
		return PreparedRun{}, fmt.Errorf("validate resolved candidate report: %w", err)
	}
	prepared := PreparedRun{
		Intent:               intent,
		CandidateReportBytes: append([]byte(nil), candidate.ReportBytes...),
		ActuationPolicy:      policy,
	}
	if err := prepared.ValidateActuationBinding(); err != nil {
		return PreparedRun{}, fmt.Errorf("validate frozen actuation binding: %w", err)
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

func validateCPURequestFitsNode(request, allocatable string) error {
	requestQuantity, err := resource.ParseQuantity(request)
	if err != nil || requestQuantity.Sign() <= 0 {
		return errors.New("CPU request must be a positive Kubernetes quantity")
	}
	allocatableQuantity, err := resource.ParseQuantity(allocatable)
	if err != nil || allocatableQuantity.Sign() <= 0 {
		return errors.New("resolved Node allocatable CPU must be a positive Kubernetes quantity")
	}
	if requestQuantity.Cmp(allocatableQuantity) > 0 {
		return fmt.Errorf("%w: request %s, allocatable %s", ErrCPURequestExceedsAllocatable, request, allocatable)
	}
	return nil
}

func (p *Preparer) freeze(
	request PreparationRequest,
	candidate ResolvedCandidate,
	node profilingrun.NodeSnapshot,
	metrics ResolvedMetricSource,
	workload ResolvedWorkloadObservation,
	actuation *profilingrun.ActuationContract,
) (profilingrun.Intent, error) {
	if node.Name != request.TargetNode {
		return profilingrun.Intent{}, errors.New("resolved Node identity does not match requested target")
	}
	cpu, err := strconv.Atoi(node.AllocatableCPU)
	if err != nil || cpu < 1 {
		return profilingrun.Intent{}, errors.New("resolved Node allocatable CPU must be a positive integer")
	}
	cpuContract := profilingrun.CPUContract{
		Policy: firstCPUPolicy, PolicyVersion: firstCPUPolicyVersion,
		Request: request.CPURequest, Limit: node.AllocatableCPU,
	}
	if candidate.State.RuntimeFamily == firstRuntimeFamily && candidate.State.Backend == firstRuntimeBackend {
		cpuContract.ORTIntraOpThreads = cpu
		cpuContract.ORTInterOpThreads = 1
	}
	intent := profilingrun.Intent{
		SchemaVersion: profilingrun.SchemaVersion,
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
		TargetNode:  node,
		CPU:         cpuContract,
		Measurement: request.Measurement,
		Metrics: profilingrun.MetricContract{
			SourceKind: metrics.Kind, NodeName: node.Name, Endpoint: metrics.Endpoint, PowerMetric: metrics.PowerMetric,
			ThermalMetrics:       metrics.ThermalMetrics,
			IntervalMilliseconds: request.Metrics.IntervalMilliseconds,
			TimeoutMilliseconds:  request.Metrics.TimeoutMilliseconds, DurationSeconds: request.Metrics.DurationSeconds,
		},
		Workload:  profilingrun.CurrentWorkloadContract(),
		Actuation: actuation,
	}
	if err := intent.Validate(); err != nil {
		return profilingrun.Intent{}, fmt.Errorf("validate frozen Run intent: %w", err)
	}
	return intent, nil
}

func validateResolvedCandidate(request CandidateSelection, candidate ResolvedCandidate) error {
	if candidate.State.Model != request.ModelSpec || candidate.State.DeviceClass != request.DeviceClass {
		return errors.New("resolved candidate does not match requested catalog objects")
	}
	if candidate.State.PowerMode != request.PowerMode {
		return errors.New("resolved candidate power mode does not match request")
	}
	switch {
	case candidate.State.RuntimeFamily == firstRuntimeFamily &&
		candidate.State.Backend == firstRuntimeBackend:
	case candidate.State.RuntimeFamily == profilingrun.RKNNLiteRuntimeFamily &&
		candidate.State.Backend == profilingrun.RKNNLiteBackend &&
		candidate.State.HostContract != nil:
	default:
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

func validateCandidateMaterialBinding(candidate ResolvedCandidate) error {
	if candidate.State.ArtifactPath != candidate.ArtifactPath {
		return errors.New("resolved candidate artifact path does not match execution state")
	}
	report, err := profilingrun.DecodeCandidateReport(candidate.ReportBytes)
	if err != nil {
		return err
	}
	checks := []struct{ name, want, got string }{
		{"DeviceClass architecture", candidate.DeviceArchitecture, report.DeviceClassFacts.Architecture},
		{"artifact image", candidate.ArtifactImage, report.ArtifactSupply.Image},
		{"artifact architecture", candidate.ArtifactArchitecture, report.ArtifactSupply.Architecture},
		{"artifact path", candidate.State.ArtifactPath, report.ArtifactSupply.Path},
		{"metric-observer image", candidate.MetricObserverImage, report.MetricObserverImage},
		{"power mode", candidate.State.PowerMode, report.Selection.PowerMode},
	}
	for _, check := range checks {
		if check.want != check.got {
			return fmt.Errorf("candidate report %s mismatch", check.name)
		}
	}
	if !maps.Equal(candidate.DeviceNodeSelector, report.DeviceClassFacts.NodeSelector) {
		return errors.New("candidate report DeviceClass nodeSelector mismatch")
	}
	if !slices.Equal(candidate.DevicePowerModes, report.DeviceClassFacts.PowerModes) {
		return errors.New("candidate report DeviceClass powerModes mismatch")
	}
	if !actuatorTargetsMatch(candidate.SelectedActuator, report.Selection.Actuator) {
		return errors.New("candidate report selected actuator mismatch")
	}
	return nil
}

func actuatorTargetsMatch(left, right *profilingrun.ActuatorTarget) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return reflect.DeepEqual(left, right)
}

func (r PreparationRequest) Validate() error {
	for name, value := range map[string]string{
		"candidate ModelSpec":     r.Candidate.ModelSpec,
		"candidate DeviceClass":   r.Candidate.DeviceClass,
		"candidate executionPath": r.Candidate.ExecutionPath,
		"candidate powerMode":     r.Candidate.PowerMode,
		"target Node":             r.TargetNode,
		"CPU request":             r.CPURequest,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	cpuRequest, err := resource.ParseQuantity(r.CPURequest)
	if err != nil || cpuRequest.Sign() <= 0 {
		return errors.New("CPU request must be a positive Kubernetes quantity")
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
	for name, value := range map[string]string{
		"measurement workloadMode":     r.Measurement.WorkloadMode,
		"measurement inputGenerator":   r.Measurement.InputGenerator,
		"measurement outputValidation": r.Measurement.OutputValidation,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return nil
}
