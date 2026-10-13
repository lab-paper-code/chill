package profilederivation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"path"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/lab-paper-code/chill/internal/modelcompat"
)

const (
	curvePlanSchemaVersion = "chill.dacs.io/curve-plan.v1alpha8"
	curveRuntimeFirstfloor = "firstfloor"
	curveBackendORTCUDA    = "ort-cuda"
	curveBackendORTCPU     = "ort-cpu"
)

// CurveCPUContract is the execution CPU contract shared by every planned batch.
// It is deliberately Kubernetes-free.
type CurveCPUContract struct {
	Policy             string `json:"policy"`
	PolicyVersion      string `json:"policyVersion"`
	Request            string `json:"request"`
	Limit              string `json:"limit"`
	ORTIntraOpThreads  int    `json:"ortIntraOpThreads"`
	ORTInterOpThreads  int    `json:"ortInterOpThreads"`
	AffinityCPUs       []int  `json:"affinityCPUs,omitempty"`
	ExclusivityClaimed bool   `json:"exclusivityClaimed"`
}

// CurveRuntimeAccessContract freezes accelerator delivery and physical GPU identity.
type CurveRuntimeAccessContract struct {
	Privileged         bool              `json:"privileged"`
	SupplementalGroups []int64           `json:"supplementalGroups,omitempty"`
	Resources          map[string]string `json:"resources,omitempty"`
	GPUUUID            string            `json:"gpuUUID,omitempty"`
}

// CurveMetricSourceContract identifies the wall-power source without carrying
// credentials or Kubernetes objects.
type CurveMetricSourceContract struct {
	SourceKind  string `json:"sourceKind"`
	NodeName    string `json:"nodeName"`
	Endpoint    string `json:"endpoint"`
	PowerMetric string `json:"powerMetric"`
}

// CurveProtocolContract contains the mode-specific protocol fields that must
// remain fixed across batches. Batch is intentionally absent.
type CurveProtocolContract struct {
	WorkloadMode               string `json:"workloadMode"`
	InputGenerator             string `json:"inputGenerator"`
	OutputValidation           string `json:"outputValidation"`
	WarmupIterations           int    `json:"warmupIterations"`
	SettlingDurationSeconds    int    `json:"settlingDurationSeconds"`
	DurationSeconds            int    `json:"durationSeconds"`
	MetricIntervalMilliseconds int    `json:"metricIntervalMilliseconds"`
	MetricTimeoutMilliseconds  int    `json:"metricTimeoutMilliseconds"`
	MetricDurationSeconds      int    `json:"metricDurationSeconds"`
	MetricObserverImage        string `json:"metricObserverImage"`
	WorkloadObserverImage      string `json:"workloadObserverImage"`
	WorkloadSchemaVersion      string `json:"workloadSchemaVersion"`
}

// CurveConversionContract identifies how the immutable plan artifact was derived.
type CurveConversionContract struct {
	TargetPlatform string `json:"targetPlatform,omitempty"`
	Tool           string `json:"tool,omitempty"`
	ToolVersion    string `json:"toolVersion,omitempty"`
}

type CurveTensorContract struct {
	Tensors []modelcompat.InputTensor `json:"tensors"`
}

// CurveArtifactContract binds the one immutable artifact every planned batch
// executes.
type CurveArtifactContract struct {
	ExecutionPath string `json:"executionPath"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Image         string `json:"image"`
}

// CurvePlanSpec is the raw material selected for one measurement plan.
type CurvePlanSpec struct {
	Name              string                          `json:"name"`
	LogicalModel      string                          `json:"logicalModel"`
	DeviceClass       string                          `json:"deviceClass"`
	NodeUID           string                          `json:"nodeUID"`
	RuntimeFamily     string                          `json:"runtimeFamily"`
	Backend           string                          `json:"backend"`
	Firstfloor        *modelcompat.FirstfloorSettings `json:"firstfloor,omitempty"`
	RuntimeAccess     *CurveRuntimeAccessContract     `json:"runtimeAccess,omitempty"`
	RuntimeImage      string                          `json:"runtimeImage"`
	PowerMode         string                          `json:"powerMode"`
	CPU               CurveCPUContract                `json:"cpu"`
	MetricSource      CurveMetricSourceContract       `json:"metricSource"`
	ActiveProtocol    CurveProtocolContract           `json:"activeProtocol"`
	IdleProtocol      CurveProtocolContract           `json:"idleProtocol"`
	Conversion        CurveConversionContract         `json:"conversion"`
	Artifact          CurveArtifactContract           `json:"artifact"`
	PlannedBatchSizes []int                           `json:"plannedBatchSizes"`
	Tensor            CurveTensorContract             `json:"tensor"`
}

type curvePlanRecord struct {
	SchemaVersion string        `json:"schemaVersion"`
	Shared        CurvePlanSpec `json:"shared"`
}

// CurvePlan can only be constructed through NewCurvePlan, which validates and
// orders its planned batch sizes.
type CurvePlan struct {
	record curvePlanRecord
}

func NewCurvePlan(spec CurvePlanSpec) (CurvePlan, error) {
	spec = cloneCurvePlanSpec(spec)
	sort.Ints(spec.PlannedBatchSizes)
	sort.Ints(spec.CPU.AffinityCPUs)
	if err := validateCurvePlanSpec(spec); err != nil {
		return CurvePlan{}, err
	}
	record := curvePlanRecord{
		SchemaVersion: curvePlanSchemaVersion,
		Shared:        spec,
	}
	return CurvePlan{record: record}, nil
}

// DecodeCurvePlan strict-decodes a persisted plan projection and returns the
// same canonical identity as structured construction. JSON whitespace is not
// part of the plan identity.
func DecodeCurvePlan(raw []byte) (CurvePlan, error) {
	var record curvePlanRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return CurvePlan{}, fmt.Errorf("decode curve plan: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CurvePlan{}, errors.New("curve plan contains a trailing JSON value")
	}
	if record.SchemaVersion != curvePlanSchemaVersion {
		return CurvePlan{}, fmt.Errorf("unsupported curve-plan schema %q", record.SchemaVersion)
	}
	return NewCurvePlan(record.Shared)
}

func (c CurvePlan) CanonicalJSON() ([]byte, error) {
	if err := c.requireInitialized(); err != nil {
		return nil, err
	}
	return json.Marshal(c.record)
}

// SchemaVersion returns the exact curve-plan schema identifier.
func (c CurvePlan) SchemaVersion() string {
	if c.requireInitialized() != nil {
		return ""
	}
	return c.record.SchemaVersion
}

func (c CurvePlan) Shared() CurvePlanSpec {
	return cloneCurvePlanSpec(c.record.Shared)
}

func (c CurvePlan) PlannedBatchSizes() []int {
	return append([]int(nil), c.record.Shared.PlannedBatchSizes...)
}

// requireInitialized checks opaque type state only. The constructor and decoder
// own all content validation.
func (c CurvePlan) requireInitialized() error {
	if c.record.SchemaVersion != curvePlanSchemaVersion {
		return errors.New("curve plan is not initialized")
	}
	return nil
}

// VerifiedCurveState is the plain projection made only after active and idle
// evidence have crossed their Loader boundaries. One artifact means
// one state, so there is one projection per plan.
type VerifiedCurveState struct {
	Shared                 CurvePlanSpec `json:"shared"`
	StateIdentity          string        `json:"stateIdentity"`
	EnvironmentIdentity    string        `json:"environmentIdentity"`
	ActiveProtocolIdentity string        `json:"activeProtocolIdentity"`
	IdleProtocolIdentity   string        `json:"idleProtocolIdentity"`
}

// VerifiedCurvePlan is a Loader-verified composition boundary.
type VerifiedCurvePlan struct {
	recorded CurvePlan
	state    VerifiedCurveState
}

func VerifyCurvePlan(
	plan CurvePlan,
	verified VerifiedCurveState,
) (VerifiedCurvePlan, error) {
	if err := plan.requireInitialized(); err != nil {
		return VerifiedCurvePlan{}, err
	}
	if !reflect.DeepEqual(verified.Shared, plan.record.Shared) {
		return VerifiedCurvePlan{}, errors.New("verified curve state does not match the plan")
	}
	if !cleanCurveValue(verified.StateIdentity) || !cleanCurveValue(verified.EnvironmentIdentity) ||
		!cleanCurveValue(verified.ActiveProtocolIdentity) || !cleanCurveValue(verified.IdleProtocolIdentity) {
		return VerifiedCurvePlan{}, errors.New("verified curve state has an invalid derivation identity")
	}
	verified.Shared = cloneCurvePlanSpec(verified.Shared)
	return VerifiedCurvePlan{recorded: plan, state: verified}, nil
}

func (p VerifiedCurvePlan) Name() string { return p.recorded.record.Shared.Name }

func (p VerifiedCurvePlan) EnvironmentIdentity() string { return p.state.EnvironmentIdentity }

func validateCurvePlanSpec(shared CurvePlanSpec) error {
	for name, value := range map[string]string{
		"name": shared.Name, "logical model": shared.LogicalModel, "device class": shared.DeviceClass,
		"Node UID": shared.NodeUID, "runtime family": shared.RuntimeFamily,
		"backend": shared.Backend, "power mode": shared.PowerMode,
	} {
		if !cleanCurveValue(value) {
			return fmt.Errorf("curve plan %s is required without surrounding whitespace", name)
		}
	}
	if !cleanCurveValue(shared.RuntimeImage) {
		return errors.New("curve plan runtime image is invalid")
	}
	if err := validateCurveRuntime(shared); err != nil {
		return err
	}
	if err := validateCurveCPU(shared.CPU); err != nil {
		return err
	}
	if err := validateCurveMetricSource(shared.MetricSource); err != nil {
		return err
	}
	if err := validateCurveProtocol("active", shared.ActiveProtocol); err != nil {
		return err
	}
	if err := validateCurveProtocol("idle", shared.IdleProtocol); err != nil {
		return err
	}
	if shared.ActiveProtocol.WorkloadMode == shared.IdleProtocol.WorkloadMode {
		return errors.New("curve plan active and idle workload modes must differ")
	}
	if err := validateCurveConversion(shared.Conversion); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"execution path": shared.Artifact.ExecutionPath,
		"name":           shared.Artifact.Name,
		"path":           shared.Artifact.Path,
		"image":          shared.Artifact.Image,
	} {
		if !cleanCurveValue(value) {
			return fmt.Errorf("curve plan artifact %s is required without surrounding whitespace", name)
		}
	}
	if shared.Artifact.Path == "/" || !strings.HasPrefix(shared.Artifact.Path, "/") ||
		path.Clean(shared.Artifact.Path) != shared.Artifact.Path {
		return errors.New("curve plan artifact path is invalid")
	}
	if len(shared.PlannedBatchSizes) == 0 {
		return errors.New("curve plan requires at least one planned batch size")
	}
	seenBatches := make(map[int]struct{}, len(shared.PlannedBatchSizes))
	for _, plannedBatchSize := range shared.PlannedBatchSizes {
		if plannedBatchSize <= 0 {
			return errors.New("curve plan batch sizes must be positive")
		}
		if shared.Firstfloor != nil && plannedBatchSize > int(shared.Firstfloor.MaxBatchSize) {
			return errors.New("curve plan batch exceeds selected firstfloor maxBatchSize")
		}
		if _, exists := seenBatches[plannedBatchSize]; exists {
			return fmt.Errorf("curve plan batch size %d is duplicated", plannedBatchSize)
		}
		seenBatches[plannedBatchSize] = struct{}{}
	}
	if err := modelcompat.ValidateInputTensors(shared.Tensor.Tensors); err != nil {
		return fmt.Errorf("curve plan tensor contract: %w", err)
	}
	return nil
}

func validateCurveRuntime(shared CurvePlanSpec) error {
	if shared.RuntimeFamily == curveRuntimeFirstfloor {
		if shared.Backend != curveBackendORTCPU && shared.Backend != curveBackendORTCUDA && shared.Backend != "rknnlite" {
			return errors.New("firstfloor curve plan requires ort-cpu, ort-cuda, or rknnlite")
		}
		if err := modelcompat.ValidateFirstfloorRuntimeSettings(shared.Firstfloor); err != nil {
			return err
		}
		if shared.Backend == curveBackendORTCUDA {
			if shared.RuntimeAccess == nil || !cleanCurveValue(shared.RuntimeAccess.GPUUUID) ||
				!strings.HasPrefix(shared.RuntimeAccess.GPUUUID, "GPU-") ||
				len(shared.RuntimeAccess.Resources) != 1 || shared.RuntimeAccess.Resources["nvidia.com/gpu"] != "1" {
				return errors.New("firstfloor CUDA curve plan requires the frozen GPU UUID and one nvidia.com/gpu resource")
			}
		} else if shared.RuntimeAccess != nil &&
			(shared.RuntimeAccess.GPUUUID != "" || len(shared.RuntimeAccess.Resources) != 0) {
			return errors.New("non-CUDA firstfloor curve plan cannot contain CUDA allocation")
		}
	} else if shared.Firstfloor != nil {
		return errors.New("non-firstfloor curve plan cannot contain firstfloor settings")
	}
	if len(shared.CPU.AffinityCPUs) > 0 && shared.RuntimeFamily != curveRuntimeFirstfloor {
		return errors.New("explicit CPU affinity requires firstfloor execution")
	}
	return nil
}

func validateCurveCPU(cpu CurveCPUContract) error {
	for _, value := range []string{cpu.Policy, cpu.PolicyVersion, cpu.Request, cpu.Limit} {
		if !cleanCurveValue(value) {
			return errors.New("curve plan CPU contract is incomplete")
		}
	}
	if cpu.ORTIntraOpThreads < 0 || cpu.ORTInterOpThreads < 0 {
		return errors.New("curve plan CPU thread counts cannot be negative")
	}
	for index, cpuID := range cpu.AffinityCPUs {
		if cpuID < 0 || (index > 0 && cpuID <= cpu.AffinityCPUs[index-1]) {
			return errors.New("curve plan CPU affinity IDs must be nonnegative, unique, and sorted")
		}
	}
	if len(cpu.AffinityCPUs) > 0 && len(cpu.AffinityCPUs) < cpu.ORTIntraOpThreads {
		return errors.New("curve plan CPU affinity mask is smaller than the ONNX Runtime thread count")
	}
	return nil
}

func validateCurveMetricSource(source CurveMetricSourceContract) error {
	for _, value := range []string{source.SourceKind, source.NodeName, source.Endpoint, source.PowerMetric} {
		if !cleanCurveValue(value) {
			return errors.New("curve plan metric-source contract is incomplete")
		}
	}
	return nil
}

func validateCurveProtocol(mode string, protocol CurveProtocolContract) error {
	for _, value := range []string{protocol.WorkloadMode, protocol.InputGenerator, protocol.OutputValidation} {
		if !cleanCurveValue(value) {
			return fmt.Errorf("curve plan %s protocol is incomplete", mode)
		}
	}
	if protocol.WarmupIterations < 0 || protocol.SettlingDurationSeconds < 0 ||
		protocol.DurationSeconds < 1 ||
		protocol.MetricIntervalMilliseconds < 1 || protocol.MetricTimeoutMilliseconds < 1 ||
		protocol.MetricTimeoutMilliseconds >= protocol.MetricIntervalMilliseconds ||
		protocol.MetricDurationSeconds < protocol.DurationSeconds ||
		!cleanCurveValue(protocol.MetricObserverImage) ||
		!cleanCurveValue(protocol.WorkloadObserverImage) ||
		!cleanCurveValue(protocol.WorkloadSchemaVersion) {
		return fmt.Errorf("curve plan %s protocol is invalid", mode)
	}
	return nil
}

func validateCurveConversion(conversion CurveConversionContract) error {
	values := []string{conversion.TargetPlatform, conversion.Tool, conversion.ToolVersion}
	present := 0
	for _, value := range values {
		if value == "" {
			continue
		}
		if !cleanCurveValue(value) {
			return errors.New("curve plan conversion contract is invalid")
		}
		present++
	}
	if present != 0 && present != len(values) {
		return errors.New("curve plan conversion contract must be all empty or all present")
	}
	return nil
}

func cleanCurveValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func cloneCurvePlanSpec(shared CurvePlanSpec) CurvePlanSpec {
	shared.PlannedBatchSizes = append([]int(nil), shared.PlannedBatchSizes...)
	shared.CPU.AffinityCPUs = slices.Clone(shared.CPU.AffinityCPUs)
	shared.Tensor.Tensors = modelcompat.CloneInputTensors(shared.Tensor.Tensors)
	shared.Firstfloor = modelcompat.CloneFirstfloorRuntimeSettings(shared.Firstfloor)
	if shared.RuntimeAccess != nil {
		access := *shared.RuntimeAccess
		access.SupplementalGroups = append([]int64(nil), access.SupplementalGroups...)
		access.Resources = maps.Clone(access.Resources)
		shared.RuntimeAccess = &access
	}
	return shared
}
