package main

// Native load primitives shared by the load drivers: dataset records, the
// request context, the pooled native client and seeded arrivals.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	gateway "github.com/lab-paper-code/chill/internal/serving/firstfloor"
)

var profilingResponseBuffers = sync.Pool{New: func() any { return new(bytes.Buffer) }}

type profilingRecord struct {
	ID        string
	directory string
}

// loadRequestContext is the native request context a load driver sends.
type loadRequestContext struct {
	workloadUID, definition string
	slo                     native.SLO
	marginMS, lifetimeMS    float64
}

// nativeRequest dates the request at its scheduled arrival: the gateway's
// client clock starts there, and admission-slot waiting is sender elapsed time
// that belongs to the end-to-end client latency.
func (c loadRequestContext) nativeRequest(id string, scheduled, sent time.Time) native.Request {
	return native.Request{ID: id, Context: native.RequestContext{
		RequestID: id, WorkloadUID: c.workloadUID, Definition: c.definition,
		AdmissionPolicy: native.AdmissionDelay, ReceivedAt: scheduled.UTC(),
		SenderElapsedMS: float64(sent.Sub(scheduled)) / 1e6, CompletionMarginMS: c.marginMS,
		SLO: c.slo, LifetimeMS: c.lifetimeMS,
	}}
}

func newNativeLoadClient(maxQueueRequests int, timeout time.Duration) (*gateway.Client, *http.Transport) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Native admission owns the explicit FIFO. HTTP pooling must not add a
	// second, unobserved dispatch queue behind those frozen native slots.
	transport.MaxIdleConnsPerHost = max(maxQueueRequests, 64)
	transport.MaxIdleConns = transport.MaxIdleConnsPerHost
	return &gateway.Client{HTTPClient: &http.Client{Transport: transport, Timeout: timeout}}, transport
}

// arrivalRNG is a reproducible random source named by seed.
func arrivalRNG(seed string) *rand.Rand {
	hash := fnv.New128a()
	_, _ = io.WriteString(hash, seed)
	var sum [16]byte
	digest := hash.Sum(sum[:0])
	return rand.New(rand.NewPCG(binary.BigEndian.Uint64(digest[:8]), binary.BigEndian.Uint64(digest[8:])))
}

// readProfilingRecords lists the dataset's record directories and rejects a
// record whose framed fields exceed the native per-request upper bytes.
func readProfilingRecords(
	ctx context.Context, directory string, fields []string, upperBytes int64,
) ([]profilingRecord, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	records := make([]profilingRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		record := profilingRecord{ID: entry.Name(), directory: filepath.Join(directory, entry.Name())}
		var framed int64
		for _, field := range fields {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			info, err := os.Stat(filepath.Join(record.directory, field))
			if err != nil {
				return nil, err
			}
			if !info.Mode().IsRegular() || info.Size() > upperBytes-framed-4 {
				return nil, errors.New("dataset record exceeds frozen framed decoded request upper bytes or is not a regular file")
			}
			framed += 4 + info.Size()
		}
		records = append(records, record)
	}
	if len(records) == 0 {
		return nil, errors.New("profiling dataset contains no records")
	}
	return records, ctx.Err()
}

// profilingRecordSource prepares dataset records as native inputs.
type profilingRecordSource struct {
	fields     []string
	upperBytes int64
	cache      struct {
		sync.Mutex
		id     string
		inputs gateway.PreparedNativeInputs
	}
}

func (d *profilingRecordSource) recordInputs(
	ctx context.Context, record profilingRecord,
) (gateway.PreparedNativeInputs, error) {
	d.cache.Lock()
	defer d.cache.Unlock()
	if err := ctx.Err(); err != nil {
		return gateway.PreparedNativeInputs{}, err
	}
	if d.cache.id == record.ID {
		return d.cache.inputs, nil
	}
	// ponytail: one immutable record cached; a byte-bounded LRU is warranted only
	// if measured multi-record disk reads dominate offering lag. Never cache the
	// whole dataset or retain the previous record while preparing its successor.
	d.cache.id, d.cache.inputs = "", gateway.PreparedNativeInputs{}
	inputs := make([]native.Field, len(d.fields))
	var framed int64
	for index, field := range d.fields {
		if err := ctx.Err(); err != nil {
			return gateway.PreparedNativeInputs{}, err
		}
		remaining := d.upperBytes - framed - 4
		if remaining < 0 {
			return gateway.PreparedNativeInputs{}, errors.New("dataset record exceeds frozen framed decoded request upper bytes")
		}
		file, err := os.Open(filepath.Join(record.directory, field))
		if err != nil {
			return gateway.PreparedNativeInputs{}, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return gateway.PreparedNativeInputs{}, err
		}
		if int64(len(data)) > remaining {
			return gateway.PreparedNativeInputs{}, errors.New("dataset record exceeds frozen framed decoded request upper bytes")
		}
		framed += 4 + int64(len(data))
		inputs[index] = native.Field{Name: field, Data: [][]byte{data}}
	}
	prepared, err := gateway.PrepareNativeInputs(inputs)
	if err != nil {
		return gateway.PreparedNativeInputs{}, err
	}
	d.cache.id, d.cache.inputs = record.ID, prepared
	return prepared, nil
}
