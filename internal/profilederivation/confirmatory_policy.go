package profilederivation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
)

const ConfirmatoryDerivationPolicySchemaVersion = "chill.dacs.io/confirmatory-derivation-policy.v1alpha4"

type ConfirmatoryBatchAllocation struct {
	Batch  int `json:"batch"`
	Trials int `json:"trials"`
}

type ConfirmatoryAllocation struct {
	Status             BSatStatus                    `json:"status"`
	Code               string                        `json:"code"`
	CandidateBatch     int                           `json:"candidateBatch"`
	ConfirmatoryTrials *int                          `json:"confirmatoryTrials,omitempty"`
	Batches            []ConfirmatoryBatchAllocation `json:"batches,omitempty"`
}

type confirmatoryDerivationPolicyRecord struct {
	SchemaVersion string `json:"schemaVersion"`
	DiscoveryDerivationPolicySpec
	Confirmation ConfirmatoryAllocation `json:"confirmation"`
}

// ConfirmatoryDerivationPolicy is the second immutable policy artifact. It
// repeats the discovery scientific fields and adds only the realized frozen
// allocation selected before confirmatory measurement starts.
type ConfirmatoryDerivationPolicy struct {
	record confirmatoryDerivationPolicyRecord
}

func FreezeConfirmatoryDerivationPolicy(
	discovery DiscoveryDerivationPolicy,
	sizing BSatSizingResult,
) (ConfirmatoryDerivationPolicy, error) {
	if err := discovery.requireFrozen(); err != nil {
		return ConfirmatoryDerivationPolicy{}, err
	}
	allocation := ConfirmatoryAllocation{
		Status: sizing.Status, Code: sizing.Code, CandidateBatch: sizing.CandidateBatch,
	}
	if sizing.Status == BSatCandidateOnly && sizing.Code == BSatCodeConfirmatoryTrialsSized &&
		sizing.ConfirmatoryTrials != nil {
		trials := *sizing.ConfirmatoryTrials
		allocation.ConfirmatoryTrials = &trials
		for _, batch := range sizing.ConfirmatoryBatches {
			allocation.Batches = append(allocation.Batches, ConfirmatoryBatchAllocation{
				Batch: batch, Trials: trials,
			})
		}
	} else if sizing.Status != BSatUnavailable ||
		sizing.Code != BSatCodeConfirmatoryBudgetInsufficient || sizing.ConfirmatoryTrials != nil {
		return ConfirmatoryDerivationPolicy{}, errors.New("confirmatory policy requires a sized or budget-insufficient outcome")
	}
	record := confirmatoryDerivationPolicyRecord{
		SchemaVersion:                 ConfirmatoryDerivationPolicySchemaVersion,
		DiscoveryDerivationPolicySpec: discovery.spec(),
		Confirmation:                  allocation,
	}
	return freezeConfirmatoryDerivationPolicyRecord(record)
}

// DecodeConfirmatoryDerivationPolicy strict-decodes exactly one JSON policy
// file and verifies it against both the discovery artifact and its realized
// sizing result.
func DecodeConfirmatoryDerivationPolicy(
	raw []byte,
	discovery DiscoveryDerivationPolicy,
	sizing BSatSizingResult,
) (ConfirmatoryDerivationPolicy, error) {
	var record confirmatoryDerivationPolicyRecord
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return ConfirmatoryDerivationPolicy{}, fmt.Errorf("decode confirmatory derivation policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ConfirmatoryDerivationPolicy{}, errors.New("confirmatory derivation policy contains a trailing JSON value")
	}
	if record.SchemaVersion != ConfirmatoryDerivationPolicySchemaVersion {
		return ConfirmatoryDerivationPolicy{}, fmt.Errorf(
			"unsupported confirmatory derivation-policy schema %q", record.SchemaVersion,
		)
	}
	parsed, err := freezeConfirmatoryDerivationPolicyRecord(record)
	if err != nil {
		return ConfirmatoryDerivationPolicy{}, err
	}
	expected, err := FreezeConfirmatoryDerivationPolicy(discovery, sizing)
	if err != nil {
		return ConfirmatoryDerivationPolicy{}, err
	}
	if !reflect.DeepEqual(parsed.record, expected.record) {
		return ConfirmatoryDerivationPolicy{}, errors.New("confirmatory derivation policy does not match frozen discovery sizing")
	}
	return parsed, nil
}

func (p ConfirmatoryDerivationPolicy) CanonicalJSON() ([]byte, error) {
	if err := p.requireFrozen(); err != nil {
		return nil, err
	}
	return json.Marshal(p.record)
}

func (p ConfirmatoryDerivationPolicy) Name() string { return p.record.Name }

func (p ConfirmatoryDerivationPolicy) CurveFamilyName() string {
	return p.record.CurveFamilyName
}

func (p ConfirmatoryDerivationPolicy) CandidateBatch() int {
	return p.record.Confirmation.CandidateBatch
}

func (p ConfirmatoryDerivationPolicy) ConfirmatoryTrials() *int {
	if p.record.Confirmation.ConfirmatoryTrials == nil {
		return nil
	}
	trials := *p.record.Confirmation.ConfirmatoryTrials
	return &trials
}

func (p ConfirmatoryDerivationPolicy) ConfirmatoryBatches() []int {
	result := make([]int, len(p.record.Confirmation.Batches))
	for index, allocation := range p.record.Confirmation.Batches {
		result[index] = allocation.Batch
	}
	return result
}

func (p ConfirmatoryDerivationPolicy) BSatPolicy() (BSatPolicy, error) {
	discovery, err := p.discoveryPolicy()
	if err != nil {
		return BSatPolicy{}, err
	}
	return discovery.BSatPolicy()
}

func (p ConfirmatoryDerivationPolicy) discoveryPolicy() (DiscoveryDerivationPolicy, error) {
	if err := p.requireFrozen(); err != nil {
		return DiscoveryDerivationPolicy{}, err
	}
	record := discoveryDerivationPolicyRecord{
		SchemaVersion:                 DiscoveryDerivationPolicySchemaVersion,
		DiscoveryDerivationPolicySpec: p.record.DiscoveryDerivationPolicySpec,
	}
	record.PointPolicies = append([]PointPolicyEntry(nil), record.PointPolicies...)
	return freezeDiscoveryDerivationPolicyRecord(record)
}

func validateConfirmatoryPolicyCurve(
	curve Curve,
	policy ConfirmatoryDerivationPolicy,
) (DiscoveryDerivationPolicy, BSatPolicy, error) {
	discovery, err := policy.discoveryPolicy()
	if err != nil {
		return DiscoveryDerivationPolicy{}, BSatPolicy{}, err
	}
	bsat, err := validateDiscoveryPolicyCurve(curve, discovery)
	if err != nil {
		return DiscoveryDerivationPolicy{}, BSatPolicy{}, err
	}
	return discovery, bsat, nil
}

func freezeConfirmatoryDerivationPolicyRecord(
	record confirmatoryDerivationPolicyRecord,
) (ConfirmatoryDerivationPolicy, error) {
	if record.SchemaVersion != ConfirmatoryDerivationPolicySchemaVersion {
		return ConfirmatoryDerivationPolicy{}, fmt.Errorf(
			"unsupported confirmatory derivation-policy schema %q", record.SchemaVersion,
		)
	}
	discoveryRecord := discoveryDerivationPolicyRecord{
		SchemaVersion:                 DiscoveryDerivationPolicySchemaVersion,
		DiscoveryDerivationPolicySpec: record.DiscoveryDerivationPolicySpec,
	}
	discovery, err := freezeDiscoveryDerivationPolicyRecord(discoveryRecord)
	if err != nil {
		return ConfirmatoryDerivationPolicy{}, err
	}
	record.DiscoveryDerivationPolicySpec = discovery.spec()
	record.Confirmation.Batches = append(
		[]ConfirmatoryBatchAllocation(nil), record.Confirmation.Batches...,
	)
	if record.Confirmation.ConfirmatoryTrials != nil {
		trials := *record.Confirmation.ConfirmatoryTrials
		record.Confirmation.ConfirmatoryTrials = &trials
	}
	sort.Slice(record.Confirmation.Batches, func(i, j int) bool {
		return record.Confirmation.Batches[i].Batch < record.Confirmation.Batches[j].Batch
	})
	if err := validateConfirmatoryDerivationPolicyRecord(record, discovery); err != nil {
		return ConfirmatoryDerivationPolicy{}, err
	}
	return ConfirmatoryDerivationPolicy{record: record}, nil
}

func validateConfirmatoryDerivationPolicyRecord(
	record confirmatoryDerivationPolicyRecord,
	discovery DiscoveryDerivationPolicy,
) error {
	if record.SchemaVersion != ConfirmatoryDerivationPolicySchemaVersion {
		return fmt.Errorf("unsupported confirmatory derivation-policy schema %q", record.SchemaVersion)
	}
	if !reflect.DeepEqual(record.DiscoveryDerivationPolicySpec, discovery.spec()) {
		return errors.New("confirmatory derivation policy does not bind its repeated discovery policy")
	}
	bsat, err := discovery.BSatPolicy()
	if err != nil {
		return err
	}
	allocation := record.Confirmation
	if allocation.CandidateBatch < 1 || allocation.CandidateBatch >= bsat.KMax {
		return errors.New("confirmatory derivation policy has an invalid allocation")
	}
	if allocation.Status == BSatUnavailable && allocation.Code == BSatCodeConfirmatoryBudgetInsufficient {
		if allocation.ConfirmatoryTrials != nil || len(allocation.Batches) != 0 {
			return errors.New("budget-insufficient confirmatory policy cannot contain a Run allocation")
		}
		return nil
	}
	if allocation.Status != BSatCandidateOnly || allocation.Code != BSatCodeConfirmatoryTrialsSized ||
		allocation.ConfirmatoryTrials == nil ||
		*allocation.ConfirmatoryTrials < bsat.MinimumPointTrials ||
		*allocation.ConfirmatoryTrials > bsat.MaximumConfirmatoryTrials ||
		len(allocation.Batches) != bsat.KMax-allocation.CandidateBatch+1 {
		return errors.New("confirmatory derivation policy has an invalid allocation")
	}
	for index, batch := range allocation.Batches {
		if batch.Batch != allocation.CandidateBatch+index ||
			batch.Trials != *allocation.ConfirmatoryTrials {
			return errors.New("confirmatory derivation policy allocation is not the complete frozen domain")
		}
	}
	confirmatoryTrials := *allocation.ConfirmatoryTrials
	confirmatoryRuns := len(allocation.Batches) * confirmatoryTrials
	totalRuns := bsat.KMax*bsat.DiscoveryTrials + confirmatoryRuns
	if totalRuns > bsat.MaximumActiveRunBudget {
		return errors.New("confirmatory derivation policy allocation exceeds the active-Run budget")
	}
	return nil
}

// requireFrozen checks opaque type state only. The constructor and decoder own
// all content validation.
func (p ConfirmatoryDerivationPolicy) requireFrozen() error {
	if p.record.SchemaVersion != ConfirmatoryDerivationPolicySchemaVersion {
		return errors.New("confirmatory derivation policy was not frozen")
	}
	return nil
}
