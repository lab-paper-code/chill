// Command power integrates whole and active POOL energy from fetched hourly
// observation segments with the ChillServer OperatingPower reducer.
//
//	go run ./hack/observations/power --dir <dir>/obs --scheduler-config <file> --from <RFC3339> --to <RFC3339>
//
// The scheduler configuration is the accepted ChillServer configuration of
// the run (the scheduler ConfigMap value); its meters are the meter set the
// server integrates. Catalog rows only resolve the recorded meter bodies.
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

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
)

func main() {
	dir := flag.String("dir", "", "directory of <hour>/<stream>.jsonl.gz segments")
	from := flag.String("from", "", "interval start, RFC3339")
	to := flag.String("to", "", "interval end, RFC3339")
	config := flag.String("scheduler-config", "", "accepted scheduler configuration JSON of the run")
	flag.Parse()
	start, err := time.Parse(time.RFC3339Nano, *from)
	end, endErr := time.Parse(time.RFC3339Nano, *to)
	meters, metersErr := configuredMeters(*config)
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

// configuredMeters reads the meter declarations of a scheduler configuration.
func configuredMeters(name string) ([]serving.OperatingPowerMeter, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	var config struct {
		Meters []servingkube.MeterDeclaration `json:"meters"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	meters := make([]serving.OperatingPowerMeter, len(config.Meters))
	for index, meter := range config.Meters {
		meters[index] = serving.OperatingPowerMeter{
			Node: meter.Node, Meter: meter.ID, Scope: meter.Scope, Source: meter.Source,
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
