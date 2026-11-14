package kube

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const invalidEvidenceCode = "InvalidEvidence"
const catalogCompletePhase = "Complete"

// measurementResultUnreadableCode reports a Complete measurement whose stored
// result is not a session result this catalog reads.
const measurementResultUnreadableCode = "MeasurementResultUnreadable"

// Catalog lists measurement requests and retains one profile per
// name/UID/resourceVersion, built from the request's one stored session
// result object and nothing else. Inference never reads requests or downloads
// results; callers own the mutable parts of each returned view.
type Catalog struct {
	store           *Store
	objects         evidencestore.LocationReader
	maxObjectBytes  int64
	current         map[types.NamespacedName]catalogProfile
	sourceMu        sync.RWMutex
	sourceCheckedAt time.Time
	sourceError     string
	revision        string
}

type catalogProfile struct {
	uid     types.UID
	version string
	profile serving.Profile
	facts   serving.MeasurementFacts
	issues  []CatalogIssue
}

type CatalogIssue struct {
	Source types.NamespacedName `json:"source"`
	Code   string               `json:"code"`
	Reason string               `json:"reason"`
}

type CatalogView struct {
	Profiles            []serving.Profile                        `json:"profiles"`
	CurrentMeasurements []profilederivation.MeasurementSelection `json:"currentMeasurements"`
	Insufficiencies     []CatalogIssue                           `json:"insufficiencies"`
	// ReuseChecks is always empty: profiles are measured per node. The field
	// stays until its query readers move to the session result.
	ReuseChecks      []serving.ProfileReuseCheck         `json:"reuseChecks,omitempty"`
	MeasurementFacts map[string]serving.MeasurementFacts `json:"measurementFacts"`
	SourceCheckedAt  time.Time                           `json:"-"`
	SourceError      string                              `json:"-"`
	Revision         string                              `json:"-"`
}

func NewCatalog(store *Store, objects evidencestore.LocationReader, maxObjectBytes int64) (*Catalog, error) {
	if store == nil || store.client == nil {
		return nil, errors.New("native Kubernetes store is required")
	}
	if objects == nil {
		return nil, errors.New("catalog object reader is required")
	}
	return &Catalog{
		store: store, objects: objects, maxObjectBytes: maxObjectBytes,
		current: make(map[types.NamespacedName]catalogProfile),
	}, nil
}

// Refresh reads every Complete measurement in namespaces. A bad source cannot
// contribute a partial profile; other sources remain usable. Cancellation
// returns no view, so callers cannot publish a partial refresh.
func (c *Catalog) Refresh(ctx context.Context, namespaces []string) (view CatalogView, err error) {
	defer func() {
		c.sourceMu.Lock()
		defer c.sourceMu.Unlock()
		if err != nil {
			c.sourceError = err.Error()
			return
		}
		c.sourceCheckedAt, c.sourceError, c.revision = view.SourceCheckedAt, "", view.Revision
	}()
	if err := ctx.Err(); err != nil {
		return CatalogView{}, err
	}
	view = CatalogView{Profiles: []serving.Profile{}, Insufficiencies: []CatalogIssue{}, MeasurementFacts: map[string]serving.MeasurementFacts{}}
	var revisions []string
	seen := map[types.NamespacedName]struct{}{}
	var candidates []profilederivation.MeasurementSelection
	var replacedProfiles []serving.Profile
	names, replacements := map[string]string{}, map[string]string{}
	for _, namespace := range namespaces {
		campaigns := &edgev1alpha1.BatchEnergyOptimizationList{}
		if err := c.store.client.List(ctx, campaigns, client.InNamespace(namespace)); err != nil {
			return CatalogView{}, err
		}
		for i := range campaigns.Items {
			campaign := &campaigns.Items[i]
			id := campaign.Namespace + "/" + campaign.Name + "/" + string(campaign.UID)
			names[campaign.Namespace+"/"+campaign.Name] = id
			if name := campaign.Annotations[profilederivation.ReplacesMeasurementAnnotation]; name != "" && campaign.DeletionTimestamp == nil {
				replacements[id] = campaign.Namespace + "/" + name
			}
		}
		replaced := profilederivation.ReplacedMeasurements(replacements, names)
		replacementDiagnostics := replacementCampaignSelection(campaigns.Items)
		for i := range campaigns.Items {
			campaign := &campaigns.Items[i]
			revisions = append(revisions, campaign.Namespace+"/"+campaign.Name+"/"+string(campaign.UID)+"/"+campaign.ResourceVersion)
			id := campaign.Namespace + "/" + campaign.Name + "/" + string(campaign.UID)
			if campaign.DeletionTimestamp != nil {
				continue
			}
			completedAt := measurementCompletedAt(campaign)
			state := campaign.Spec.State.Candidate
			candidateIndex := len(candidates)
			candidates = append(candidates, profilederivation.MeasurementSelection{
				ID: id, Model: state.ModelSpec, DeviceClass: state.DeviceClass, ExecutionPath: state.ExecutionPath, PowerMode: state.PowerMode,
				MeasuredAt: profilederivation.MeasurementTime(time.Time{}, completedAt),
			})
			source := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
			if reason := replacementDiagnostics[campaign.Name]; reason != "" {
				view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: "ReplacementSelection", Reason: reason})
			}
			if replaced[id] {
				if campaign.Status.Phase == catalogCompletePhase && campaign.Status.ObservedGeneration == campaign.Generation {
					seen[source] = struct{}{}
					profile, _, readErr := c.readProfile(ctx, campaign)
					if ctx.Err() != nil {
						return CatalogView{}, ctx.Err()
					}
					if readErr == nil && len(profile.Batches) > 0 {
						replacedProfiles = append(replacedProfiles, profile)
					}
				}
				continue
			}
			if campaign.Status.Phase != catalogCompletePhase || campaign.Status.ObservedGeneration != campaign.Generation {
				continue
			}
			if reason, invalid := campaign.Annotations[profilingrun.InvalidAnnotation]; invalid {
				view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: measurementInvalidCode, Reason: reason})
				continue
			}
			if _, duplicate := seen[source]; duplicate {
				continue
			}
			seen[source] = struct{}{}
			profile, issues, err := c.readProfile(ctx, campaign)
			if err != nil {
				if ctx.Err() != nil {
					return CatalogView{}, ctx.Err()
				}
				view.Insufficiencies = append(view.Insufficiencies, CatalogIssue{Source: source, Code: invalidEvidenceCode, Reason: err.Error()})
				continue
			}
			view.Insufficiencies = append(view.Insufficiencies, issues...)
			candidates[candidateIndex].MeasuredAt = profilederivation.MeasurementTime(profile.MeasuredAt, completedAt)
			if len(profile.Batches) > 0 {
				view.Profiles = append(view.Profiles, profile)
				view.MeasurementFacts[profile.ID] = cloneMeasurementFacts(c.current[source].facts)
			}
		}
	}
	for source := range c.current {
		if _, listed := seen[source]; !listed {
			delete(c.current, source)
		}
	}
	view.Profiles, err = serving.ExcludeReplacedConditionProfiles(append(view.Profiles, replacedProfiles...), profilederivation.ReplacedMeasurements(replacements, names))
	if err != nil {
		return CatalogView{}, err
	}
	view.CurrentMeasurements = profilederivation.CurrentMeasurements(candidates, profilederivation.ReplacedMeasurements(replacements, names))
	slices.Sort(revisions)
	view.Revision = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(revisions, "\x00"))))
	if err := ctx.Err(); err != nil {
		return CatalogView{}, err
	}
	view.SourceCheckedAt = time.Now().UTC()
	return view, nil
}

func measurementCompletedAt(campaign *edgev1alpha1.BatchEnergyOptimization) time.Time {
	for _, condition := range campaign.Status.Conditions {
		if condition.Type == "MeasurementComplete" {
			return condition.LastTransitionTime.Time
		}
	}
	return time.Time{}
}

// SourceStatus reports the last real source check, not the snapshot read time.
func (c *Catalog) SourceStatus() (time.Time, string, string) {
	c.sourceMu.RLock()
	defer c.sourceMu.RUnlock()
	return c.sourceCheckedAt, c.sourceError, c.revision
}

// Operator replacement is selection, not a second admission policy. Differences
// and ineffective annotations are diagnostics; normal evidence safety remains.
func replacementCampaignSelection(campaigns []edgev1alpha1.BatchEnergyOptimization) map[string]string {
	diagnostics := make(map[string]string)
	byName := make(map[string]*edgev1alpha1.BatchEnergyOptimization, len(campaigns))
	for i := range campaigns {
		byName[campaigns[i].Name] = &campaigns[i]
	}
	for i := range campaigns {
		replacement := &campaigns[i]
		name := replacement.Annotations[profilederivation.ReplacesMeasurementAnnotation]
		if name == "" || replacement.DeletionTimestamp != nil {
			continue
		}
		original := byName[name]
		if original == nil || original == replacement {
			diagnostics[replacement.Name] = "replacement ignored: target must be a distinct existing campaign"
			continue
		}
		diagnostics[name] = "superseded by " + replacement.Name + "; original evidence retained"
		if !reflect.DeepEqual(original.Spec, replacement.Spec) {
			diagnostics[replacement.Name] = "operator replacement changes specification; selection retained as declared"
		}
	}
	return diagnostics
}

// CurrentProfiles is the executable selection, separate from public history.
// A latest failed measurement has no profile, so cannot revive an old success.
func (view CatalogView) CurrentProfiles() []serving.Profile {
	current := make(map[string]bool, len(view.CurrentMeasurements))
	for _, measurement := range view.CurrentMeasurements {
		current[measurement.ID] = true
	}
	profiles := make([]serving.Profile, 0, len(current))
	for _, profile := range view.Profiles {
		if current[profile.ID] {
			profiles = append(profiles, profile)
		}
	}
	return profiles
}

// readProfile builds one serving profile from the stored session result.
func (c *Catalog) readProfile(ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization) (serving.Profile, []CatalogIssue, error) {
	source := types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}
	if campaign.Status.Result == nil {
		delete(c.current, source)
		return serving.Profile{}, nil, errors.New("completed measurement has no stored result reference")
	}
	if admitted, found := c.current[source]; found &&
		admitted.uid == campaign.UID && admitted.version == campaign.ResourceVersion {
		return cloneCatalogProfile(admitted.profile), slices.Clone(admitted.issues), nil
	}
	// A new identity/version is read again; the old success is never retained
	// if that read fails.
	delete(c.current, source)
	stored := campaign.Status.Result
	reference := evidencestore.ObjectIdentity{Store: stored.Store, Key: stored.Key, Size: stored.Size}
	raw, err := c.readObject(ctx, reference)
	if err != nil {
		return serving.Profile{}, nil, fmt.Errorf("read stored result %s: %w", reference.Key, err)
	}
	result, err := profilederivation.DecodeSessionResult(raw)
	if err != nil {
		issues := []CatalogIssue{{Source: source, Code: measurementResultUnreadableCode, Reason: fmt.Sprintf("stored result %s: %v", reference.Key, err)}}
		c.current[source] = catalogProfile{uid: campaign.UID, version: campaign.ResourceVersion, issues: issues}
		return serving.Profile{}, slices.Clone(issues), nil
	}
	profile, err := sessionProfile(campaign, reference, result)
	if err != nil {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: err.Error()}}, nil
	}
	if len(profile.Batches) == 0 {
		return serving.Profile{}, []CatalogIssue{{Source: source, Code: "MeasurementEvidenceMissing", Reason: "the session has no batch with service samples, measured memory, a valid saturation level and identified joules per input"}}, nil
	}
	facts := serving.MeasurementFacts{
		Request:         profilederivation.MeasurementRequestReference{Namespace: campaign.Namespace, Name: campaign.Name, UID: string(campaign.UID)},
		ResultReference: profilederivation.RawEvidenceIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size},
		Condition:       profile.Condition, Batches: []profilederivation.MeasurementBatchFacts{},
	}
	c.current[source] = catalogProfile{
		uid: campaign.UID, version: campaign.ResourceVersion, profile: cloneCatalogProfile(profile), facts: cloneMeasurementFacts(facts),
	}
	return profile, nil, nil
}

// sessionProfile maps a session result onto the serving profile. The condition
// keeps the curve-plan shape its readers use: the planned batch sizes are the
// measured ones, and the runtime version and provider come from the executor's
// runtime report.
func sessionProfile(campaign *edgev1alpha1.BatchEnergyOptimization, reference evidencestore.ObjectIdentity, result profilederivation.SessionResult) (serving.Profile, error) {
	if result.Condition.Firstfloor == nil || result.RuntimeMetadata == nil {
		return serving.Profile{}, errors.New("native runtime/settings unavailable")
	}
	var meter metricobserver.SourceIdentity
	if err := json.Unmarshal(result.MeterSource, &meter); err != nil {
		slog.Warn("session meter source unavailable", "reason", err)
	}
	session := result.Condition
	condition := profilederivation.CurvePlanSpec{
		LogicalModel: session.LogicalModel, DeviceClass: session.DeviceClass, NodeUID: session.NodeUID,
		RuntimeFamily: session.RuntimeFamily, Backend: session.Backend, Firstfloor: session.Firstfloor,
		RuntimeAccess: session.RuntimeAccess, RuntimeImage: session.RuntimeImage, Actor: session.Actor,
		PowerMode: session.PowerMode, Artifact: session.Artifact, Preprocessor: session.Preprocessor,
		MetricSource:   profilederivation.CurveMetricSourceContract{SourceKind: meter.Kind, NodeName: meter.NodeName, Endpoint: meter.Endpoint, PowerMetric: meter.Metric},
		ActiveProtocol: profilederivation.CurveProtocolContract{Dataset: session.Dataset},
	}
	if metadata := result.RuntimeMetadata; metadata != nil {
		condition.RuntimeVersion = metadata.RuntimeVersion
		if len(metadata.Providers) > 0 {
			condition.RuntimeProvider = metadata.Providers[0]
		}
	}
	profile := serving.Profile{
		ID: types.NamespacedName{Namespace: campaign.Namespace, Name: campaign.Name}.String() + "/" + string(campaign.UID), Reference: reference,
		SourceVersion: campaign.ResourceVersion,
		Evidence:      []evidencestore.ObjectIdentity{reference}, NodeName: session.NodeName,
		MeasuredAt: measurementCompletedAt(campaign), RuntimeMetadata: result.RuntimeMetadata,
		RuntimeDelivery: slices.Clone(result.RuntimeDelivery), MetricSource: meter,
		Power: &serving.ProfilePower{IdleWatts: result.Power.IdleWatts, AwakeStepWatts: result.Power.AwakeStepWatts, HoldSeconds: result.Power.HoldSeconds},
	}
	for _, batch := range result.Batches {
		if _, identified := batch.JoulesPerInput.Value(); !identified {
			continue
		}
		if batch.PeakMemoryBytes <= 0 || batch.SaturationInputRate == nil || len(batch.ServiceNanos) < 2 {
			continue
		}
		samples := make([]firstfloor.ServiceSample, len(batch.ServiceNanos))
		for k, service := range batch.ServiceNanos {
			samples[k] = firstfloor.ServiceSample{ServiceDurationNanos: service, PrecedingIdleNanos: &batch.PrecedingIdleNanos[k], TrialID: string(campaign.UID)}
		}
		distribution, err := firstfloor.NewServiceDistribution(samples)
		if err != nil {
			slog.Warn("batch service records unavailable", "batch", batch.Batch, "reason", err)
			continue
		}
		slope := batch.JoulesPerInput
		out := serving.BatchProfile{
			Batch: batch.Batch, ServiceSamples: samples, ServiceDistribution: distribution,
			MemoryBytes: uint64(batch.PeakMemoryBytes),
			MemoryBasis: profilederivation.MemoryBasis{Observation: batch.MemoryKind,
				Evidence: profilederivation.RawEvidenceIdentity{Store: reference.Store, Key: reference.Key, Size: reference.Size}},
			InputSlopeJoules: &slope, DispatchOccupancyNanos: slices.Clone(batch.DispatchOccupancyNanos),
		}
		if batch.SaturationInputRate != nil {
			rate := *batch.SaturationInputRate
			out.SaturationInputRate = &rate
		}
		condition.PlannedBatchSizes = append(condition.PlannedBatchSizes, batch.Batch)
		profile.Batches = append(profile.Batches, out)
	}
	profile.Condition = condition
	return profile, nil
}

// Only mutable ownership is copied; immutable samples, distributions and dispatch
// occupancy records stay shared. Mutating those records requires an explicit copy.
func cloneCatalogProfile(profile serving.Profile) serving.Profile {
	profile.Evidence = slices.Clone(profile.Evidence)
	profile.Batches = slices.Clone(profile.Batches)
	for i := range profile.Batches {
		batch := &profile.Batches[i]
		if batch.InputSlopeJoules != nil {
			slope := *batch.InputSlopeJoules
			batch.InputSlopeJoules = &slope
		}
		if batch.SaturationInputRate != nil {
			rate := *batch.SaturationInputRate
			batch.SaturationInputRate = &rate
		}
	}
	if profile.Power != nil {
		power := *profile.Power
		profile.Power = &power
	}
	profile.Condition = profilederivation.CloneCurvePlanSpec(profile.Condition)
	if profile.RuntimeMetadata != nil {
		metadata := *profile.RuntimeMetadata
		metadata.ModelFiles = slices.Clone(metadata.ModelFiles)
		metadata.Inputs = slices.Clone(metadata.Inputs)
		metadata.Outputs = slices.Clone(metadata.Outputs)
		metadata.Providers = slices.Clone(metadata.Providers)
		metadata.AvailableProviders = slices.Clone(metadata.AvailableProviders)
		metadata.CPUAffinity = slices.Clone(metadata.CPUAffinity)
		metadata.InitializedBatches = slices.Clone(metadata.InitializedBatches)
		metadata.RKNN = slices.Clone(metadata.RKNN)
		profile.RuntimeMetadata = &metadata
	}
	profile.RuntimeDelivery = slices.Clone(profile.RuntimeDelivery)
	profile.MetricSource.MetricLabels = maps.Clone(profile.MetricSource.MetricLabels)
	if profile.MetricSource.ThermalMetrics != nil {
		thermal := *profile.MetricSource.ThermalMetrics
		profile.MetricSource.ThermalMetrics = &thermal
	}
	return profile
}

func (c *Catalog) readObject(ctx context.Context, identity evidencestore.ObjectIdentity) ([]byte, error) {
	if err := identity.Location().Validate(); err != nil {
		return nil, err
	}
	if c.maxObjectBytes < 1 || c.maxObjectBytes == 1<<63-1 {
		return nil, errors.New("catalog object read bound required")
	}
	stream, err := c.objects.OpenLocation(ctx, identity.Location())
	if err != nil {
		return nil, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, c.maxObjectBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		slog.Warn("closing stored session result", "key", identity.Key, "reason", closeErr)
	}
	if int64(len(payload)) > c.maxObjectBytes {
		return nil, errors.New("catalog object exceeds configured byte limit")
	}
	return payload, nil
}
