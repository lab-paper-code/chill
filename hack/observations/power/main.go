// Command power integrates whole and active POOL energy from fetched hourly
// observation segments with the ChillServer OperatingPower reducer.
//
//	go run ./hack/observations/power --dir <dir>/obs --metric-sources <file> --nodes <file> --from <RFC3339> --to <RFC3339>
//
// The meter set is the one ChillServer derives: one wall meter per Profiler
// metric source of the run (the facts ConfigMap metric-sources value) whose
// Node exists in --nodes (kubectl get nodes -o json). Catalog rows only
// resolve the recorded meter bodies.
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
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/serving"
	corev1 "k8s.io/api/core/v1"
)

func main() {
	dir := flag.String("dir", "", "directory of <hour>/<stream>.jsonl.gz segments")
	from := flag.String("from", "", "interval start, RFC3339")
	to := flag.String("to", "", "interval end, RFC3339")
	sources := flag.String("metric-sources", "", "Profiler metric-sources YAML of the run")
	nodes := flag.String("nodes", "", "Node list JSON of the run (kubectl get nodes -o json)")
	flag.Parse()
	start, err := time.Parse(time.RFC3339Nano, *from)
	end, endErr := time.Parse(time.RFC3339Nano, *to)
	meters, metersErr := derivedMeters(*sources, *nodes)
	if err = errors.Join(err, endErr, metersErr); err == nil {
		var result serving.OperatingPowerWindow
		if result, err = run(*dir, meters, start, end); err == nil {
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")
			err = encoder.Encode(result)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// derivedMeters builds the wall meters ChillServer derives from the Profiler
// metric sources and the live Node identities.
func derivedMeters(sourcesFile, nodesFile string) ([]serving.OperatingPowerMeter, error) {
	raw, err := os.ReadFile(sourcesFile)
	if err != nil {
		return nil, err
	}
	sources, err := profilerkube.DecodeMetricSources(raw)
	if err != nil {
		return nil, err
	}
	raw, err = os.ReadFile(nodesFile)
	if err != nil {
		return nil, err
	}
	var nodes corev1.NodeList
	if err := json.Unmarshal(raw, &nodes); err != nil {
		return nil, err
	}
	uids := map[string]string{}
	for _, n := range nodes.Items {
		uids[n.Name] = string(n.UID)
	}
	var meters []serving.OperatingPowerMeter
	for _, s := range sources {
		if uid := uids[s.Node]; uid != "" {
			meters = append(meters, serving.OperatingPowerMeter{
				Node: serving.NodeReference{Name: s.Node, UID: uid}, Meter: s.MeterID, Scope: "node-exclusive-wall",
				Source: metricobserver.SourceIdentity{
					Kind: s.Kind, NodeName: s.Node, Endpoint: s.Endpoint, Metric: s.PowerMetric, MetricLabels: s.MetricLabels,
				},
			})
		}
	}
	return meters, nil
}

// run reads [from-1m, to+1m] as the server does, so every hour it touches
// must be fetched; fetch.sh adds the hour on each side.
func run(dir string, meters []serving.OperatingPowerMeter, from, to time.Time) (serving.OperatingPowerWindow, error) {
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
