package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

// path mode uses the existing offline planner, then replays only its selected
// target (or keep when no target exists), with an explicit reproducible seed.
//
// go run ./hack/kappa -mode path -scheduler scheduler.json -rows rows.json -arrivals requests.jsonl
//
// Unlike compare/guard, -arrivals here is the actual raw requests stream: one
// serving.RequestRow JSON object per line, not SchedulerArrival or an API page.
// assigned/final rows carry requestID, workloadUID, definitionID, receivedAt and
// the optional direct timing fields; final supersedes assigned. Native rows are
// supplemental physical evidence only. Unknown timing and frozen POST fields
// stay absent, never zero-filled. .jsonl.gz is accepted by the existing scanner.
// Supply cutoff-consistent rows, including assigned-only requests and failures;
// a completed-only export cannot establish received-population SLO compliance.
func pathAssessment(settings serving.SchedulerSettings, snapshot serving.SchedulerSnapshot, rowsFile, requestsFile string) error {
	report, err := pathAssessmentReport(settings, snapshot, rowsFile, requestsFile)
	if err != nil { return err }
	return json.NewEncoder(os.Stdout).Encode(report)
}

type pathReport struct {
	DecisionStatus string `json:"decisionStatus"`
	DecisionReasons []string `json:"decisionReasons,omitempty"`
	serving.PathAssessment
}

func pathAssessmentReport(settings serving.SchedulerSettings, snapshot serving.SchedulerSnapshot, rowsFile, requestsFile string) (pathReport, error) {
	rows, err := readRows(rowsFile)
	if err != nil { return pathReport{}, err }
	requests, err := readPathRequests(requestsFile)
	if err != nil { return pathReport{}, err }
	// Decode-time service distributions are transient. Hydrate the same native
	// model used by live profiles once, without copying profile/raw populations.
	seen := map[*serving.Profile]bool{}
	hydrate := func(instances []serving.SchedulerInstance) error {
		for _, i := range instances {
			p := i.Profile
			if p == nil || seen[p] { continue }
			seen[p] = true
			for j := range p.Batches {
				b := &p.Batches[j]
				if b.ServiceDistribution != nil { continue }
				b.ServiceDistribution, err = firstfloor.NewServiceDistribution(b.ServiceSamples)
				if err != nil { return fmt.Errorf("profile %s batch %d: %w", p.ID, b.Batch, err) }
			}
		}
		return nil
	}
	if err := hydrate(snapshot.Instances); err != nil { return pathReport{}, err }
	for _, o := range snapshot.Options {
		if err := hydrate([]serving.SchedulerInstance{o.Instance}); err != nil { return pathReport{}, err }
	}
	for _, r := range snapshot.Reservations {
		if r.Target != nil {
			if err := hydrate(r.Target.Instances); err != nil { return pathReport{}, err }
		}
	}
	definitions := map[string]*serving.Definition{}
	if err := restoreReplayArrivals(&snapshot, nil, settings, false, definitions); err != nil { return pathReport{}, err }
	// Reuse the raw row's acceptance/compute contract. Unknown definitions stay
	// unmatched observations and are not invented as synthetic compute inputs.
	snapshot.Arrivals = []serving.SchedulerArrival{}
	for _, r := range requests {
		if r.Kind == serving.RequestRowNative { continue }
		d := definitions[r.DefinitionID]
		if d != nil && d.UID != r.WorkloadUID { d = nil }
		snapshot.Arrivals = append(snapshot.Arrivals, serving.SchedulerArrival{
			ID: r.RequestID, At: r.ReceivedAt, WorkloadUID: r.WorkloadUID, Definition: d,
			Lifetime: time.Duration(r.LifetimeNanos), Accepted: r.Accepted(), Compute: r.ValidatedNanos != nil || r.Accepted(), Outcome: r.Outcome,
		})
	}
	ctx := context.Background()
	decision, err := serving.EvaluateScheduler(ctx, settings, snapshot, rows)
	if err != nil { return pathReport{}, err }
	candidate := decision.Keep
	if decision.Target != nil { candidate = *decision.Target }
	const seed uint64 = 1
	assessment, err := serving.AssessPath(ctx, settings, snapshot, candidate, seed, requests)
	return pathReport{DecisionStatus: decision.Status, DecisionReasons: decision.Reasons, PathAssessment: assessment}, err
}

// Keep final-over-assigned precedence identical to the observation reducer.
// Native supplements cannot replace the HTTP row, even when they arrive later.
func readPathRequests(name string) ([]*serving.RequestRow, error) {
	byID := map[string]*serving.RequestRow{}
	native := []*serving.RequestRow{}
	err := scanReplayFile(name, func(raw []byte) error {
		row := &serving.RequestRow{}
		if err := json.Unmarshal(raw, row); err != nil { return err }
		if row.RequestID == "" { return fmt.Errorf("raw RequestRow requires requestID") }
		switch row.Kind {
		case serving.RequestRowNative:
			native = append(native, row)
		case serving.RequestRowAssigned, serving.RequestRowFinal:
			if row.ReceivedAt.IsZero() { return fmt.Errorf("raw RequestRow %s requires receivedAt", row.RequestID) }
			if row.Kind == serving.RequestRowFinal || byID[row.RequestID] == nil { byID[row.RequestID] = row }
		default:
			return fmt.Errorf("raw RequestRow %s has unsupported kind %q; use requests JSONL, not SchedulerArrival JSONL", row.RequestID, row.Kind)
		}
		return nil
	})
	if err != nil { return nil, err }
	ids := make([]string, 0, len(byID))
	for id := range byID { ids = append(ids, id) }
	slices.Sort(ids)
	rows := make([]*serving.RequestRow, 0, len(ids)+len(native))
	for _, id := range ids { rows = append(rows, byID[id]) }
	return append(rows, native...), nil
}
