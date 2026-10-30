package kube

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"

	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/types"
)

const profileReuseAnnotation = "chill.dacs.io/profile-reuse"
const profileReuseRejected = "Rejected"

type profileReuseRequest struct {
	SourceProfileID           string  `json:"sourceProfileID"`
	MaximumRelativeDifference float64 `json:"maximumRelativeDifference"`
	MinimumMeasurementSeconds float64 `json:"minimumMeasurementSeconds"`
	MinimumTrials             int     `json:"minimumTrials"`
}

// Confirmations are never independently eligible. Resolve only direct, latest
// sources before projecting the accepted rows onto the target physical Node.
func applyProfileReuse(view CatalogView, requests map[string]string) (CatalogView, error) {
	if len(requests) == 0 {
		var err error
		view.Profiles, err = latestConditionProfiles(view.Profiles)
		return view, err
	}
	direct := make([]serving.Profile, 0, len(view.Profiles))
	confirmations := make([]serving.Profile, 0, len(requests))
	for _, profile := range view.Profiles {
		if _, found := requests[profile.ID]; found {
			confirmations = append(confirmations, profile)
		} else {
			direct = append(direct, profile)
		}
	}
	var err error
	view.Profiles, err = latestConditionProfiles(direct)
	if err != nil {
		return CatalogView{}, err
	}
	sources := make(map[string]serving.Profile, len(view.Profiles))
	for _, profile := range view.Profiles {
		sources[profile.ID] = profile
	}
	for _, target := range confirmations {
		request, parseErr := decodeProfileReuseRequest(requests[target.ID])
		source, found := sources[request.SourceProfileID]
		check := serving.ProfileReuseCheck{
			SourceProfileID: request.SourceProfileID, ConfirmationProfileID: target.ID,
			SourceReference: source.Reference, ConfirmationReference: target.Reference,
			SourceNode:                 serving.NodeReference{Name: source.NodeName, UID: source.Condition.NodeUID},
			TargetNode:                 serving.NodeReference{Name: target.NodeName, UID: target.Condition.NodeUID},
			MaximumRelativeDifference:  request.MaximumRelativeDifference,
			MinimumMeasurementSeconds:  request.MinimumMeasurementSeconds,
			MinimumTrials:              request.MinimumTrials,
			ConfirmationActiveSeconds:  target.Condition.ActiveProtocol.DurationSeconds,
			ConfirmationIdleSeconds:    target.Condition.IdleProtocol.DurationSeconds,
			ConfirmationTrialsPerBatch: target.TrialsPerBatch,
			SourceIdleWatts:            source.IdleBaselineWatts, TargetIdleWatts: target.IdleBaselineWatts,
			Outcome: profileReuseRejected,
		}
		switch {
		case parseErr != nil:
			check.Reason = parseErr.Error()
		case !found:
			check.Reason = "source must be a current direct profile in the same namespace"
		default:
			if err := compareReuseProfiles(source, target, &check); err != nil {
				check.Reason = err.Error()
			} else {
				check.Outcome = measurementAccepted
				reused := reusedProfile(source, target, check)
				view.Profiles = append(view.Profiles, reused)
				// Keep confirmation measurements independent from the referenced
				// source. Never synthesize total energy from the mixed serving row.
				if facts, found := view.MeasurementFacts[target.ID]; found {
					view.MeasurementFacts[reused.ID] = cloneMeasurementFacts(facts)
				}
			}
		}
		view.ReuseChecks = append(view.ReuseChecks, check)
		if check.Outcome != measurementAccepted {
			namespace, nameUID, _ := strings.Cut(target.ID, "/")
			name, _, _ := strings.Cut(nameUID, "/")
			view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{
				Source: types.NamespacedName{Namespace: namespace, Name: name},
				Code:   "ProfileReuseRejected", Reason: check.Reason + "; independent target measurement required",
			})
		}
	}
	slices.SortFunc(view.ReuseChecks, func(a, b serving.ProfileReuseCheck) int {
		return strings.Compare(a.ConfirmationProfileID, b.ConfirmationProfileID)
	})
	view.Profiles, err = latestConditionProfiles(view.Profiles)
	return view, err
}

func decodeProfileReuseRequest(raw string) (profileReuseRequest, error) {
	request := profileReuseRequest{MinimumMeasurementSeconds: 30, MinimumTrials: 3}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return profileReuseRequest{}, fmt.Errorf("invalid profile reuse annotation: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return profileReuseRequest{}, errors.New("profile reuse annotation must contain one JSON object")
	}
	if request.SourceProfileID == "" || !(request.MaximumRelativeDifference > 0 && request.MaximumRelativeDifference < 1) {
		return profileReuseRequest{}, errors.New("sourceProfileID and finite maximumRelativeDifference in (0,1) required")
	}
	if math.IsNaN(request.MinimumMeasurementSeconds) || math.IsInf(request.MinimumMeasurementSeconds, 0) || request.MinimumMeasurementSeconds <= 0 || request.MinimumTrials < 2 {
		return profileReuseRequest{}, errors.New("minimumMeasurementSeconds must be finite and positive; minimumTrials must be at least 2")
	}
	return request, nil
}

func reuseCondition(condition profilederivation.CurvePlanSpec) profilederivation.CurvePlanSpec {
	condition.Name, condition.NodeUID = "", ""
	condition.MetricSource = profilederivation.CurveMetricSourceContract{}
	condition.ActiveProtocol.DurationSeconds, condition.ActiveProtocol.MetricDurationSeconds = 0, 0
	condition.IdleProtocol.DurationSeconds, condition.IdleProtocol.MetricDurationSeconds = 0, 0
	if condition.RuntimeAccess != nil {
		access := *condition.RuntimeAccess
		access.GPUUUID = ""
		condition.RuntimeAccess = &access
	}
	return condition
}

func compareReuseProfiles(source, target serving.Profile, check *serving.ProfileReuseCheck) error {
	sourceNamespace, _, _ := strings.Cut(source.ID, "/")
	targetNamespace, _, _ := strings.Cut(target.ID, "/")
	if sourceNamespace != targetNamespace || source.Reuse != nil ||
		source.Condition.NodeUID == target.Condition.NodeUID || source.NodeName == target.NodeName {
		return errors.New("reuse requires a direct source and a distinct physical Node in the same namespace")
	}
	if !completeNativeProfile(source) || !completeNativeProfile(target) ||
		!reflect.DeepEqual(reuseCondition(source.Condition), reuseCondition(target.Condition)) {
		return errors.New("reuse device class, immutable images, resource allocation or execution condition mismatch")
	}
	if float64(target.Condition.ActiveProtocol.DurationSeconds) < check.MinimumMeasurementSeconds || float64(target.Condition.IdleProtocol.DurationSeconds) < check.MinimumMeasurementSeconds || target.TrialsPerBatch < check.MinimumTrials {
		return fmt.Errorf("confirmation requires active/idle windows >=%g seconds and >=%d trials", check.MinimumMeasurementSeconds, check.MinimumTrials)
	}
	sourceRuntime, targetRuntime := *source.RuntimeMetadata, *target.RuntimeMetadata
	sourceRuntime.GPUUUID, targetRuntime.GPUUUID = "", ""
	if !reflect.DeepEqual(sourceRuntime, targetRuntime) {
		return errors.New("actual runner versions, resources or initialized batch conditions differ")
	}
	idle, err := reuseRelativeDifference(source.IdleBaselineWatts, target.IdleBaselineWatts)
	if err != nil {
		return fmt.Errorf("idle wall power: %w", err)
	}
	check.IdlePowerRelativeDifference = &idle
	exceeded := idle > check.MaximumRelativeDifference
	for _, batch := range source.Batches {
		index := slices.IndexFunc(target.Batches, func(candidate serving.BatchProfile) bool { return candidate.Batch == batch.Batch })
		if index < 0 {
			return fmt.Errorf("confirmation missing source batch %d", batch.Batch)
		}
		sourceMean, _ := source.MeanNanos(batch.Batch)
		targetMean, _ := target.MeanNanos(batch.Batch)
		service, err := reuseRelativeDifference(sourceMean, targetMean)
		if err != nil {
			return fmt.Errorf("batch %d service: %w", batch.Batch, err)
		}
		power, err := reuseRelativeDifference(batch.MeanWallWatts, target.Batches[index].MeanWallWatts)
		if err != nil {
			return fmt.Errorf("batch %d active wall power: %w", batch.Batch, err)
		}
		check.Batches = append(check.Batches, serving.ProfileReuseBatchCheck{
			Batch: batch.Batch, SourceMeanServiceNanos: sourceMean, TargetMeanServiceNanos: targetMean,
			ServiceRelativeDifference: service,
			SourceMeanWallWatts:       batch.MeanWallWatts, TargetMeanWallWatts: target.Batches[index].MeanWallWatts,
			PowerRelativeDifference: power,
		})
		exceeded = exceeded || service > check.MaximumRelativeDifference || power > check.MaximumRelativeDifference
	}
	if exceeded {
		return errors.New("service or active/idle wall power relative difference exceeds configured maximum")
	}
	return nil
}

func reuseRelativeDifference(source, target float64) (float64, error) {
	if !(source > 0) || !(target > 0) || math.IsInf(source, 0) || math.IsInf(target, 0) || math.IsNaN(source) || math.IsNaN(target) {
		return 0, errors.New("finite positive source and confirmation values required")
	}
	difference := math.Abs(target-source) / source
	if math.IsInf(difference, 0) {
		return 0, errors.New("relative difference is not finite")
	}
	return difference, nil
}

func reusedProfile(source, target serving.Profile, check serving.ProfileReuseCheck) serving.Profile {
	profile := target
	profile.Batches = nil
	profile = cloneCatalogProfile(profile)
	// Annotations can change; never overwrite an immutable startup bundle
	// when the pinned source or confirmation policy changes.
	policy := fmt.Sprintf("%s\x00%g\x00%g\x00%d", check.SourceProfileID, check.MaximumRelativeDifference, check.MinimumMeasurementSeconds, check.MinimumTrials)
	profile.ID += fmt.Sprintf("/reuse-%x", sha256.Sum256([]byte(policy)))
	check.Batches = slices.Clone(check.Batches)
	if check.IdlePowerRelativeDifference != nil {
		value := *check.IdlePowerRelativeDifference
		check.IdlePowerRelativeDifference = &value
	}
	profile.Reuse = &check
	profile.ConfidenceLevel = source.ConfidenceLevel
	profile.Batches = slices.Clone(source.Batches)
	for index := range profile.Batches {
		batch := &profile.Batches[index]
		batch.DurationNanos = slices.Clone(batch.DurationNanos)
		confirmation := target.Batches[slices.IndexFunc(target.Batches, func(candidate serving.BatchProfile) bool { return candidate.Batch == batch.Batch })]
		batch.MemoryBytes = max(batch.MemoryBytes, confirmation.MemoryBytes)
		batch.MeanWallWatts = confirmation.MeanWallWatts
	}
	profile.Evidence = append(profile.Evidence, source.Reference)
	profile.Evidence = append(profile.Evidence, source.Evidence...)
	// MetricSource and runtime delivery stay target-owned; source UUID/meter
	// identities occur only in the retained original evidence.
	return profile
}
