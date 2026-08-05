package profilederivation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

const (
	curveFamilyContractSchemaVersion = "chill.dacs.io/curve-family-contract.v1alpha1"
	curveFamilyIdentitySchemaVersion = "chill.dacs.io/curve-family-identity.v1alpha1"
)

var (
	curveDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	curveImagePattern  = regexp.MustCompile(`^[^@[:space:]]+@sha256:[0-9a-f]{64}$`)
)

// CurveCPUContract is the execution CPU contract shared by every family
// member. It is deliberately Kubernetes-free.
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
	SourceKind                 string `json:"sourceKind"`
	NodeName                   string `json:"nodeName"`
	Endpoint                   string `json:"endpoint"`
	PowerMetric                string `json:"powerMetric"`
	ConfigurationContentDigest string `json:"configurationContentDigest"`
}

// CurveProtocolContract contains the mode-specific protocol fields that must
// remain fixed across members. Batch and the member artifact-supply image are
// intentionally absent.
type CurveProtocolContract struct {
	WorkloadMode               string `json:"workloadMode"`
	InputGenerator             string `json:"inputGenerator"`
	OutputValidation           string `json:"outputValidation"`
	WarmupIterations           int    `json:"warmupIterations"`
	SettlingDurationSeconds    int    `json:"settlingDurationSeconds"`
	DurationSeconds            int    `json:"durationSeconds"`
	Repetitions                int    `json:"repetitions"`
	MetricIntervalMilliseconds int    `json:"metricIntervalMilliseconds"`
	MetricTimeoutMilliseconds  int    `json:"metricTimeoutMilliseconds"`
	MetricDurationSeconds      int    `json:"metricDurationSeconds"`
	MetricObserverImage        string `json:"metricObserverImage"`
	WorkloadObserverImage      string `json:"workloadObserverImage"`
	WorkloadContractDigest     string `json:"workloadContractDigest"`
}

// CurveConversionContract is present only when each member is a separately
// derived immutable artifact.
type CurveConversionContract struct {
	Derived                bool   `json:"derived"`
	TargetPlatform         string `json:"targetPlatform,omitempty"`
	Tool                   string `json:"tool,omitempty"`
	ToolVersion            string `json:"toolVersion,omitempty"`
	ToolDistributionDigest string `json:"toolDistributionDigest,omitempty"`
	RecipeDigest           string `json:"recipeDigest,omitempty"`
}

type CurveTensorContract struct {
	Layout        string `json:"layout"`
	DataType      string `json:"dataType"`
	NonBatchShape []int  `json:"nonBatchShape"`
}

// CurveFamilySharedContract is the pre-measurement anchor that every planned
// member must share.
type CurveFamilySharedContract struct {
	LogicalModel             string                    `json:"logicalModel"`
	SourceArtifactDigest     string                    `json:"sourceArtifactDigest"`
	ModelSpecContentDigest   string                    `json:"modelSpecContentDigest"`
	DeviceClassContentDigest string                    `json:"deviceClassContentDigest"`
	RuntimeDeclarationDigest string                    `json:"runtimeDeclarationDigest"`
	DeviceClass              string                    `json:"deviceClass"`
	NodeUID                  string                    `json:"nodeUID"`
	RuntimeFamily            string                    `json:"runtimeFamily"`
	Backend                  string                    `json:"backend"`
	RuntimeImage             string                    `json:"runtimeImage"`
	PowerMode                string                    `json:"powerMode"`
	HostContractDigest       string                    `json:"hostContractDigest,omitempty"`
	CPU                      CurveCPUContract          `json:"cpu"`
	MetricSource             CurveMetricSourceContract `json:"metricSource"`
	ActiveProtocol           CurveProtocolContract     `json:"activeProtocol"`
	IdleProtocol             CurveProtocolContract     `json:"idleProtocol"`
	Conversion               CurveConversionContract   `json:"conversion"`
	Tensor                   CurveTensorContract       `json:"tensor"`
}

// CurveFamilyMemberContract binds every batch-varying exact artifact fact.
type CurveFamilyMemberContract struct {
	Batch                int    `json:"batch"`
	ExecutionPath        string `json:"executionPath"`
	ArtifactName         string `json:"artifactName"`
	ArtifactDigest       string `json:"artifactDigest"`
	ArtifactPath         string `json:"artifactPath"`
	ArtifactImage        string `json:"artifactImage"`
	FixedBatchCapability int    `json:"fixedBatchCapability"`
	InputShape           []int  `json:"inputShape"`
}

type curveFamilyContractRecord struct {
	SchemaVersion string                      `json:"schemaVersion"`
	Shared        CurveFamilySharedContract   `json:"shared"`
	Members       []CurveFamilyMemberContract `json:"members"`
}

// CurveFamilyContract can only be constructed through
// FreezeCurveFamilyContract, which canonicalizes batch order and fixes its
// content identity.
type CurveFamilyContract struct {
	record curveFamilyContractRecord
	digest string
}

func FreezeCurveFamilyContract(
	shared CurveFamilySharedContract,
	members []CurveFamilyMemberContract,
) (CurveFamilyContract, error) {
	shared = cloneCurveFamilyShared(shared)
	members = cloneCurveFamilyMembers(members)
	sort.Slice(members, func(i, j int) bool { return members[i].Batch < members[j].Batch })
	if err := validateCurveFamilyContract(shared, members); err != nil {
		return CurveFamilyContract{}, err
	}
	record := curveFamilyContractRecord{
		SchemaVersion: curveFamilyContractSchemaVersion,
		Shared:        shared,
		Members:       members,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return CurveFamilyContract{}, fmt.Errorf("encode curve-family contract: %w", err)
	}
	return CurveFamilyContract{record: record, digest: versionedCurveDigest(curveFamilyContractSchemaVersion, payload)}, nil
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
	return FreezeCurveFamilyContract(record.Shared, record.Members)
}

func (c CurveFamilyContract) Digest() string {
	return c.digest
}

func (c CurveFamilyContract) CanonicalJSON() ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(c.record)
}

func (c CurveFamilyContract) Shared() CurveFamilySharedContract {
	return cloneCurveFamilyShared(c.record.Shared)
}

func (c CurveFamilyContract) Members() []CurveFamilyMemberContract {
	return cloneCurveFamilyMembers(c.record.Members)
}

func (c CurveFamilyContract) validate() error {
	if c.record.SchemaVersion != curveFamilyContractSchemaVersion || c.digest == "" {
		return errors.New("curve-family contract was not frozen")
	}
	if err := validateCurveFamilyContract(c.record.Shared, c.record.Members); err != nil {
		return err
	}
	payload, err := json.Marshal(c.record)
	if err != nil {
		return err
	}
	if c.digest != versionedCurveDigest(curveFamilyContractSchemaVersion, payload) {
		return errors.New("curve-family contract digest is invalid")
	}
	return nil
}

// VerifiedCurveFamilyMember is the plain projection made only after active and
// idle frozen evidence have crossed their Loader boundaries.
type VerifiedCurveFamilyMember struct {
	Shared                 CurveFamilySharedContract `json:"shared"`
	Member                 CurveFamilyMemberContract `json:"member"`
	StateIdentity          string                    `json:"stateIdentity"`
	EnvironmentIdentity    string                    `json:"environmentIdentity"`
	ActiveProtocolIdentity string                    `json:"activeProtocolIdentity"`
	IdleProtocolIdentity   string                    `json:"idleProtocolIdentity"`
}

type realizedCurveFamilyMember struct {
	Batch                  int    `json:"batch"`
	StateIdentity          string `json:"stateIdentity"`
	ActiveProtocolIdentity string `json:"activeProtocolIdentity"`
	IdleProtocolIdentity   string `json:"idleProtocolIdentity"`
}

type curveFamilyIdentityRecord struct {
	SchemaVersion       string                      `json:"schemaVersion"`
	ContractDigest      string                      `json:"contractDigest"`
	EnvironmentIdentity string                      `json:"environmentIdentity"`
	Members             []realizedCurveFamilyMember `json:"members"`
}

// CurveFamily is a Loader-verified composition boundary. It does not make its
// exact member states physically equivalent.
type CurveFamily struct {
	contract            CurveFamilyContract
	identity            string
	environmentIdentity string
	members             map[int]VerifiedCurveFamilyMember
}

func VerifyCurveFamily(
	contract CurveFamilyContract,
	verified []VerifiedCurveFamilyMember,
) (CurveFamily, error) {
	if err := contract.validate(); err != nil {
		return CurveFamily{}, err
	}
	if len(verified) != len(contract.record.Members) {
		return CurveFamily{}, errors.New("verified curve-family member count does not match the contract")
	}
	verified = cloneVerifiedCurveFamilyMembers(verified)
	sort.Slice(verified, func(i, j int) bool { return verified[i].Member.Batch < verified[j].Member.Batch })

	members := make(map[int]VerifiedCurveFamilyMember, len(verified))
	realized := make([]realizedCurveFamilyMember, len(verified))
	environment := ""
	type stateProtocols struct{ active, idle string }
	byState := make(map[string]stateProtocols)
	for index, member := range verified {
		expected := contract.record.Members[index]
		if !reflect.DeepEqual(member.Shared, contract.record.Shared) || !reflect.DeepEqual(member.Member, expected) {
			return CurveFamily{}, fmt.Errorf("verified curve-family member batch %d contradicts the frozen contract", expected.Batch)
		}
		if !validDerivedIdentity(member.StateIdentity, "chill.dacs.io/execution-state-identity.") ||
			!validDerivedIdentity(member.EnvironmentIdentity, "chill.dacs.io/external-environment-identity.") ||
			!validDerivedIdentity(member.ActiveProtocolIdentity, "chill.dacs.io/measurement-protocol-identity.") ||
			!validDerivedIdentity(member.IdleProtocolIdentity, "chill.dacs.io/measurement-protocol-identity.") {
			return CurveFamily{}, fmt.Errorf("verified curve-family member batch %d has an invalid derivation identity", expected.Batch)
		}
		if environment == "" {
			environment = member.EnvironmentIdentity
		} else if member.EnvironmentIdentity != environment {
			return CurveFamily{}, errors.New("verified curve-family members do not share one environment identity")
		}
		protocols := stateProtocols{active: member.ActiveProtocolIdentity, idle: member.IdleProtocolIdentity}
		if previous, exists := byState[member.StateIdentity]; exists && previous != protocols {
			return CurveFamily{}, errors.New("one execution state maps to inconsistent active or idle protocols")
		}
		byState[member.StateIdentity] = protocols
		if _, exists := members[expected.Batch]; exists {
			return CurveFamily{}, errors.New("verified curve-family contains a duplicate batch")
		}
		members[expected.Batch] = member
		realized[index] = realizedCurveFamilyMember{
			Batch: expected.Batch, StateIdentity: member.StateIdentity,
			ActiveProtocolIdentity: member.ActiveProtocolIdentity,
			IdleProtocolIdentity:   member.IdleProtocolIdentity,
		}
	}
	record := curveFamilyIdentityRecord{
		SchemaVersion:       curveFamilyIdentitySchemaVersion,
		ContractDigest:      contract.Digest(),
		EnvironmentIdentity: environment,
		Members:             realized,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return CurveFamily{}, fmt.Errorf("encode realized curve-family identity: %w", err)
	}
	return CurveFamily{
		contract: contract, identity: versionedCurveDigest(curveFamilyIdentitySchemaVersion, payload),
		environmentIdentity: environment, members: members,
	}, nil
}

func (f CurveFamily) Identity() string { return f.identity }

func (f CurveFamily) ContractDigest() string { return f.contract.Digest() }

func (f CurveFamily) EnvironmentIdentity() string { return f.environmentIdentity }

func (f CurveFamily) Members() []VerifiedCurveFamilyMember {
	result := make([]VerifiedCurveFamilyMember, 0, len(f.members))
	for _, member := range f.members {
		result = append(result, cloneVerifiedCurveFamilyMember(member))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Member.Batch < result[j].Member.Batch })
	return result
}

func validateCurveFamilyContract(shared CurveFamilySharedContract, members []CurveFamilyMemberContract) error {
	if err := validateCurveFamilyShared(shared); err != nil {
		return err
	}
	if len(members) == 0 {
		return errors.New("curve-family requires at least one member")
	}
	for index, member := range members {
		if err := validateCurveFamilyMember(shared, member, index+1); err != nil {
			return err
		}
	}
	return nil
}

func validateCurveFamilyShared(shared CurveFamilySharedContract) error {
	for name, value := range map[string]string{
		"logical model": shared.LogicalModel, "device class": shared.DeviceClass,
		"Node UID": shared.NodeUID, "runtime family": shared.RuntimeFamily,
		"backend": shared.Backend, "power mode": shared.PowerMode,
	} {
		if !cleanCurveValue(value) {
			return fmt.Errorf("curve-family shared %s is required without surrounding whitespace", name)
		}
	}
	for name, value := range map[string]string{
		"source artifact":     shared.SourceArtifactDigest,
		"ModelSpec content":   shared.ModelSpecContentDigest,
		"DeviceClass content": shared.DeviceClassContentDigest,
		"runtime declaration": shared.RuntimeDeclarationDigest,
	} {
		if !curveDigestPattern.MatchString(value) {
			return fmt.Errorf("curve-family shared %s digest is invalid", name)
		}
	}
	if shared.HostContractDigest != "" && !curveDigestPattern.MatchString(shared.HostContractDigest) {
		return errors.New("curve-family host-contract digest is invalid")
	}
	if !curveImagePattern.MatchString(shared.RuntimeImage) {
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

func validateCurveFamilyMember(
	shared CurveFamilySharedContract,
	member CurveFamilyMemberContract,
	expectedBatch int,
) error {
	if member.Batch != expectedBatch {
		return errors.New("curve-family members must form a contiguous batch domain starting at one")
	}
	for name, value := range map[string]string{
		"execution path": member.ExecutionPath, "artifact name": member.ArtifactName,
		"artifact path": member.ArtifactPath,
	} {
		if !cleanCurveValue(value) {
			return fmt.Errorf("curve-family member batch %d %s is invalid", member.Batch, name)
		}
	}
	if member.ArtifactPath == "/" || !strings.HasPrefix(member.ArtifactPath, "/") ||
		path.Clean(member.ArtifactPath) != member.ArtifactPath ||
		!curveDigestPattern.MatchString(member.ArtifactDigest) || !curveImagePattern.MatchString(member.ArtifactImage) {
		return fmt.Errorf("curve-family member batch %d artifact contract is invalid", member.Batch)
	}
	if (shared.Conversion.Derived && member.FixedBatchCapability != member.Batch) ||
		(!shared.Conversion.Derived && member.FixedBatchCapability != 0 && member.FixedBatchCapability != member.Batch) {
		return fmt.Errorf("curve-family member batch %d fixed capability does not match", member.Batch)
	}
	if len(member.InputShape) != len(shared.Tensor.NonBatchShape)+1 || member.InputShape[0] != member.Batch {
		return fmt.Errorf("curve-family member batch %d input shape does not match", member.Batch)
	}
	for shapeIndex, dimension := range shared.Tensor.NonBatchShape {
		if member.InputShape[shapeIndex+1] != dimension {
			return fmt.Errorf("curve-family member batch %d non-batch tensor shape differs", member.Batch)
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
	if !curveDigestPattern.MatchString(source.ConfigurationContentDigest) {
		return errors.New("curve-family metric-source configuration digest is invalid")
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
		protocol.DurationSeconds < 1 || protocol.Repetitions < 1 ||
		protocol.MetricIntervalMilliseconds < 1 || protocol.MetricTimeoutMilliseconds < 1 ||
		protocol.MetricTimeoutMilliseconds >= protocol.MetricIntervalMilliseconds ||
		protocol.MetricDurationSeconds < protocol.DurationSeconds ||
		!curveImagePattern.MatchString(protocol.MetricObserverImage) ||
		!curveImagePattern.MatchString(protocol.WorkloadObserverImage) ||
		!curveDigestPattern.MatchString(protocol.WorkloadContractDigest) {
		return fmt.Errorf("curve-family %s protocol is invalid", mode)
	}
	return nil
}

func validateCurveConversion(conversion CurveConversionContract) error {
	values := []string{conversion.TargetPlatform, conversion.Tool, conversion.ToolVersion,
		conversion.ToolDistributionDigest, conversion.RecipeDigest}
	if !conversion.Derived {
		for _, value := range values {
			if value != "" {
				return errors.New("non-derived curve-family conversion contract must be empty")
			}
		}
		return nil
	}
	for index, value := range values {
		if !cleanCurveValue(value) {
			return errors.New("derived curve-family conversion contract is incomplete")
		}
		if index >= 3 && !curveDigestPattern.MatchString(value) {
			return errors.New("derived curve-family conversion digest is invalid")
		}
	}
	return nil
}

func cleanCurveValue(value string) bool {
	return value != "" && strings.TrimSpace(value) == value
}

func validDerivedIdentity(identity, prefix string) bool {
	if !strings.HasPrefix(identity, prefix) {
		return false
	}
	separator := strings.LastIndex(identity, "@")
	return separator > len(prefix) && curveDigestPattern.MatchString(identity[separator+1:])
}

func versionedCurveDigest(schemaVersion string, payload []byte) string {
	sum := sha256.Sum256(payload)
	return schemaVersion + "@sha256:" + hex.EncodeToString(sum[:])
}

func cloneCurveFamilyShared(shared CurveFamilySharedContract) CurveFamilySharedContract {
	shared.Tensor.NonBatchShape = append([]int(nil), shared.Tensor.NonBatchShape...)
	return shared
}

func cloneCurveFamilyMembers(members []CurveFamilyMemberContract) []CurveFamilyMemberContract {
	result := make([]CurveFamilyMemberContract, len(members))
	for index, member := range members {
		result[index] = member
		result[index].InputShape = append([]int(nil), member.InputShape...)
	}
	return result
}

func cloneVerifiedCurveFamilyMember(member VerifiedCurveFamilyMember) VerifiedCurveFamilyMember {
	member.Shared = cloneCurveFamilyShared(member.Shared)
	member.Member.InputShape = append([]int(nil), member.Member.InputShape...)
	return member
}

func cloneVerifiedCurveFamilyMembers(members []VerifiedCurveFamilyMember) []VerifiedCurveFamilyMember {
	result := make([]VerifiedCurveFamilyMember, len(members))
	for index, member := range members {
		result[index] = cloneVerifiedCurveFamilyMember(member)
	}
	return result
}
