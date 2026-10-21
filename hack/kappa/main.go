// Command kappa runs the server capacity table and the server planner
// (serving.EvaluateScheduler) offline.
//
//	go run ./hack/kappa -mode table   -scheduler scheduler-pre.json [-margin 139] [-workloads image-classification-tight-slo] [-rows rows.json]
//	go run ./hack/kappa -mode tick    -scheduler scheduler-pre.json -rows rows.json [-margin 139] [-workloads ...] [-rate 40] [-repeat 200]
//	go run ./hack/kappa -mode compare -scheduler scheduler-pre.json -profiles profiles-pre.json -records records.jsonl -rows rows.json [-margin 139]
//
// table builds every row the named autonomous Workloads need, the way
// ChillServer does on start, and writes them to -rows. tick times the planner
// on the dump's snapshot, optionally with Poisson arrivals at -rate req/s in
// its observation window. compare replans each recorded decision with the
// rows and reports agreement with the recorded target and the planner time.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"gonum.org/v1/gonum/stat"
)

func main() {
	mode := flag.String("mode", "table", "table, tick or compare")
	schedulerFile := flag.String("scheduler", "", "ChillServer /api/v1/scheduler dump; options carry profile bodies")
	profilesFile := flag.String("profiles", "", "ChillServer /api/v1/scheduler/profiles dump (compare)")
	recordsFile := flag.String("records", "", "scheduler record JSONL with decision rows (compare)")
	rowsFile := flag.String("rows", "", "capacity rows JSON: table writes it, tick and compare read it")
	margin := flag.Float64("margin", -1, "completion margin ms; negative uses the settings of the input")
	window := flag.Float64("window", 60, "capacityWindow seconds")
	windows := flag.Int("windows", 20, "capacityWindows")
	passing := flag.Int("passing", 19, "capacityPassingWindows")
	workloads := flag.String("workloads", "", "comma-separated Workload names under autonomous control (default all)")
	rate := flag.Float64("rate", 0, "tick: Poisson arrivals (req/s) in the observation window; 0 keeps the dump's")
	repeat := flag.Int("repeat", 200, "tick: planner runs")
	flag.Parse()
	snapshot, settings, err := loadScheduler(*schedulerFile)
	if err == nil {
		if *margin >= 0 {
			settings.CompletionMarginMS = *margin
		}
		settings.CapacityWindow = time.Duration(*window * 1e9)
		settings.CapacityWindows, settings.CapacityPassingWindows, settings.CapacityMinimumRequests = *windows, *passing, 1000
		settings.AutonomousWorkloads = nil
		for _, w := range snapshot.Workloads {
			if *workloads == "" || slices.Contains(strings.Split(*workloads, ","), w.Name) {
				settings.AutonomousWorkloads = append(settings.AutonomousWorkloads, w.UID)
			}
		}
		switch *mode {
		case "table":
			err = table(settings, snapshot, *rowsFile)
		case "tick":
			err = tick(settings, snapshot, *rowsFile, *rate, *repeat)
		case "compare":
			err = compare(settings, *profilesFile, *recordsFile, *rowsFile, *margin)
		default:
			err = fmt.Errorf("unknown mode %q", *mode)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func loadScheduler(name string) (serving.SchedulerSnapshot, serving.SchedulerSettings, error) {
	var dump struct {
		Settings serving.SchedulerSettings `json:"settings"`
		State    struct {
			Snapshot json.RawMessage `json:"snapshot"`
		} `json:"state"`
	}
	data, err := os.ReadFile(name)
	if err == nil {
		err = json.Unmarshal(data, &dump)
	}
	if err != nil {
		return serving.SchedulerSnapshot{}, dump.Settings, err
	}
	snapshot, err := decodeSnapshot(dump.State.Snapshot)
	return snapshot, dump.Settings, err
}

// decodeSnapshot reads a snapshot; the legacy instance field workloadUIDs of
// records taken before the single-Workload model fills workloadUID.
func decodeSnapshot(raw json.RawMessage) (serving.SchedulerSnapshot, error) {
	var snapshot serving.SchedulerSnapshot
	var legacy struct {
		Instances []struct {
			WorkloadUIDs []string `json:"workloadUIDs"`
		} `json:"instances"`
	}
	if err := errors.Join(json.Unmarshal(raw, &snapshot), json.Unmarshal(raw, &legacy)); err != nil {
		return snapshot, err
	}
	for i, instance := range legacy.Instances {
		if len(instance.WorkloadUIDs) > 0 && snapshot.Instances[i].WorkloadUID == "" {
			snapshot.Instances[i].WorkloadUID = instance.WorkloadUIDs[0]
		}
	}
	return snapshot, nil
}

// table builds the server capacity table for the autonomous Workloads, prints
// every row with its build time and writes the rows.
func table(settings serving.SchedulerSettings, snapshot serving.SchedulerSnapshot, rowsFile string) error {
	began := time.Now()
	t := serving.NewCapacityTable(runtime.GOMAXPROCS(0), nil, nil)
	t.Ensure(context.Background(), settings, snapshot)
	t.Wait()
	elapsed := time.Since(began)
	names := map[string]string{}
	for _, o := range snapshot.Options {
		if o.Instance.Profile != nil {
			names[o.Instance.Profile.ID] = o.Instance.Profile.Condition.Name
		}
	}
	fmt.Printf("completion margin %g ms, GOMAXPROCS %d\n\n| workload | option | b | mu (req/s) | kappa | status | s |\n|---|---|---|---|---|---|---|\n", settings.CompletionMarginMS, runtime.GOMAXPROCS(0))
	nonMonotone := 0
	rows := t.Rows()
	for _, row := range rows {
		kappa := "-"
		if row.Kappa != nil {
			kappa = fmt.Sprintf("%.1f", *row.Kappa)
		}
		if row.Reason == serving.CapacityNonMonotone {
			nonMonotone++
		}
		fmt.Printf("| %s | %s | %d | %.1f | %s | %s %s | %.1f |\n", row.Input.WorkloadUID, names[row.Input.ProfileID], row.Input.MaxBatch, row.Mu, kappa, row.Status, row.Reason, row.Elapsed.Seconds())
	}
	fmt.Printf("\n%d rows, %d NonMonotone, table %.1f s\n", len(rows), nonMonotone, elapsed.Seconds())
	if rowsFile == "" {
		return nil
	}
	data, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	return os.WriteFile(rowsFile, data, 0o644)
}

func readRows(name string) ([]serving.CapacityRow, error) {
	var rows []serving.CapacityRow
	data, err := os.ReadFile(name)
	if err == nil {
		err = json.Unmarshal(data, &rows)
	}
	return rows, err
}

// tick times the planner on one snapshot and prints its choice.
func tick(settings serving.SchedulerSettings, snapshot serving.SchedulerSnapshot, rowsFile string, rate float64, repeat int) error {
	rows, err := readRows(rowsFile)
	if err != nil {
		return err
	}
	if rate > 0 {
		snapshot.Arrivals = poisson(snapshot, settings, rate)
	}
	times := []float64{}
	var decision serving.SchedulerDecision
	for range repeat {
		began := time.Now()
		if decision, err = serving.EvaluateScheduler(context.Background(), settings, snapshot, rows); err != nil {
			return err
		}
		times = append(times, time.Since(began).Seconds()*1e3)
	}
	fmt.Printf("GOMAXPROCS %d, %d runs, rate %g req/s: tick ms p50 %.3f p99 %.3f max %.3f\n", runtime.GOMAXPROCS(0), repeat, rate, quantile(times, 0.5), quantile(times, 0.99), slices.Max(times))
	plans := 0
	if decision.Capacity != nil {
		plans = len(decision.Capacity.Plans)
		fmt.Printf("T %.1f s, r %.1f s, cells %d\n", decision.Capacity.Period.Seconds(), decision.Capacity.Route.Seconds(), decision.Capacity.Cells)
	}
	fmt.Printf("status %s %v, node plans %d, compared Nodes %d, target %s\n", decision.Status, decision.Reasons, plans, len(decision.Search.ComparedNodes), targetKey(decision.Target))
	return nil
}

// poisson replaces the arrivals with Poisson arrivals of the first autonomous
// Workload over the observation window.
func poisson(snapshot serving.SchedulerSnapshot, settings serving.SchedulerSettings, rate float64) []serving.SchedulerArrival {
	var w *serving.Definition
	for _, d := range snapshot.Workloads {
		if slices.Contains(settings.AutonomousWorkloads, d.UID) && w == nil {
			w = d
		}
	}
	rng := rand.New(rand.NewPCG(1, 1))
	start := snapshot.ObservationEnd.Add(-settings.ObservationWindow)
	arrivals := []serving.SchedulerArrival{}
	for t := rng.ExpFloat64() / rate; t < settings.ObservationWindow.Seconds(); t += rng.ExpFloat64() / rate {
		arrivals = append(arrivals, serving.SchedulerArrival{ID: fmt.Sprintf("a%07d", len(arrivals)), At: start.Add(time.Duration(t * 1e9)), Definition: w, Accepted: true, Compute: true, WorkloadUID: w.UID})
	}
	return arrivals
}

func quantile(xs []float64, p float64) float64 {
	sorted := slices.Clone(xs)
	sort.Float64s(sorted)
	return stat.Quantile(p, stat.Empirical, sorted, nil)
}

// targetKey names a target by Node, profile and batch limit of each instance.
func targetKey(target *serving.SchedulerCandidate) string {
	if target == nil {
		return "-"
	}
	keys := []string{}
	for _, i := range target.Instances {
		profile := i.ProfileID
		if i.Profile != nil {
			profile = i.Profile.ID
		}
		keys = append(keys, fmt.Sprintf("%s|%s|%v|b%d", i.Node.Name, profile, i.Allocation.CPUs, i.MaxBatch))
	}
	sort.Strings(keys)
	return strings.Join(keys, " + ")
}

type record struct {
	At       time.Time                  `json:"at"`
	Decision *serving.SchedulerDecision `json:"decision"`
	Snapshot json.RawMessage            `json:"snapshot"`
}

// compare replans every recorded decision with the server planner. The rows
// carry the capacity settings, so each record runs with base's capacity and
// margin settings and the record's own control settings.
func compare(base serving.SchedulerSettings, profilesFile, recordsFile, rowsFile string, margin float64) error {
	rows, err := readRows(rowsFile)
	if err != nil {
		return err
	}
	var catalog struct {
		Profiles []serving.Profile `json:"profiles"`
	}
	data, err := os.ReadFile(profilesFile)
	if err == nil {
		err = json.Unmarshal(data, &catalog)
	}
	file, openErr := os.Open(recordsFile)
	if err = errors.Join(err, openErr); err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	profiles := map[string]*serving.Profile{}
	for j := range catalog.Profiles {
		profiles[catalog.Profiles[j].ID] = &catalog.Profiles[j]
	}
	hydrate := func(instances []serving.SchedulerInstance) {
		for j := range instances {
			if instances[j].Profile == nil {
				instances[j].Profile = profiles[instances[j].ProfileID]
			}
		}
	}
	decisions, targets, agree := 0, 0, 0
	statuses := map[string]int{}
	times := []float64{}
	disagreements := []string{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 1<<20), 1<<30)
	for scanner.Scan() {
		var r record
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil {
			return err
		}
		if r.Decision == nil || len(r.Snapshot) == 0 {
			continue
		}
		s, err := decodeSnapshot(r.Snapshot)
		if err != nil {
			return err
		}
		hydrate(s.Instances)
		for j := range s.Options {
			s.Options[j].Instance.Profile = profiles[s.Options[j].Instance.ProfileID]
		}
		if s.InProgressTarget != nil {
			hydrate(s.InProgressTarget.Instances)
		}
		settings := base
		recorded := r.Decision.Settings
		settings.ObservationWindow, settings.ControlInterval, settings.ComparisonPeriod, settings.MaxObservationAge = recorded.ObservationWindow, recorded.ControlInterval, recorded.ComparisonPeriod, recorded.MaxObservationAge
		settings.MaxReplicas, settings.MaxInstances, settings.MaxCPUs, settings.Controls = recorded.MaxReplicas, recorded.MaxInstances, recorded.MaxCPUs, recorded.Controls
		settings.AutonomousWorkloads = recorded.AutonomousWorkloads
		if margin < 0 {
			settings.CompletionMarginMS = recorded.CompletionMarginMS
		}
		began := time.Now()
		d, err := serving.EvaluateScheduler(context.Background(), settings, s, rows)
		if err != nil {
			return err
		}
		times = append(times, time.Since(began).Seconds()*1e3)
		decisions++
		statuses[d.Status]++
		if r.Decision.Target == nil {
			continue
		}
		targets++
		hydrate(r.Decision.Target.Instances)
		if want, got := targetKey(r.Decision.Target), targetKey(d.Target); want == got {
			agree++
		} else {
			disagreements = append(disagreements, fmt.Sprintf("| %s | %s | %s | %s | %s |", r.At.Format("15:04:05"), r.Decision.Status, want, d.Status, got))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	fmt.Printf("decisions %d, with recorded target %d, same target %d, statuses %v\n", decisions, targets, agree, statuses)
	if len(times) > 0 {
		fmt.Printf("GOMAXPROCS %d: tick ms p50 %.3f p99 %.3f max %.3f\n", runtime.GOMAXPROCS(0), quantile(times, 0.5), quantile(times, 0.99), slices.Max(times))
	}
	if len(disagreements) > 0 {
		fmt.Printf("\n| at | recorded status | recorded target | status | target |\n|---|---|---|---|---|\n%s\n", strings.Join(disagreements, "\n"))
	}
	return nil
}
