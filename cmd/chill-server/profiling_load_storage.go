package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

var errProfilingResultBudget = errors.New("native profile result budget exceeded")

// Indexes retain only ordering and disk locations, never native output payloads.
type profilingJSONEntry struct {
	offset, length int64
	at             time.Time
	id             string
	batch          uint64
}

type profilingJSONArray struct {
	file    *os.File
	entries []profilingJSONEntry
	bytes   int64
}

func (a *profilingJSONArray) append(value any, entry profilingJSONEntry) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	entry.offset, entry.length = a.bytes, int64(len(raw))
	if _, err := a.file.Write(raw); err != nil {
		return err
	}
	a.bytes += int64(len(raw))
	a.entries = append(a.entries, entry)
	return nil
}

func (a *profilingJSONArray) contentBytes() int64 {
	return a.bytes + int64(max(0, len(a.entries)-1))
}

func (a *profilingJSONArray) write(w io.Writer) error {
	if _, err := io.WriteString(w, "["); err != nil {
		return err
	}
	buffer := make([]byte, os.Getpagesize())
	source := io.NewSectionReader(a.file, 0, a.bytes)
	row := io.LimitedReader{R: source}
	target := struct{ io.Writer }{w}
	for index, entry := range a.entries {
		if index > 0 {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		if _, err := source.Seek(entry.offset, io.SeekStart); err != nil {
			return err
		}
		row.N = entry.length
		if _, err := io.CopyBuffer(target, &row, buffer); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "]")
	return err
}

type profilingPhaseStore struct {
	requests, samples, latencies profilingJSONArray
}

func (s *profilingPhaseStore) close() error {
	var err error
	for _, array := range []*profilingJSONArray{&s.requests, &s.samples, &s.latencies} {
		if array.file != nil {
			err = errors.Join(err, array.file.Close())
		}
	}
	return err
}

func (d *profilingLoadDriver) openPhaseStore(id string) (*profilingPhaseStore, error) {
	if d.evidenceDir == "" {
		var err error
		d.evidenceDir, err = os.MkdirTemp(d.dataDir, ".profiling-evidence-")
		if err != nil {
			return nil, err
		}
	}
	store := new(profilingPhaseStore)
	for _, array := range []*profilingJSONArray{&store.requests, &store.samples, &store.latencies} {
		var err error
		array.file, err = os.CreateTemp(d.evidenceDir, "rows-")
		if err != nil {
			_ = store.close()
			return nil, err
		}
	}
	if d.stores == nil {
		d.stores = make(map[string]*profilingPhaseStore)
	}
	d.stores[id] = store
	return store, nil
}

func (d *profilingLoadDriver) updatePhase(phase profilingrun.Phase) error {
	found := false
	for index := range d.result.Phases {
		if d.result.Phases[index].ID == phase.ID {
			d.result.Phases[index], found = phase, true
			break
		}
	}
	if !found {
		d.result.Phases = append(d.result.Phases, phase)
	}
	d.metadataValid = false
	return d.checkResultBudget()
}

// The exact current JSON size includes every retained row, comma and envelope.
// There is no projection of future Poisson arrivals or guessed per-phase padding.
func (d *profilingLoadDriver) resultBytes() (int64, error) {
	if !d.metadataValid {
		raw, err := json.Marshal(d.result)
		if err != nil {
			return 0, err
		}
		// Three nil slices per phase become three streamed JSON arrays.
		d.metadataBytes = int64(len(raw)) - int64(len(d.result.Phases))*3*int64(len("null")-len("[]"))
		d.metadataValid = true
	}
	size := d.metadataBytes
	for _, phase := range d.result.Phases {
		store := d.stores[phase.ID]
		if store == nil {
			return 0, errors.New("phase has no evidence store")
		}
		size += store.requests.contentBytes() + store.samples.contentBytes() + store.latencies.contentBytes()
	}
	return size, nil
}

func (d *profilingLoadDriver) checkResultBudget() error {
	size, err := d.resultBytes()
	if err != nil {
		return err
	}
	if err := d.reserveResultBudget(size - d.resultBudgetBytes); err != nil {
		return err
	}
	d.resultBudgetBytes = size
	return nil
}

// Collection and the independent FIFO producer share the frozen byte limit.
func (d *profilingLoadDriver) reserveResultBudget(bytes int64) error {
	for {
		used := d.budgetBytes.Load()
		if bytes > d.plan.MaxResultBytes-used {
			return fmt.Errorf("%w: %d bytes exceeds frozen limit %d",
				errProfilingResultBudget, used+bytes, d.plan.MaxResultBytes)
		}
		if d.budgetBytes.CompareAndSwap(used, used+bytes) {
			return nil
		}
	}
}

func (d *profilingLoadDriver) writePhase(w io.Writer, phase profilingrun.Phase) error {
	raw, err := json.Marshal(phase)
	if err != nil {
		return err
	}
	store := d.stores[phase.ID]
	if store == nil {
		return errors.New("phase has no evidence store")
	}
	for _, field := range []struct {
		name  string
		array *profilingJSONArray
	}{
		{"requests", &store.requests}, {"latencyMs", &store.latencies}, {"nativeSamples", &store.samples},
	} {
		key := []byte(`"` + field.name + `":`)
		index := bytes.Index(raw, key)
		if index < 0 {
			return errors.New("phase evidence array is missing")
		}
		end := index + len(key)
		if !bytes.HasPrefix(raw[end:], []byte("null")) {
			return errors.New("phase evidence must remain disk-backed")
		}
		if _, err := w.Write(raw[:end]); err != nil {
			return err
		}
		if err := field.array.write(w); err != nil {
			return err
		}
		raw = raw[end+len("null"):]
	}
	_, err = w.Write(raw)
	return err
}

func (d *profilingLoadDriver) resultFile() (*os.File, error) {
	if err := d.checkResultBudget(); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(d.evidenceDir, "result-")
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()
	if _, err := io.WriteString(file, `{"status":"Succeeded","phases":[`); err != nil {
		return nil, err
	}
	for index, phase := range d.result.Phases {
		if index > 0 {
			if _, err := io.WriteString(file, ","); err != nil {
				return nil, err
			}
		}
		if err := d.writePhase(file, phase); err != nil {
			return nil, err
		}
	}
	if _, err := io.WriteString(file, `],"supplySizing":`); err != nil {
		return nil, err
	}
	if len(d.result.SupplySizing) == 0 {
		_, err = io.WriteString(file, "null")
	} else {
		_, err = file.Write(d.result.SupplySizing)
	}
	if err != nil {
		return nil, err
	}
	if _, err = io.WriteString(file, "}"); err != nil {
		return nil, err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	ok = true
	return file, nil
}

func (s *profilingPhaseStore) sort() {
	slices.SortFunc(s.requests.entries, func(a, b profilingJSONEntry) int {
		if order := a.at.Compare(b.at); order != 0 {
			return order
		}
		return strings.Compare(a.id, b.id)
	})
	for _, array := range []*profilingJSONArray{&s.samples, &s.latencies} {
		slices.SortFunc(array.entries, func(a, b profilingJSONEntry) int {
			if a.batch < b.batch {
				return -1
			}
			if a.batch > b.batch {
				return 1
			}
			return 0
		})
	}
}
