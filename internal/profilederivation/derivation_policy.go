package profilederivation

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
)

const DiscoveryDerivationPolicySchemaVersion = "chill.dacs.io/discovery-derivation-policy.v1alpha2"

type PointPolicyStage string

const (
	PointPolicyStageIdleBaseline     PointPolicyStage = "idle-baseline"
	PointPolicyStageIncrementalPoint PointPolicyStage = "incremental-point"
)

var derivationPolicyNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// PointPolicyEntry is one stage-scoped configured point policy. A discovery
// policy contains exactly one entry for each supported stage.
type PointPolicyEntry struct {
	Name          string           `json:"name"`
	Stage         PointPolicyStage `json:"stage"`
	MinimumTrials int              `json:"minimumTrials"`
}

// BSatPolicyEntry fixes every configurable value used by Plan 5 discovery,
// sizing, and confirmation.
type BSatPolicyEntry struct {
	Name                         string  `json:"name"`
	KMax                         int     `json:"kmax"`
	RelativeImprovementTolerance float64 `json:"relativeImprovementTolerance"`
	DiscoveryTrials              int     `json:"discoveryTrials"`
	MaximumConfirmatoryTrials    int     `json:"maximumConfirmatoryTrials"`
	MaximumActiveRunBudget       int     `json:"maximumActiveRunBudget"`
	FamilyWiseAlpha              float64 `json:"familyWiseAlpha"`
}

type DiscoveryDerivationPolicySpec struct {
	Name                      string             `json:"name"`
	CurveFamilyContractDigest string             `json:"curveFamilyContractDigest"`
	PointConfidenceLevel      float64            `json:"pointConfidenceLevel"`
	PointPolicies             []PointPolicyEntry `json:"pointPolicies"`
	BatchSaturation           BSatPolicyEntry    `json:"batchSaturation"`
}

type discoveryDerivationPolicyRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	DiscoveryDerivationPolicySpec
}

// DiscoveryDerivationPolicy is an immutable, content-addressed projection of
// the policy file fixed before discovery measurement starts.
type DiscoveryDerivationPolicy struct {
	record discoveryDerivationPolicyRecord
	digest string
}

func FreezeDiscoveryDerivationPolicy(
	spec DiscoveryDerivationPolicySpec,
) (DiscoveryDerivationPolicy, error) {
	record := discoveryDerivationPolicyRecord{
		SchemaVersion:                 DiscoveryDerivationPolicySchemaVersion,
		DiscoveryDerivationPolicySpec: spec,
	}
	return freezeDiscoveryDerivationPolicyRecord(record)
}

func freezeDiscoveryDerivationPolicyRecord(
	record discoveryDerivationPolicyRecord,
) (DiscoveryDerivationPolicy, error) {
	record.PointPolicies = append([]PointPolicyEntry(nil), record.PointPolicies...)
	sort.Slice(record.PointPolicies, func(i, j int) bool {
		if record.PointPolicies[i].Stage != record.PointPolicies[j].Stage {
			return record.PointPolicies[i].Stage < record.PointPolicies[j].Stage
		}
		return record.PointPolicies[i].Name < record.PointPolicies[j].Name
	})
	if err := validateDiscoveryDerivationPolicyRecord(record); err != nil {
		return DiscoveryDerivationPolicy{}, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return DiscoveryDerivationPolicy{}, fmt.Errorf("encode discovery derivation policy: %w", err)
	}
	return DiscoveryDerivationPolicy{record: record, digest: derivationPolicyDigest(payload)}, nil
}

// DecodeDiscoveryDerivationPolicy strict-decodes exactly one JSON policy file.
func DecodeDiscoveryDerivationPolicy(raw []byte) (DiscoveryDerivationPolicy, error) {
	var record discoveryDerivationPolicyRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return DiscoveryDerivationPolicy{}, fmt.Errorf("decode discovery derivation policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DiscoveryDerivationPolicy{}, errors.New("discovery derivation policy contains a trailing JSON value")
	}
	if record.SchemaVersion != DiscoveryDerivationPolicySchemaVersion {
		return DiscoveryDerivationPolicy{}, fmt.Errorf(
			"unsupported discovery derivation-policy schema %q", record.SchemaVersion,
		)
	}
	return freezeDiscoveryDerivationPolicyRecord(record)
}

func (p DiscoveryDerivationPolicy) CanonicalJSON() ([]byte, error) {
	if err := p.requireFrozen(); err != nil {
		return nil, err
	}
	return json.Marshal(p.record)
}

func (p DiscoveryDerivationPolicy) Digest() string { return p.digest }

func (p DiscoveryDerivationPolicy) Name() string { return p.record.Name }

func (p DiscoveryDerivationPolicy) CurveFamilyContractDigest() string {
	return p.record.CurveFamilyContractDigest
}

func (p DiscoveryDerivationPolicy) spec() DiscoveryDerivationPolicySpec {
	spec := p.record.DiscoveryDerivationPolicySpec
	spec.PointPolicies = append([]PointPolicyEntry(nil), spec.PointPolicies...)
	return spec
}

func (p DiscoveryDerivationPolicy) PointPolicy(stage PointPolicyStage) (PointPolicy, error) {
	if err := p.requireFrozen(); err != nil {
		return PointPolicy{}, err
	}
	for _, entry := range p.record.PointPolicies {
		if entry.Stage == stage {
			return PointPolicy{
				Version: entry.Name, MinimumTrials: entry.MinimumTrials,
				ConfidenceLevel: p.record.PointConfidenceLevel,
			}, nil
		}
	}
	return PointPolicy{}, fmt.Errorf("discovery derivation policy has no point policy for stage %q", stage)
}

func (p DiscoveryDerivationPolicy) BSatPolicy() (BSatPolicy, error) {
	if err := p.requireFrozen(); err != nil {
		return BSatPolicy{}, err
	}
	entry := p.record.BatchSaturation
	minimumPointTrials := pointPolicyMinimumTrials(
		p.record.PointPolicies, PointPolicyStageIncrementalPoint,
	)
	return BSatPolicy{
		Version: entry.Name, KMax: entry.KMax,
		RelativeImprovementTolerance: entry.RelativeImprovementTolerance,
		MinimumPointTrials:           minimumPointTrials,
		DiscoveryTrials:              entry.DiscoveryTrials,
		MaximumConfirmatoryTrials:    entry.MaximumConfirmatoryTrials,
		MaximumActiveRunBudget:       entry.MaximumActiveRunBudget,
		FamilyWiseAlpha:              entry.FamilyWiseAlpha,
	}, nil
}

func validateDiscoveryPolicyCurve(
	curve Curve,
	policy DiscoveryDerivationPolicy,
) (BSatPolicy, error) {
	if err := curve.family.requireVerified(); err != nil {
		return BSatPolicy{}, err
	}
	if err := policy.requireFrozen(); err != nil {
		return BSatPolicy{}, err
	}
	idlePolicy, err := policy.PointPolicy(PointPolicyStageIdleBaseline)
	if err != nil {
		return BSatPolicy{}, err
	}
	activePolicy, err := policy.PointPolicy(PointPolicyStageIncrementalPoint)
	if err != nil {
		return BSatPolicy{}, err
	}
	for _, baseline := range curve.baselines {
		if baseline.PolicyVersion != idlePolicy.Version || len(baseline.Trials) < idlePolicy.MinimumTrials ||
			baseline.ConfidenceLevel != idlePolicy.ConfidenceLevel {
			return BSatPolicy{}, errors.New("curve idle baseline does not match the discovery point policy")
		}
	}
	for _, pass := range curve.passes {
		for _, point := range pass.points {
			if point.PointPolicyVersion != activePolicy.Version || len(point.Trials) < activePolicy.MinimumTrials ||
				point.Confidence.Level != activePolicy.ConfidenceLevel ||
				point.PreviousConfidence.Level != activePolicy.ConfidenceLevel {
				return BSatPolicy{}, errors.New("curve point does not match the discovery point policy")
			}
		}
	}
	return policy.BSatPolicy()
}

// requireFrozen checks opaque type state only. The constructor and decoder own
// all content validation and digest construction.
func (p DiscoveryDerivationPolicy) requireFrozen() error {
	if p.record.SchemaVersion != DiscoveryDerivationPolicySchemaVersion {
		return errors.New("discovery derivation policy was not frozen")
	}
	return nil
}

func validateDiscoveryDerivationPolicyRecord(record discoveryDerivationPolicyRecord) error {
	if record.SchemaVersion != DiscoveryDerivationPolicySchemaVersion {
		return fmt.Errorf("unsupported discovery derivation-policy schema %q", record.SchemaVersion)
	}
	spec := record.DiscoveryDerivationPolicySpec
	if !validProbability(spec.PointConfidenceLevel) {
		return errors.New("discovery derivation policy has an invalid point confidence level")
	}
	if !validDerivationPolicyName(spec.Name) {
		return errors.New("discovery derivation-policy name must be a lowercase DNS-style name of at most 63 characters")
	}
	if !cleanCurveValue(spec.CurveFamilyContractDigest) {
		return errors.New("discovery derivation policy has an invalid curve-family contract identity")
	}
	if len(spec.PointPolicies) != 2 {
		return errors.New("discovery derivation policy requires exactly two point policies")
	}
	seenNames := make(map[string]struct{}, len(spec.PointPolicies))
	seenStages := make(map[PointPolicyStage]struct{}, len(spec.PointPolicies))
	for _, entry := range spec.PointPolicies {
		if !validDerivationPolicyName(entry.Name) {
			return errors.New("point-policy name must be a lowercase DNS-style name of at most 63 characters")
		}
		if entry.Stage != PointPolicyStageIdleBaseline && entry.Stage != PointPolicyStageIncrementalPoint {
			return fmt.Errorf("unsupported point-policy stage %q", entry.Stage)
		}
		if entry.MinimumTrials < 3 {
			return fmt.Errorf("point policy %q requires at least three trials", entry.Name)
		}
		if _, exists := seenNames[entry.Name]; exists {
			return fmt.Errorf("duplicate point-policy name %q", entry.Name)
		}
		if _, exists := seenStages[entry.Stage]; exists {
			return fmt.Errorf("duplicate point-policy stage %q", entry.Stage)
		}
		seenNames[entry.Name] = struct{}{}
		seenStages[entry.Stage] = struct{}{}
	}
	if !validDerivationPolicyName(spec.BatchSaturation.Name) {
		return errors.New("batch-saturation policy name must be a lowercase DNS-style name of at most 63 characters")
	}
	entry := spec.BatchSaturation
	minimumPointTrials := pointPolicyMinimumTrials(
		spec.PointPolicies, PointPolicyStageIncrementalPoint,
	)
	if err := validateBSatPolicy(BSatPolicy{
		Version: "pending-policy-digest", KMax: entry.KMax,
		RelativeImprovementTolerance: entry.RelativeImprovementTolerance,
		MinimumPointTrials:           minimumPointTrials,
		DiscoveryTrials:              entry.DiscoveryTrials,
		MaximumConfirmatoryTrials:    entry.MaximumConfirmatoryTrials,
		MaximumActiveRunBudget:       entry.MaximumActiveRunBudget,
		FamilyWiseAlpha:              entry.FamilyWiseAlpha,
	}); err != nil {
		return err
	}
	return nil
}

func pointPolicyMinimumTrials(entries []PointPolicyEntry, stage PointPolicyStage) int {
	for _, entry := range entries {
		if entry.Stage == stage {
			return entry.MinimumTrials
		}
	}
	return 0
}

func validDerivationPolicyName(name string) bool {
	return len(name) >= 1 && len(name) <= 63 && derivationPolicyNamePattern.MatchString(name)
}

func derivationPolicyDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}
