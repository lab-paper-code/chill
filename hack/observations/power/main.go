// Command power integrates whole and active POOL energy from fetched hourly
// observation segments with the ChillServer OperatingPower reducer.
//
//	go run ./hack/observations/power --dir <dir>/obs --settings <file> --state <file> --from <RFC3339> --to <RFC3339>
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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/serving"
)

func main() {
	dir := flag.String("dir", "", "directory of <hour>/<stream>.jsonl.gz segments")
	from := flag.String("from", "", "interval start, RFC3339")
	to := flag.String("to", "", "interval end, RFC3339")
	settings := flag.String("settings", "", "scheduler settings JSON with accepted meters")
	state := flag.String("state", "", "scheduler state JSON with actual Node references")
	nodeNames := flag.String("node-names", "", "optional comma-separated exact Node names")
	maximumGap := flag.Duration("maximum-gap", 0, "explicit policy re-evaluation maximum gap (e.g. 3s); omitted preserves recorded policy")
	flag.Parse()
	if *maximumGap < 0 {
		fmt.Fprintln(os.Stderr, "maximum-gap must be positive")
		os.Exit(1)
	}
	start, err := time.Parse(time.RFC3339Nano, *from)
	end, endErr := time.Parse(time.RFC3339Nano, *to)
	meters, metersErr := derivedMeters(*settings, *state, *nodeNames)
	if err = errors.Join(err, endErr, metersErr); err == nil {
		var result serving.OperatingPowerWindow
		if result, err = run(*dir, meters, start, end, *maximumGap); err == nil {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			response := struct {
				serving.OperatingPowerWindow
				PolicyOverride map[string]float64 `json:"policyOverride,omitempty"`
			}{OperatingPowerWindow: result}
			if *maximumGap > 0 {
				response.PolicyOverride = map[string]float64{"maximumGapSeconds": maximumGap.Seconds()}
			}
			err = encoder.Encode(response)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// derivedMeters consumes API facts directly; it never synthesizes Kubernetes Nodes.
func derivedMeters(settingsFile, stateFile, nodeNames string) ([]serving.OperatingPowerMeter, error) {
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
	meters := make([]serving.OperatingPowerMeter, 0, len(settings.Meters))
	for _, meter := range settings.Meters {
		if uid := uids[meter.Node.Name]; uid != "" && (len(selected) == 0 || selected[meter.Node.Name]) {
			if uid != meter.Node.UID || meter.ID == "" || meter.Scope != "node-exclusive-wall" {
				return nil, errors.New("settings meter does not match its actual Node reference or wall scope")
			}
			meters = append(meters, serving.OperatingPowerMeter{
				Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source,
			})
		}
	}
	if len(meters) == 0 {
		return nil, errors.New("no accepted meters cover the selected state Nodes")
	}
	return meters, nil
}

// run reads [from-1m, to+1m] as the server does, so every hour it touches
// must be exported; the collector adds the hour on each side.
func run(dir string, meters []serving.OperatingPowerMeter, from, to time.Time, maximumGap time.Duration) (serving.OperatingPowerWindow, error) {
	last := to.Add(time.Minute)
	for hour := from.Add(-time.Minute).UTC().Truncate(time.Hour); !hour.After(last); hour = hour.Add(time.Hour) {
		if _, err := os.Stat(filepath.Join(dir, hour.Format("20060102T15"), "scheduler.jsonl.gz")); err != nil {
			return serving.OperatingPowerWindow{}, fmt.Errorf("power boundary hour not fetched: %w", err)
		}
	}
	catalogs, err := filepath.Glob(filepath.Join(dir, "*", "catalog.jsonl.gz"))
	if err != nil {
		return serving.OperatingPowerWindow{}, err
	}
	catalog := &serving.ObservationCatalog{}
	for _, name := range catalogs {
		if err := eachLine(name, func(raw []byte) error {
			var row serving.CatalogRow
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			catalog.Add(row)
			return nil
		}); err != nil {
			return serving.OperatingPowerWindow{}, err
		}
	}
	schedulers, err := filepath.Glob(filepath.Join(dir, "*", "scheduler.jsonl.gz"))
	if err != nil {
		return serving.OperatingPowerWindow{}, err
	}
	records := func(yield func(*serving.SchedulerObservation) error) error {
		for _, name := range schedulers {
			if err := eachLine(name, func(raw []byte) error {
				record, err := serving.OperatingPowerRecord(raw, catalog)
				if err != nil {
					return err
				}
				if maximumGap > 0 && record.PowerCoverage != nil {
					policy := *record.PowerCoverage
					policy.MaximumGap = maximumGap
					record.PowerCoverage = &policy
				}
				return yield(record)
			}); err != nil {
				return err
			}
		}
		return nil
	}
	return serving.ReduceOperatingPower(from, to, meters, 1<<30, records)
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
