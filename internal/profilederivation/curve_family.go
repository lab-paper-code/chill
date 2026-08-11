package profilederivation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"sort"
	"strings"
)

const (
	curveFamilyContractSchemaVersion = "chill.dacs.io/curve-family-contract.v1alpha3"
)

// CurveCPUContract is the execution CPU contract shared by every family batch.
// It is deliberately Kubernetes-free.
type CurveCPUContract struct {
	Policy             string `json:"policy"`
	PolicyVersion      string `json:"policyVersion"`
	Request            string `json:"request"`
	Limit              string `json:"limit"`
	ORTIntraOpThreads  int    `json:"ortIntraOpThreads"`
	ORTInterOpThreads  int    `json:"ortInterOpThreads"`
	ExclusivityClaimed bool   `json:"exclusivityClaimed"`
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

// CurveConversionContract identifies how the immutable family artifact was
// derived.
type CurveConversionContract struct {
	TargetPlatform string `json:"targetPlatform,omitempty"`
	Tool           string `json:"tool,omitempty"`
	ToolVersion    string `json:"toolVersion,omitempty"`
}

type CurveTensorContract struct {
	Layout        string `json:"layout"`
	DataType      string `json:"dataType"`
	NonBatchShape []int  `json:"nonBatchShape"`
}

// CurveArtifactContract binds the one immutable artifact every batch of this
// family executes.
type CurveArtifactContract struct {
	ExecutionPath string `json:"executionPath"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	Image         string `json:"image"`
}

// CurveFamilySharedContract is the pre-measurement family anchor.
type CurveFamilySharedContract struct {
	Name            string                    `json:"name"`
	LogicalModel    string                    `json:"logicalModel"`
	DeviceClass     string                    `json:"deviceClass"`
	NodeUID         string                    `json:"nodeUID"`
	RuntimeFamily   string                    `json:"runtimeFamily"`
	Backend         string                    `json:"backend"`
	RuntimeImage    string                    `json:"runtimeImage"`
	PowerMode       string                    `json:"powerMode"`
	CPU             CurveCPUContract          `json:"cpu"`
	MetricSource    CurveMetricSourceContract `json:"metricSource"`
	ActiveProtocol  CurveProtocolContract     `json:"activeProtocol"`
	IdleProtocol    CurveProtocolContract     `json:"idleProtocol"`
	Conversion      CurveConversionContract   `json:"conversion"`
	Artifact        CurveArtifactContract     `json:"artifact"`
	EligibleBatches []int                     `json:"eligibleBatches"`
	Tensor          CurveTensorContract       `json:"tensor"`
}

type curveFamilyContractRecord struct {
	SchemaVersion string                    `json:"schemaVersion"`
	Shared        CurveFamilySharedContract `json:"shared"`
}

// CurveFamilyContract can only be constructed through
// FreezeCurveFamilyContract, which validates and orders its eligible batches.
type CurveFamilyContract struct {
	record curveFamilyContractRecord
}

func FreezeCurveFamilyContract(shared CurveFamilySharedContract) (CurveFamilyContract, error) {
	shared = cloneCurveFamilyShared(shared)
	sort.Ints(shared.EligibleBatches)
	if err := validateCurveFamilyShared(shared); err != nil {
		return CurveFamilyContract{}, err
	}
	record := curveFamilyContractRecord{
		SchemaVersion: curveFamilyContractSchemaVersion,
		Shared:        shared,
	}
	return CurveFamilyContract{record: record}, nil
}

// DecodeCurveFamilyContract strict-decodes a persisted contract projection and
// returns the same frozen canonical identity as structured construction. JSON
// whitespace is not part of the contract identity.
func DecodeCurveFamilyContract(raw []byte) (CurveFamilyContract, error) {
	var record curveFamilyContractRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return CurveFamilyContract{}, fmt.Errorf("decode curve-family contract: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return CurveFamilyContract{}, errors.New("curve-family contract contains a trailing JSON value")
	}
	if record.SchemaVersion != curveFamilyContractSchemaVersion {
		return CurveFamilyContract{}, fmt.Errorf("unsupported curve-family contract schema %q", record.SchemaVersion)
	}
	return FreezeCurveFamilyContract(record.Shared)
}

func (c CurveFamilyContract) CanonicalJSON() ([]byte, error) {
	if err := c.requireFrozen(); err != nil {
		return nil, err
	}
	return json.Marshal(c.record)
}

func (c CurveFamilyContract) Shared() CurveFamilySharedContract {
	return cloneCurveFamilyShared(c.record.Shared)
}

func (c CurveFamilyContract) EligibleBatches() []int {
	return append([]int(nil), c.record.Shared.EligibleBatches...)
}

// requireFrozen checks opaque type state only. The constructor and decoder own
// all content validation.
func (c CurveFamilyContract) requireFrozen() error {
	if c.record.SchemaVersion != curveFamilyContractSchemaVersion {
		return errors.New("curve-family contract was not frozen")
	}
	return nil
}

// VerifiedCurveFamilyState is the plain projection made only after active and
// idle frozen evidence have crossed their Loader boundaries. One artifact means
// one state, so there is one projection per family.
type VerifiedCurveFamilyState struct {
	Shared                 CurveFamilySharedContract `json:"shared"`
	StateIdentity          string                    `json:"stateIdentity"`
	EnvironmentIdentity    string                    `json:"environmentIdentity"`
	ActiveProtocolIdentity string                    `json:"activeProtocolIdentity"`
	IdleProtocolIdentity   string                    `json:"idleProtocolIdentity"`
}

// CurveFamily is a Loader-verified composition boundary.
type CurveFamily struct {
	contract CurveFamilyContract
	state    VerifiedCurveFamilyState
}

func VerifyCurveFamily(
	contract CurveFamilyContract,
	verified VerifiedCurveFamilyState,
) (CurveFamily, error) {
	if err := contract.requireFrozen(); err != nil {
		return CurveFamily{}, err
	}
	if !reflect.DeepEqual(verified.Shared, contract.record.Shared) {
		return CurveFamily{}, errors.New("verified curve-family state does not match the contract")
	}
	if !cleanCurveValue(verified.StateIdentity) || !cleanCurveValue(verified.EnvironmentIdentity) ||
		!cleanCurveValue(verified.ActiveProtocolIdentity) || !cleanCurveValue(verified.IdleProtocolIdentity) {
		return CurveFamily{}, errors.New("verified curve-family state has an invalid derivation identity")
	}
	verified.Shared = cloneCurveFamilyShared(verified.Shared)
	return CurveFamily{contract: contract, state: verified}, nil
}

func (f CurveFamily) Name() string { return f.contract.record.Shared.Name }

func (f CurveFamily) EnvironmentIdentity() string { return f.state.EnvironmentIdentity }

func validateCurveFamilyShared(shared CurveFamilySharedContract) error {
	for name, value := range map[string]string{
		"name": shared.Name, "logical model": shared.LogicalModel, "device class": shared.DeviceClass,
		"Node UID": shared.NodeUID, "runtime family": shared.RuntimeFamily,
		"backend": shared.Backend, "power mode": shared.PowerMode,
	} {
		if !cleanCurveValue(value) {
			return fmt.Errorf("curve-family shared %s is required without surrounding whitespace", name)
		}
	}
	if !cleanCurveValue(shared.RuntimeImage) {
		return errors.New("curve-family runtime image is invalid")
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
		return errors.New("curve-family active and idle workload modes must differ")
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
			return fmt.Errorf("curve-family artifact %s is required without surrounding whitespace", name)
		}
	}
	if shared.Artifact.Path == "/" || !strings.HasPrefix(shared.Artifact.Path, "/") ||
		path.Clean(shared.Artifact.Path) != shared.Artifact.Path {
		return errors.New("curve-family artifact path is invalid")
	}
	if len(shared.EligibleBatches) == 0 {
		return errors.New("curve-family requires at least one eligible batch")
	}
	seenBatches := make(map[int]struct{}, len(shared.EligibleBatches))
	for _, batch := range shared.EligibleBatches {
		if batch <= 0 {
			return errors.New("curve-family eligible batches must be positive")
		}
		if _, exists := seenBatches[batch]; exists {
			return fmt.Errorf("curve-family eligible batch %d is duplicated", batch)
		}
		seenBatches[batch] = struct{}{}
	}
	if !cleanCurveValue(shared.Tensor.Layout) || !cleanCurveValue(shared.Tensor.DataType) ||
		len(shared.Tensor.NonBatchShape) == 0 {
		return errors.New("curve-family tensor contract is incomplete")
	}
	for _, dimension := range shared.Tensor.NonBatchShape {
		if dimension < 1 {
			return errors.New("curve-family tensor dimensions must be positive")
		}
	}
	return nil
}

func validateCurveCPU(cpu CurveCPUContract) error {
	for _, value := range []string{cpu.Policy, cpu.PolicyVersion, cpu.Request, cpu.Limit} {
		if !cleanCurveValue(value) {
			return errors.New("curve-family CPU contract is incomplete")
		}
	}
	if cpu.ORTIntraOpThreads < 0 || cpu.ORTInterOpThreads < 0 {
		return errors.New("curve-family CPU thread counts cannot be negative")
	}
	return nil
}

func validateCurveMetricSource(source CurveMetricSourceContract) error {
	for _, value := range []string{source.SourceKind, source.NodeName, source.Endpoint, source.PowerMetric} {
		if !cleanCurveValue(value) {
			return errors.New("curve-family metric-source contract is incomplete")
		}
	}
	return nil
}

func validateCurveProtocol(mode string, protocol CurveProtocolContract) error {
	for _, value := range []string{protocol.WorkloadMode, protocol.InputGenerator, protocol.OutputValidation} {
		if !cleanCurveValue(value) {
			return fmt.Errorf("curve-family %s protocol is incomplete", mode)
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
		return fmt.Errorf("curve-family %s protocol is invalid", mode)
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
			return errors.New("curve-family conversion contract is invalid")
		}
		present++
	}
	if present != 0 && present != len(values) {
		return errors.New("curve-family conversion contract must be all empty or all present")
	}
	return nil
}

func cleanCurveValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func cloneCurveFamilyShared(shared CurveFamilySharedContract) CurveFamilySharedContract {
	shared.EligibleBatches = append([]int(nil), shared.EligibleBatches...)
	shared.Tensor.NonBatchShape = append([]int(nil), shared.Tensor.NonBatchShape...)
	return shared
}
