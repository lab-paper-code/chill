package kube

import (
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
)

// ProfileSummary is a deliberate read contract, not an embedded internal Profile.
// Raw latency arrays and runtime delivery are never visited by an encoder.
type ProfileSummary struct {
	ID                string                         `json:"id"`
	Reference         evidencestore.ObjectIdentity   `json:"reference"`
	Evidence          []evidencestore.ObjectIdentity `json:"evidence"`
	NodeName          string                         `json:"nodeName"`
	Condition         derivation.CurvePlanSpec       `json:"condition"`
	Batches           []ProfileBatchSummary          `json:"batches"`
	ConfidenceLevel   float64                        `json:"confidenceLevel"`
	TrialsPerBatch    int                            `json:"trialsPerBatch,omitempty"`
	IdleBaselineWatts float64                        `json:"idleBaselineWatts"`
	MeasuredAt        time.Time                      `json:"measuredAt"`
	RuntimeMetadata   *firstfloor.RuntimeMetadata    `json:"runtimeMetadata,omitempty"`
	MetricSource      metricobserver.SourceIdentity  `json:"metricSource"`
	Reuse             *serving.ProfileReuseCheck     `json:"reuse,omitempty"`
}

type ProfileBatchSummary struct {
	Batch                 int     `json:"batch"`
	EnergyPerInputJoules  float64 `json:"energyPerInputJoules"`
	EnergyBelowResolution bool    `json:"energyBelowResolution,omitempty"`
	MemoryBytes           uint64  `json:"memoryBytes"`
	MeanWallWatts         float64 `json:"meanWallWatts,omitempty"`
}

func SummarizeProfile(p serving.Profile) ProfileSummary {
	out := ProfileSummary{ID: p.ID, Reference: p.Reference, Evidence: p.Evidence, NodeName: p.NodeName,
		Condition: p.Condition, ConfidenceLevel: p.ConfidenceLevel, TrialsPerBatch: p.TrialsPerBatch,
		IdleBaselineWatts: p.IdleBaselineWatts, MeasuredAt: p.MeasuredAt, RuntimeMetadata: p.RuntimeMetadata,
		MetricSource: p.MetricSource, Reuse: p.Reuse, Batches: make([]ProfileBatchSummary, 0, len(p.Batches))}
	for _, b := range p.Batches {
		out.Batches = append(
			out.Batches,
			ProfileBatchSummary{
				b.Batch,
				b.EnergyPerInputJoules,
				b.EnergyBelowResolution,
				b.MemoryBytes,
				b.MeanWallWatts,
			},
		)
	}
	return out
}

func (p ProfileSummary) Profile() serving.Profile {
	out := serving.Profile{ID: p.ID, Reference: p.Reference, Evidence: p.Evidence, NodeName: p.NodeName,
		Condition: p.Condition, ConfidenceLevel: p.ConfidenceLevel, TrialsPerBatch: p.TrialsPerBatch,
		IdleBaselineWatts: p.IdleBaselineWatts, MeasuredAt: p.MeasuredAt, RuntimeMetadata: p.RuntimeMetadata,
		MetricSource: p.MetricSource, Reuse: p.Reuse, Batches: make([]serving.BatchProfile, 0, len(p.Batches))}
	for _, b := range p.Batches {
		out.Batches = append(
			out.Batches,
			serving.BatchProfile{
				Batch:                 b.Batch,
				EnergyPerInputJoules:  b.EnergyPerInputJoules,
				EnergyBelowResolution: b.EnergyBelowResolution,
				MemoryBytes:           b.MemoryBytes,
				MeanWallWatts:         b.MeanWallWatts,
			},
		)
	}
	return out
}

type PreparationSummary struct {
	PrepareSpec
	Instance PreparationInstance `json:"instance"`
}
type PreparationInstance struct {
	serving.SchedulerInstance
	Profile *ProfileSummary `json:"profile,omitempty"`
}

func SummarizePreparations(input map[string]PrepareSpec) map[string]PreparationSummary {
	if input == nil {
		return nil
	}
	out := make(map[string]PreparationSummary, len(input))
	for id, p := range input {
		instance := PreparationInstance{SchedulerInstance: p.Instance}
		if p.Instance.Profile != nil {
			summary := SummarizeProfile(*p.Instance.Profile)
			instance.Profile = &summary
		}
		// Drop the internal pointer before even the intermediate projection exists.
		instance.SchedulerInstance.Profile = nil
		p.Instance.Profile = nil
		out[id] = PreparationSummary{PrepareSpec: p, Instance: instance}
	}
	return out
}

// CatalogReportingInput contains the already-derived, bounded numerical facts
// needed by query builders, not raw trials or an internal catalog serialization.
type CatalogReportingInput struct {
	ProfileID       string                                 `json:"profileID"`
	Request         derivation.MeasurementRequestReference `json:"request"`
	ResultReference derivation.RawEvidenceIdentity         `json:"resultReference"`
	Condition       derivation.CurvePlanSpec               `json:"condition"`
	MinimumTrials   int                                    `json:"minimumTrials"`
	Batches         []derivation.MeasurementBatchFacts     `json:"batches"`
}

// CatalogRead carries independent verified reporting and accepted scheduler snapshots.
// Accepted is never merged into current reporting or used to repair missing facts.
type CatalogRead struct {
	CatalogSnapshot
	Accepted *CatalogSnapshot `json:"accepted"`
}

type CatalogSnapshot struct {
	ConfigVersion   string                        `json:"configVersion"`
	Profiles        []ProfileSummary              `json:"profiles"`
	Insufficiencies []CatalogIssue                `json:"insufficiencies"`
	ReuseChecks     []serving.ProfileReuseCheck   `json:"reuseChecks,omitempty"`
	Preparations    map[string]PreparationSummary `json:"preparations"`
	Reporting       []CatalogReportingInput       `json:"reporting"`
	SourceCheckedAt time.Time                     `json:"sourceCheckedAt"`
	SourceError     string                        `json:"sourceError"`
	Revision        string                        `json:"revision"`
}

func ProjectCatalogSnapshot(
	view CatalogView,
	configVersion string,
	preparations map[string]PrepareSpec,
) CatalogSnapshot {
	out := CatalogSnapshot{ConfigVersion: configVersion, Profiles: make([]ProfileSummary, 0, len(view.Profiles)),
		Insufficiencies: view.Insufficiencies, ReuseChecks: view.ReuseChecks, Preparations: SummarizePreparations(preparations),
		Reporting: make([]CatalogReportingInput, 0, len(view.MeasurementFacts)), SourceCheckedAt: view.SourceCheckedAt,
		SourceError: view.SourceError, Revision: view.Revision}
	for _, p := range view.Profiles {
		out.Profiles = append(out.Profiles, SummarizeProfile(p))
	}
	for id, f := range view.MeasurementFacts {
		out.Reporting = append(out.Reporting, CatalogReportingInput{
			ProfileID: id, Request: f.Request, ResultReference: f.ResultReference, Condition: f.Condition,
			MinimumTrials: f.PointPolicy.MinimumTrials, Batches: f.Batches})
	}
	return out
}

// View materializes only query inputs; no scheduler/raw profile can be recovered.
func (c CatalogSnapshot) CatalogView() CatalogView {
	out := CatalogView{}
	out.Insufficiencies, out.ReuseChecks = c.Insufficiencies, c.ReuseChecks
	out.SourceCheckedAt, out.SourceError, out.Revision = c.SourceCheckedAt, c.SourceError, c.Revision
	out.Profiles = make([]serving.Profile, 0, len(c.Profiles))
	for _, p := range c.Profiles {
		out.Profiles = append(out.Profiles, p.Profile())
	}
	out.MeasurementFacts = make(map[string]serving.MeasurementFacts, len(c.Reporting))
	for _, r := range c.Reporting {
		out.MeasurementFacts[r.ProfileID] = serving.MeasurementFacts{
			Request: r.Request, ResultReference: r.ResultReference, Condition: r.Condition,
			PointPolicy: derivation.PointPolicy{MinimumTrials: r.MinimumTrials}, Batches: r.Batches}
	}
	return out
}
