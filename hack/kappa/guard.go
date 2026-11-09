package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

const replayJSONL = "jsonl"

type replayDemand struct {
	UID    string                         `json:"uid"`
	Rate   float64                        `json:"rate"`
	Keep   *serving.FeedbackWorkloadScore `json:"keep,omitempty"`
	Target *serving.FeedbackWorkloadScore `json:"target,omitempty"`
}

type replayResult struct {
	At                 time.Time      `json:"at"`
	Status             string         `json:"status"`
	TargetKey          string         `json:"targetKey"`
	TargetID           string         `json:"targetID,omitempty"`
	ReplayStatus       string         `json:"replayStatus,omitempty"`
	ReplayTargetKey    string         `json:"replayTargetKey,omitempty"`
	Disagreement       bool           `json:"disagreement"`
	PopulationMismatch bool           `json:"populationMismatch"`
	RecordedCounts     [3]int         `json:"recordedCounts"`
	ReplayCounts       [3]int         `json:"replayCounts"`
	Checks             []guardCheck   `json:"checks,omitempty"`
	Demand             []replayDemand `json:"demand"`
	ReplayDemand       []replayDemand `json:"replayDemand,omitempty"`
	ElapsedSeconds     float64        `json:"elapsedSeconds"`
}

type guardCheck struct {
	At       time.Time `json:"at"`
	Offset   int       `json:"offset"`
	Rejected bool      `json:"rejected"`
	Reason   string    `json:"reason,omitempty"`
}

// replayKey also compares integer routes and wait limits: placement alone misses
// recovery final-route changes. Physical IDs are retained; generated plan IDs aren't.
func replayKey(t *serving.SchedulerCandidate) string {
	if t == nil {
		return "-"
	}
	instances := make([]string, 0, len(t.Instances))
	for _, i := range t.Instances {
		instances = append(instances, fmt.Sprintf("%s|%s|%s|%s|b%d|w%d", i.ID, i.Node.Name, i.ProfileID, i.Allocation.GPUUUID, i.MaxBatch, i.MaxWait))
	}
	slices.Sort(instances)
	b, _ := json.Marshal(struct {
		Instances []string
		Routing   map[string]map[string]int32
	}{instances, t.Routing})
	return string(b)
}

func replaySettings(base, recorded serving.SchedulerSettings, margin float64) serving.SchedulerSettings {
	// Preserve all recorded control policy, including minimum demand, exclusions,
	// and headroom; only offline numerical settings come from base.
	recorded.CapacityWindow, recorded.CapacityWindows, recorded.CapacityPassingWindows = base.CapacityWindow, base.CapacityWindows, base.CapacityPassingWindows
	recorded.CapacityMinimumRequests, recorded.CapacityGridSteps = base.CapacityMinimumRequests, base.CapacityGridSteps
	recorded.CapacityBisectionWidth, recorded.CapacityTailSamples = base.CapacityBisectionWidth, base.CapacityTailSamples
	recorded.PlanSearchWorkers = base.PlanSearchWorkers
	if margin >= 0 {
		recorded.CompletionMarginMS = margin
	}
	return recorded
}

// replayDemands reports recorded direct outcomes. Missing evidence stays absent;
// a missing or zero tested-rate witness never becomes a shortage diagnosis.
func replayDemands(decision *serving.SchedulerDecision) []replayDemand {
	out := []replayDemand{}
	if decision.Capacity == nil {
		return out
	}
	for _, workload := range decision.Capacity.Workloads {
		demand := replayDemand{UID: workload.UID, Rate: workload.Rate}
		for j := range decision.Keep.Feedback {
			if score := &decision.Keep.Feedback[j]; score.UID == workload.UID {
				demand.Keep = score
			}
		}
		if decision.Target != nil {
			for j := range decision.Target.Feedback {
				if score := &decision.Target.Feedback[j]; score.UID == workload.UID {
					demand.Target = score
				}
			}
		}
		out = append(out, demand)
	}
	return out
}

type replayTick struct {
	r        record
	snap     serving.SchedulerSnapshot
	settings serving.SchedulerSettings
}

type replayReader struct {
	scanner         *bufio.Scanner
	base            serving.SchedulerSettings
	margin          float64
	profiles        map[string]*serving.Profile
	catalog         *observation.ObservationCatalog
	arrivals        []replayArrival
	restoreArrivals bool
	definitions     map[string]*serving.Definition
}

func hydrateReplayInstances(xs []serving.SchedulerInstance, profiles map[string]*serving.Profile) error {
	for j := range xs {
		if xs[j].Profile == nil {
			xs[j].Profile = profiles[xs[j].ProfileID]
		}
		if xs[j].Profile == nil && xs[j].ProfileID != "" {
			return fmt.Errorf("missing profile %s", xs[j].ProfileID)
		}
	}
	return nil
}

func readReplayCatalog(name string) (*observation.ObservationCatalog, map[string]*serving.Definition, error) {
	definitions := map[string]*serving.Definition{}
	if name == "" {
		return nil, definitions, nil
	}
	catalog := &observation.ObservationCatalog{}
	err := scanReplayFile(name, func(raw []byte) error {
		var row observation.CatalogRow
		if err := json.Unmarshal(raw, &row); err != nil {
			return err
		}
		catalog.Add(row)
		if row.Kind == "definition" {
			var d serving.Definition
			if err := json.Unmarshal(row.Body, &d); err != nil {
				return err
			}
			definitions[row.Key] = &d
		}
		return nil
	})
	return catalog, definitions, err
}

func (reader *replayReader) readTick() (*replayTick, error) {
	for reader.scanner.Scan() {
		var r record
		if err := json.Unmarshal(reader.scanner.Bytes(), &r); err != nil {
			return nil, err
		}
		if r.Decision == nil || len(r.Snapshot) == 0 {
			continue
		}
		snap, err := decodeSnapshot(r.Snapshot)
		if err != nil {
			return nil, err
		}
		if reader.catalog != nil {
			event, err := observation.DecodeSchedulerLine(reader.scanner.Bytes(), reader.catalog)
			if err != nil {
				return nil, err
			}
			snap, r.Decision = *event.Snapshot, event.Decision
		} else if len(snap.WorkloadIDs) > 0 || len(snap.CostRefs) > 0 || len(snap.BaselineRefs) > 0 {
			return nil, fmt.Errorf("snapshot has unresolved catalog references: supply -catalog")
		}
		settings := replaySettings(reader.base, r.Decision.Settings, reader.margin)
		if err := restoreReplayArrivals(&snap, reader.arrivals, settings, reader.restoreArrivals, reader.definitions); err != nil {
			return nil, err
		}
		if !reader.restoreArrivals && snap.Arrivals == nil && r.Decision.Search.ComputeInputs > 0 {
			return nil, fmt.Errorf("snapshot omits request arrivals: supply -arrivals")
		}
		if err := hydrateReplayInstances(snap.Instances, reader.profiles); err != nil {
			return nil, err
		}
		for j := range snap.Options {
			if err := hydrateReplayInstances([]serving.SchedulerInstance{snap.Options[j].Instance}, reader.profiles); err != nil {
				return nil, err
			}
			if snap.Options[j].Instance.Profile == nil {
				snap.Options[j].Instance.Profile = reader.profiles[snap.Options[j].Instance.ProfileID]
			}
		}
		for _, reservation := range snap.Reservations {
			if reservation.Target != nil {
				if err := hydrateReplayInstances(reservation.Target.Instances, reader.profiles); err != nil {
					return nil, err
				}
			}
		}
		if r.Decision.Target != nil {
			if err := hydrateReplayInstances(r.Decision.Target.Instances, reader.profiles); err != nil {
				return nil, err
			}
		}
		return &replayTick{r, snap, settings}, nil
	}
	return nil, reader.scanner.Err()
}

func evaluateReplayTick(queue []*replayTick, next int) (replayResult, error) {
	t := queue[0]
	began := time.Now()
	r := replayResult{At: t.r.At, Status: t.r.Decision.Status, TargetKey: replayKey(t.r.Decision.Target), Demand: replayDemands(t.r.Decision)}
	if target := t.r.Decision.Target; target != nil {
		r.TargetID = target.ID
	}
	if next < 0 {
		d, err := serving.EvaluateScheduler(context.Background(), t.settings, t.snap)
		if err != nil {
			return r, err
		}
		r.ReplayStatus, r.ReplayTargetKey = d.Status, replayKey(d.Target)
		r.RecordedCounts = [3]int{t.r.Decision.Search.Received, t.r.Decision.Search.Accepted, t.r.Decision.Search.ComputeInputs}
		r.ReplayCounts = [3]int{d.Search.Received, d.Search.Accepted, d.Search.ComputeInputs}
		r.PopulationMismatch = r.RecordedCounts != r.ReplayCounts
		r.Disagreement = r.TargetKey != r.ReplayTargetKey
		r.ReplayDemand = replayDemands(&d)
	} else if target := t.r.Decision.Target; target != nil {
		// Revalidate the immutable recorded target through the same current
		// physical admission and exact comparison boundary as production.
		for offset, future := range queue {
			reason, err := serving.SchedulerTargetAdmission(context.Background(), t.settings, future.snap, *target)
			if err != nil {
				return r, err
			}
			r.Checks = append(r.Checks, guardCheck{future.r.At, offset, reason != "", reason})
		}
	}
	r.ElapsedSeconds = time.Since(began).Seconds()
	return r, nil
}

func replay(base serving.SchedulerSettings, profilesFile, recordsFile, catalogFile, arrivalsFile string, margin float64, next int, format string) error {
	if format != replayJSONL && format != "markdown" {
		return fmt.Errorf("unknown format %q", format)
	}
	var profileCatalog struct {
		Profiles []serving.Profile `json:"profiles"`
	}
	data, err := os.ReadFile(profilesFile)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, &profileCatalog); err != nil {
		return err
	}
	profiles := map[string]*serving.Profile{}
	for j := range profileCatalog.Profiles {
		profiles[profileCatalog.Profiles[j].ID] = &profileCatalog.Profiles[j]
	}
	catalog, definitions, err := readReplayCatalog(catalogFile)
	if err != nil {
		return err
	}
	arrivals := []replayArrival{}
	if arrivalsFile != "" {
		if err := scanReplayFile(arrivalsFile, func(raw []byte) error {
			var row replayArrival
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			arrivals = append(arrivals, row)
			return nil
		}); err != nil {
			return err
		}
		slices.SortFunc(arrivals, func(a, b replayArrival) int { return a.At.Compare(b.At) })
	}
	file, err := os.Open(recordsFile)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	var input io.Reader = file
	if strings.HasSuffix(recordsFile, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		input = gz
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1<<20), 1<<30)
	reader := replayReader{
		scanner: scanner, base: base, margin: margin, profiles: profiles,
		catalog: catalog, arrivals: arrivals, restoreArrivals: arrivalsFile != "", definitions: definitions,
	}
	if format == "markdown" {
		fmt.Println("| at | status | target key | offset / snapshot at | guard rejected / reason |\n|---|---|---|---|---|")
	}
	encoder := json.NewEncoder(os.Stdout)
	queue := []*replayTick{}
	eof := false
	for {
		for !eof && len(queue) < max(1, next+1) {
			t, err := reader.readTick()
			if err != nil {
				return err
			}
			if t == nil {
				eof = true
				break
			}
			if len(queue) > 0 && !t.r.At.After(queue[len(queue)-1].r.At) {
				return fmt.Errorf("decision ticks not strictly increasing at %s", t.r.At)
			}
			queue = append(queue, t)
		}
		if len(queue) == 0 {
			break
		}
		r, err := evaluateReplayTick(queue, next)
		if err != nil {
			return err
		}
		if format == replayJSONL {
			if err := encoder.Encode(r); err != nil {
				return err
			}
		} else {
			for _, c := range r.Checks {
				fmt.Printf("| %s | %s | %s | %d / %s | %t %s |\n", r.At.Format(time.RFC3339Nano), r.Status, strings.ReplaceAll(r.TargetKey, "|", "/"), c.Offset, c.At.Format(time.RFC3339Nano), c.Rejected, c.Reason)
			}
		}
		queue[0] = nil
		queue = queue[1:]
	}
	return nil
}

type replayArrival struct {
	serving.SchedulerArrival
	DefinitionID string `json:"definitionID"`
}

func restoreReplayArrivals(snap *serving.SchedulerSnapshot, arrivals []replayArrival, settings serving.SchedulerSettings, restore bool, definitions map[string]*serving.Definition) error {
	for _, d := range snap.Workloads {
		if d != nil {
			definitions[d.Identity()] = d
		}
	}
	for j := range snap.Outstanding {
		r := &snap.Outstanding[j]
		if r.Definition == nil && r.DefinitionID != "" {
			r.Definition = definitions[r.DefinitionID]
			if r.Definition == nil {
				return fmt.Errorf("missing outstanding definition %s", r.DefinitionID)
			}
		}
	}
	if !restore {
		return nil
	}
	start := snap.ObservationEnd.Add(-settings.ObservationWindow)
	first, _ := slices.BinarySearchFunc(arrivals, start, func(a replayArrival, t time.Time) int { return a.At.Compare(t) })
	snap.Arrivals = nil
	for _, a := range arrivals[first:] {
		if !a.At.Before(snap.ObservationEnd) {
			break
		}
		a.Definition = definitions[a.DefinitionID]
		if a.Definition == nil {
			return fmt.Errorf("missing arrival definition %s", a.DefinitionID)
		}
		snap.Arrivals = append(snap.Arrivals, a.SchedulerArrival)
	}
	return nil
}

func scanReplayFile(name string, visit func([]byte) error) error {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	var input io.Reader = file
	if strings.HasSuffix(name, ".gz") {
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer func() { _ = gz.Close() }()
		input = gz
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 1<<20), 1<<30)
	for scanner.Scan() {
		if err := visit(scanner.Bytes()); err != nil {
			return err
		}
	}
	return scanner.Err()
}
