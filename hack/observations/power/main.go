// Command power integrates whole and active POOL energy from fetched hourly
// observation segments with the ChillServer OperatingPower reducer.
//
//	go run ./hack/observations/power --dir <dir>/obs --from <RFC3339> --to <RFC3339>
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
	"sort"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

func main() {
	dir := flag.String("dir", "", "directory of <hour>/<stream>.jsonl.gz segments")
	from := flag.String("from", "", "interval start, RFC3339")
	to := flag.String("to", "", "interval end, RFC3339")
	flag.Parse()
	start, err := time.Parse(time.RFC3339Nano, *from)
	end, endErr := time.Parse(time.RFC3339Nano, *to)
	if err = errors.Join(err, endErr); err == nil {
		var result serving.OperatingPowerWindow
		if result, err = run(*dir, start, end); err == nil {
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

func run(dir string, from, to time.Time) (serving.OperatingPowerWindow, error) {
	catalogs, err := filepath.Glob(filepath.Join(dir, "*", "catalog.jsonl.gz"))
	if err != nil {
		return serving.OperatingPowerWindow{}, err
	}
	catalog := &serving.ObservationCatalog{}
	meters := map[string]serving.CatalogRow{}
	for _, name := range catalogs {
		if err := eachLine(name, func(raw []byte) error {
			var row serving.CatalogRow
			if err := json.Unmarshal(raw, &row); err != nil {
				return err
			}
			catalog.Add(row)
			previous, ok := meters[row.Key]
			if row.Kind == "meter" && (!ok || !row.At.Before(previous.At)) {
				meters[row.Key] = row
			}
			return nil
		}); err != nil {
			return serving.OperatingPowerWindow{}, err
		}
	}
	// The latest catalog body of each meter is its configured identity.
	configured := make([]serving.OperatingPowerMeter, 0, len(meters))
	for key, row := range meters {
		var body serving.ObservationMeter
		if err := json.Unmarshal(row.Body, &body); err != nil {
			return serving.OperatingPowerWindow{}, err
		}
		configured = append(configured, serving.OperatingPowerMeter{
			Node: body.Node, Meter: key, Scope: body.Scope, Source: body.Source,
		})
	}
	sort.Slice(configured, func(a, b int) bool { return configured[a].Meter < configured[b].Meter })
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
	return serving.ReduceOperatingPower(from, to, configured, 1<<30, records)
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
