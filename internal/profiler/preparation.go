package profiler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/lab-paper-code/chill/internal/profilingrun"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	firstCPUPolicy        = "AllocatableCPUQuota"
	firstCPUPolicyVersion = "v1"
	firstExecutionPath    = "ort-cpu"
	firstRuntimeFamily    = "onnxruntime"
	firstRuntimeBackend   = "CPUExecutionProvider"
	firstPowerMode        = "fixed"
)

var ErrCPURequestExceedsAllocatable = errors.New("CPU request exceeds selected Node allocatable CPU")

type CandidateSelection struct {
	ModelSpec     string
	DeviceClass   string
	ExecutionPath string
	PowerMode     string
}

// PreparationRequest contains caller-selected Run inputs. Resolved endpoint,
// cluster identity, content digests, and execution images are deliberately not
// caller supplied.
type PreparationRequest struct {
	Candidate   CandidateSelection
	TargetNode  string
	CPURequest  string
	Measurement profilingrun.MeasurementContract
	Power       PowerObservationPolicy
}

type PowerObservationPolicy struct {
	IntervalMilliseconds int
	TimeoutMilliseconds  int
	DurationSeconds      int
}

// ResolvedCandidate is one already-evaluated static execution path and its
// exact report. The preparer independently verifies these facts against the
// report before freezing them.
type ResolvedCandidate struct {
	ReportBytes                     []byte
	ModelSpecContentDigest          string
	DeviceClassContentDigest        string
	RuntimeDeclarationContentDigest string
	Verdict                         string
	State                           profilingrun.ExecutionState
	DeviceArchitecture              string
	DeviceNodeSelector              map[string]string
	DevicePowerModes                []string
	RuntimeImage                    string
	ArtifactImage                   string
	ArtifactArchitecture            string
	ArtifactPath                    string
	ArtifactDigest                  string
}

// NodeSelection binds the requested Node to facts from the exact DeviceClass
// snapshot used by static compatibility evaluation.
type NodeSelection struct {
	Name                string
	DeviceArchitecture  string
	DeviceClassSelector map[string]string
}

type ResolvedPowerSource struct {
	Kind     string
	Endpoint string
	Metric   string
}

type CandidateResolver interface {
	ResolveCandidate(context.Context, CandidateSelection) (ResolvedCandidate, error)
}

type NodeResolver interface {
	ResolveNode(context.Context, NodeSelection) (profilingrun.NodeSnapshot, error)
}

type PowerSourceResolver interface {
	ResolvePowerSource(context.Context, string) (ResolvedPowerSource, error)
}

type PreparationConfig struct {
	PowerObserverImage string
	CPUPolicy          string
	CPUPolicyVersion   string
}

type PreparedRun struct {
	Intent               profilingrun.Intent
	IntentDigest         string
	CandidateReportBytes []byte
}

// Preparer resolves current facts through narrow ports and freezes one Run
// intent. It performs no Kubernetes or storage operation.
type Preparer struct {
	candidates CandidateResolver
	nodes      NodeResolver
	power      PowerSourceResolver
	config     PreparationConfig
}

func NewPreparer(
	candidates CandidateResolver,
	nodes NodeResolver,
	power PowerSourceResolver,
	config PreparationConfig,
) (*Preparer, error) {
	if candidates == nil || nodes == nil || power == nil {
		return nil, errors.New("candidate, Node, and power-source resolvers are required")
	}
	for name, value := range map[string]string{
		"power observer image": config.PowerObserverImage,
		"CPU policy":           config.CPUPolicy,
		"CPU policy version":   config.CPUPolicyVersion,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	if config.CPUPolicy != firstCPUPolicy || config.CPUPolicyVersion != firstCPUPolicyVersion {
		return nil, fmt.Errorf("first path supports only CPU policy %s/%s", firstCPUPolicy, firstCPUPolicyVersion)
	}
	return &Preparer{candidates: candidates, nodes: nodes, power: power, config: config}, nil
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
	power, err := p.power.ResolvePowerSource(ctx, request.TargetNode)
	if err != nil {
		return PreparedRun{}, fmt.Errorf("resolve power source: %w", err)
	}

	intent, err := p.freeze(request, candidate, node, power)
	if err != nil {
		return PreparedRun{}, err
	}
	if err := profilingrun.ValidateCandidateReport(candidate.ReportBytes, intent); err != nil {
		return PreparedRun{}, fmt.Errorf("validate resolved candidate report: %w", err)
	}
	digest, err := intent.Digest()
	if err != nil {
		return PreparedRun{}, fmt.Errorf("digest frozen Run intent: %w", err)
	}
	return PreparedRun{
		Intent:               intent,
		IntentDigest:         digest,
		CandidateReportBytes: append([]byte(nil), candidate.ReportBytes...),
	}, nil
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
	power ResolvedPowerSource,
) (profilingrun.Intent, error) {
	if node.Name != request.TargetNode {
		return profilingrun.Intent{}, errors.New("resolved Node identity does not match requested target")
	}
	cpu, err := strconv.Atoi(node.AllocatableCPU)
	if err != nil || cpu < 1 {
		return profilingrun.Intent{}, errors.New("resolved Node allocatable CPU must be a positive integer")
	}
	reportSum := sha256.Sum256(candidate.ReportBytes)
	intent := profilingrun.Intent{
		SchemaVersion: profilingrun.SchemaVersion,
		Candidate: profilingrun.CandidateIdentity{
			ReportDigest:             "sha256:" + hex.EncodeToString(reportSum[:]),
			ModelSpecContentDigest:   candidate.ModelSpecContentDigest,
			DeviceClassContentDigest: candidate.DeviceClassContentDigest,
			RuntimeDeclarationDigest: candidate.RuntimeDeclarationContentDigest,
			ExecutionPath:            request.Candidate.ExecutionPath,
			Verdict:                  candidate.Verdict,
		},
		State: candidate.State,
		Images: profilingrun.Images{
			Runtime:       candidate.RuntimeImage,
			Artifact:      candidate.ArtifactImage,
			PowerObserver: p.config.PowerObserverImage,
		},
		TargetNode: node,
		CPU: profilingrun.CPUContract{
			Policy:             p.config.CPUPolicy,
			PolicyVersion:      p.config.CPUPolicyVersion,
			Request:            request.CPURequest,
			Limit:              node.AllocatableCPU,
			ORTIntraOpThreads:  cpu,
			ORTInterOpThreads:  1,
			ExclusivityClaimed: false,
		},
		Measurement: request.Measurement,
		Power: profilingrun.PowerContract{
			SourceKind:           power.Kind,
			NodeName:             node.Name,
			Endpoint:             power.Endpoint,
			Metric:               power.Metric,
			IntervalMilliseconds: request.Power.IntervalMilliseconds,
			TimeoutMilliseconds:  request.Power.TimeoutMilliseconds,
			DurationSeconds:      request.Power.DurationSeconds,
		},
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
	if request.ExecutionPath != firstExecutionPath ||
		candidate.State.RuntimeFamily != firstRuntimeFamily || candidate.State.Backend != firstRuntimeBackend {
		return errors.New("resolved candidate is not the supported CPU ONNX Runtime path")
	}
	if strings.TrimSpace(candidate.DeviceArchitecture) == "" || len(candidate.DeviceNodeSelector) == 0 {
		return errors.New("resolved DeviceClass architecture and Node selector are required")
	}
	if len(candidate.DevicePowerModes) != 1 || candidate.DevicePowerModes[0] != firstPowerMode {
		return errors.New("resolved DeviceClass is not the first-path non-selectable fixed-power class")
	}
	return nil
}

func validateCandidateMaterialBinding(candidate ResolvedCandidate) error {
	report, err := profilingrun.DecodeCandidateReport(candidate.ReportBytes)
	if err != nil {
		return err
	}
	// Spike reports predate the internal material contract. They remain
	// readable for fixture migration, but newly resolved candidates must bind
	// all side facts used by Node selection and artifact execution.
	if report.SchemaVersion != profilingrun.CandidateReportSchemaVersion {
		return nil
	}
	checks := []struct{ name, want, got string }{
		{"DeviceClass architecture", candidate.DeviceArchitecture, report.DeviceClassFacts.Architecture},
		{"artifact image", candidate.ArtifactImage, report.ArtifactSupply.Image},
		{"artifact architecture", candidate.ArtifactArchitecture, report.ArtifactSupply.Architecture},
		{"artifact path", candidate.ArtifactPath, report.ArtifactSupply.Path},
		{"artifact digest", candidate.ArtifactDigest, report.ArtifactSupply.Digest},
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
	return nil
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
	if r.Candidate.ExecutionPath != firstExecutionPath {
		return fmt.Errorf("first path supports only executionPath %q", firstExecutionPath)
	}
	if r.Candidate.PowerMode != firstPowerMode {
		return fmt.Errorf("first path supports only powerMode %q", firstPowerMode)
	}
	cpuRequest, err := resource.ParseQuantity(r.CPURequest)
	if err != nil || cpuRequest.Sign() <= 0 {
		return errors.New("CPU request must be a positive Kubernetes quantity")
	}
	if r.Power.IntervalMilliseconds < 1 || r.Power.TimeoutMilliseconds < 1 ||
		r.Power.TimeoutMilliseconds >= r.Power.IntervalMilliseconds ||
		r.Power.DurationSeconds < r.Measurement.DurationSeconds {
		return errors.New("power observation policy is invalid or does not cover the measurement")
	}
	if r.Measurement.Batch < 1 || r.Measurement.WarmupIterations < 0 ||
		r.Measurement.DurationSeconds < 1 {
		return errors.New("measurement batch, duration, and warm-up iterations must be valid")
	}
	if r.Measurement.Repetitions != 1 {
		return errors.New("first profiling path requires exactly one measurement repetition")
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
