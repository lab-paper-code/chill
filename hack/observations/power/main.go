// Command power integrates whole and active POOL energy from fetched hourly
// observation segments with the ChillServer OperatingPower reducer.
//
//	go run ./hack/observations/power --dir <dir>/obs --settings <file> --state <file> --from <RFC3339> --to <RFC3339>
//	go run ./hack/observations/power --dir <dir>/obs --settings <file> --state <file> --windows <file>
//
// --windows reads a nonempty JSON list of {"id","from","to"} intervals and
// returns {"windows":[{"id":...,"power":...}]} in input order.
//
// The meter set is the accepted settings.meters declaration, scoped by the
// actual state snapshot Node references. Catalog rows resolve recorded bodies.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func main() {
	dir := flag.String("dir", "", "directory of <hour>/<stream>.jsonl.gz segments")
	from := flag.String("from", "", "interval start, RFC3339")
	to := flag.String("to", "", "interval end, RFC3339")
	windowsFile := flag.String("windows", "", "JSON list of id/from/to intervals; excludes --from and --to")
	settings := flag.String("settings", "", "scheduler settings JSON with accepted meters")
	state := flag.String("state", "", "scheduler state JSON with actual Node references")
	nodeNames := flag.String("node-names", "", "optional comma-separated exact Node names")
	maximumGap := flag.Duration("maximum-gap", 0, "explicit policy re-evaluation maximum gap (e.g. 3s); omitted preserves recorded policy")
	flag.Parse()
	if *maximumGap < 0 {
		fmt.Fprintln(os.Stderr, "maximum-gap must be positive")
		os.Exit(1)
	}
	var response any
	var err error
	hasBounds := false
	hasWindows := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "from" || f.Name == "to" {
			hasBounds = true
		}
		if f.Name == "windows" {
			hasWindows = true
		}
	})
	meters, metersErr := derivedMeters(*settings, *state, *nodeNames)
	if hasWindows {
		var windows []powerWindow
		windows, err = readWindows(*windowsFile, hasBounds)
		if err = errors.Join(err, metersErr); err == nil {
			var results []powerWindowResult
			results, err = runWindows(*dir, meters, windows, *maximumGap, eachLine)
			response = struct {
				Windows []powerWindowResult `json:"windows"`
			}{Windows: results}
		}
	} else {
		start, startErr := time.Parse(time.RFC3339Nano, *from)
		end, endErr := time.Parse(time.RFC3339Nano, *to)
		if err = errors.Join(startErr, endErr, metersErr); err == nil {
			var result observation.OperatingPowerWindow
			result, err = run(*dir, meters, start, end, *maximumGap)
			single := struct {
				observation.OperatingPowerWindow
				PolicyOverride map[string]float64 `json:"policyOverride,omitempty"`
			}{OperatingPowerWindow: result}
			if *maximumGap > 0 {
				single.PolicyOverride = map[string]float64{"maximumGapSeconds": maximumGap.Seconds()}
			}
			response = single
		}
	}
	if err == nil {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(response)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// derivedMeters consumes API facts directly; it never synthesizes Kubernetes Nodes.
func derivedMeters(settingsFile, stateFile, nodeNames string) ([]observation.OperatingPowerMeter, error) {
	raw, err := os.ReadFile(settingsFile)
	if err != nil {
		return nil, err
	}
	var settings struct {
		Meters []struct {
			ID     string                        `json:"id"`
			Node   serving.NodeReference         `json:"node"`
			Scope  string                        `json:"scope"`
			Source metricobserver.SourceIdentity `json:"source"`
		} `json:"meters"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return nil, err
	}
	if len(settings.Meters) == 0 {
		return nil, errors.New("settings must contain accepted meters")
	}
	raw, err = os.ReadFile(stateFile)
	if err != nil {
		return nil, err
	}
	var state struct {
		Snapshot struct {
			Nodes []serving.SchedulerNode `json:"nodes"`
		} `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, err
	}
	uids := make(map[string]string, len(state.Snapshot.Nodes))
	for _, n := range state.Snapshot.Nodes {
		if n.Node.Name == "" || n.Node.UID == "" || uids[n.Node.Name] != "" {
			return nil, errors.New("state contains missing or duplicate Node identities")
		}
		uids[n.Node.Name] = n.Node.UID
	}
	var selected map[string]bool
	if nodeNames != "" {
		selected = map[string]bool{}
		for _, name := range strings.Split(nodeNames, ",") {
			if uids[name] == "" || selected[name] {
				return nil, fmt.Errorf("unknown or duplicate selected Node %q", name)
			}
			selected[name] = true
		}
	}
	meters := make([]observation.OperatingPowerMeter, 0, len(settings.Meters))
	for _, meter := range settings.Meters {
		if uid := uids[meter.Node.Name]; uid != "" && (len(selected) == 0 || selected[meter.Node.Name]) {
			if uid != meter.Node.UID || meter.ID == "" || meter.Scope != "node-exclusive-wall" {
				return nil, errors.New("settings meter does not match its actual Node reference or wall scope")
			}
			meters = append(meters, observation.OperatingPowerMeter{
				Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source,
			})
		}
	}
	if len(meters) == 0 {
		return nil, errors.New("no accepted meters cover the selected state Nodes")
	}
	return meters, nil
}

type powerWindow struct {
	ID   string    `json:"id"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

type powerWindowResult struct {
	ID    string                           `json:"id"`
	Power observation.OperatingPowerWindow `json:"power"`
}

func readWindows(name string, hasBounds bool) ([]powerWindow, error) {
	if hasBounds {
		return nil, errors.New("--windows cannot be combined with --from or --to")
	}
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var windows []powerWindow
	if err := decoder.Decode(&windows); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("windows file must contain one JSON list")
	}
	if len(windows) == 0 {
		return nil, errors.New("windows must not be empty")
	}
	ids := make(map[string]bool, len(windows))
	for _, window := range windows {
		if strings.TrimSpace(window.ID) == "" || ids[window.ID] {
			return nil, fmt.Errorf("missing or duplicate window ID %q", window.ID)
		}
		if window.From.IsZero() || !window.From.Before(window.To) {
			return nil, fmt.Errorf("window %q requires a positive explicit interval", window.ID)
		}
		ids[window.ID] = true
	}
	return windows, nil
}

// run reads [from-1m, to+1m] as the server does, so every hour it touches
// must be exported; the collector adds the hour on each side.
func run(dir string, meters []observation.OperatingPowerMeter, from, to time.Time, maximumGap time.Duration) (observation.OperatingPowerWindow, error) {
	results, err := runWindows(dir, meters, []powerWindow{{From: from, To: to}}, maximumGap, eachLine)
	if err != nil {
		return observation.OperatingPowerWindow{}, err
	}
	return results[0].Power, nil
}

// runWindows hydrates the fetched local segments once, preserving the single
// window reader's full membership history and reducer-owned boundary selection.
func runWindows(dir string, meters []observation.OperatingPowerMeter, windows []powerWindow, maximumGap time.Duration, readLines func(string, func([]byte) error) error) ([]powerWindowResult, error) {
	for _, window := range windows {
		for hour := window.From.Add(-time.Minute).UTC().Truncate(time.Hour); !hour.After(window.To.Add(time.Minute)); hour = hour.Add(time.Hour) {
			if _, err := os.Stat(filepath.Join(dir, hour.Format("20060102T15"), "scheduler.jsonl.gz")); err != nil {
				return nil, fmt.Errorf("power boundary hour not fetched: %w", err)
			}
		}
	}
	catalogs, err := filepath.Glob(filepath.Join(dir, "*", "catalog.jsonl.gz"))
	if err != nil {
		return nil, err
	}
	catalog := &observation.ObservationCatalog{}
	for _, name := range catalogs {
		if err := readLines(name, func(raw []byte) error {
			var row observation.CatalogRow
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			catalog.Add(row)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	schedulers, err := filepath.Glob(filepath.Join(dir, "*", "scheduler.jsonl.gz"))
	if err != nil {
		return nil, err
	}
	var decoded []*observation.SchedulerObservation
	for _, name := range schedulers {
		if err := readLines(name, func(raw []byte) error {
			record, err := observation.OperatingPowerRecord(raw, catalog)
			if err != nil {
				return err
			}
			if maximumGap > 0 && record.PowerCoverage != nil {
				policy := *record.PowerCoverage
				policy.MaximumGap = maximumGap
				record.PowerCoverage = &policy
			}
			decoded = append(decoded, record)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	records := func(yield func(*observation.SchedulerObservation) error) error {
		for _, record := range decoded {
			if err := yield(record); err != nil {
				return err
			}
		}
		return nil
	}
	results := make([]powerWindowResult, 0, len(windows))
	for _, window := range windows {
		power, err := observation.ReduceOperatingPower(window.From, window.To, meters, 1<<30, records)
		if err != nil {
			return nil, err
		}
		results = append(results, powerWindowResult{ID: window.ID, Power: power})
	}
	return results, nil
}

func eachLine(name string, visit func([]byte) error) (result error) {
	file, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, file.Close()) }()
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(nil, 1<<30)
	for scanner.Scan() {
		if err := visit(scanner.Bytes()); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return scanner.Err()
}
