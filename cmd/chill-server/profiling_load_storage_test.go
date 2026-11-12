package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func profilingReadStoredPhase(t testing.TB, driver *profilingLoadDriver, phase profilingrun.Phase) profilingrun.Phase {
	t.Helper()
	var buffer bytes.Buffer
	if err := driver.writePhase(&buffer, phase); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(buffer.Bytes(), &phase); err != nil {
		t.Fatal(err)
	}
	return phase
}

func profilingTestRunPhase(
	t testing.TB, driver *profilingLoadDriver, ctx context.Context, spec profilingrun.Phase,
) (profilingrun.Phase, error) {
	t.Helper()
	phase, err := driver.runPhase(ctx, spec)
	if err == nil {
		phase = profilingReadStoredPhase(t, driver, phase)
	}
	return phase, err
}

func profilingTestCollector(t testing.TB, plan profilingLoadPlan) (*profilingPhaseCollector, *profilingLoadDriver) {
	t.Helper()
	driver := newProfilingLoadDriver(plan, profilingTestRecords(t), "")
	t.Cleanup(driver.close)
	store, err := driver.openPhaseStore("proof")
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.updatePhase(profilingrun.Phase{ID: "proof"}); err != nil {
		t.Fatal(err)
	}
	return &profilingPhaseCollector{
		plan: plan, store: store, groups: make(map[uint64]*profilingBatchGroup),
		requests: make(map[string]bool), completed: make(map[uint64]bool), checkBudget: driver.checkResultBudget,
	}, driver
}

func profilingTestFinishCollector(
	t testing.TB, collector *profilingPhaseCollector, driver *profilingLoadDriver,
) ([]profilingrun.PhaseRequest, []profilingrun.NativeServiceSample, []float64, error) {
	t.Helper()
	if err := collector.finish(); err != nil {
		return nil, nil, nil, err
	}
	phase := profilingReadStoredPhase(t, driver, driver.result.Phases[0])
	return phase.Requests, phase.NativeSamples, phase.LatencyMilliseconds, nil
}

func TestProfilingLoadWritesOutputsToDataVolumeAndReleasesBatchProof(t *testing.T) {
	plan := profilingTestPlan("http://native")
	plan.Batch = 2
	collector, driver := profilingTestCollector(t, plan)
	for _, id := range []string{"left", "right"} {
		row, response := profilingProofPair(id)
		response.Outputs[0].Data[0] = bytes.Repeat([]byte("payload"), 10000)
		if _, err := collector.add(row, response); err != nil {
			t.Fatal(err)
		}
	}
	if len(collector.groups) != 0 || len(collector.store.requests.entries) != 2 {
		t.Fatal("finished batch proof or output rows remain in memory")
	}
	info, err := collector.store.requests.file.Stat()
	if err != nil || info.Size() < 140000 ||
		filepath.Dir(collector.store.requests.file.Name()) != driver.evidenceDir ||
		filepath.Dir(driver.evidenceDir) != driver.dataDir {
		t.Fatalf("outputs not streamed to data volume: info=%v err=%v", info, err)
	}
	if err := collector.finish(); err != nil {
		t.Fatal(err)
	}
	file, err := driver.resultFile()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := file.Close(); err != nil {
			t.Error(err)
		}
	}()
	resultInfo, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	exact, err := driver.resultBytes()
	if err != nil || exact != resultInfo.Size() {
		t.Fatalf("incremental budget differs from exact upload: counted=%d size=%d err=%v", exact, resultInfo.Size(), err)
	}
	driver.plan.MaxResultBytes = exact
	if err := driver.checkResultBudget(); err != nil {
		t.Fatal(err)
	}
	driver.plan.MaxResultBytes--
	if err := driver.checkResultBudget(); err == nil {
		t.Fatal("accepted one byte beyond exact frozen budget")
	}
}

func TestProfilingLoadOutputBudgetFailsBeforeBatchFinishes(t *testing.T) {
	plan := profilingTestPlan("http://native")
	plan.Batch = 2
	collector, driver := profilingTestCollector(t, plan)
	driver.plan.MaxResultBytes = 2000
	row, response := profilingProofPair("left")
	response.Outputs[0].Data[0] = bytes.Repeat([]byte("large"), 1000)
	if _, err := collector.add(row, response); err == nil || !strings.Contains(err.Error(), "result budget") {
		t.Fatalf("oversized first output waited for remaining batch/phase: %v", err)
	}
	if len(collector.store.samples.entries) != 0 {
		t.Fatal("budget test unexpectedly completed batch")
	}
}

func TestProfilingLoadDatasetUsesOneRecordCache(t *testing.T) {
	records := profilingTestRecords(t)
	root := filepath.Dir(records[0].directory)
	second := filepath.Join(root, "second")
	if err := os.Mkdir(second, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "image"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := profilingTestPlan("http://native")
	loaded, err := loadProfilingRecords(context.Background(), root, plan)
	if err != nil || len(loaded) != 2 {
		t.Fatalf("index dataset: %v %v", loaded, err)
	}
	driver := newProfilingLoadDriver(plan, loaded, "")
	defer driver.close()
	if _, err := driver.recordInputs(context.Background(), loaded[0]); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(loaded[0].directory, "image")); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.recordInputs(context.Background(), loaded[0]); err != nil {
		t.Fatalf("current record not cached: %v", err)
	}
	if _, err := driver.recordInputs(context.Background(), loaded[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := driver.recordInputs(context.Background(), loaded[0]); err == nil {
		t.Fatal("old wire payload survived single-record eviction")
	}
}

func TestProfilingLoadAllowsNonmultipleSaturationWorkers(t *testing.T) {
	type incoming struct {
		request  native.Request
		entered  time.Time
		response chan native.Response
	}
	var mu sync.Mutex
	var queued []incoming
	var batches atomic.Uint64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := incoming{entered: time.Now().UTC(), response: make(chan native.Response, 1)}
		if err := json.NewDecoder(r.Body).Decode(&item.request); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		queued = append(queued, item)
		if len(queued) == 2 {
			pair := queued
			queued = nil
			batch := batches.Add(1)
			go func() {
				start := time.Now().UTC()
				time.Sleep(150 * time.Microsecond)
				finish := time.Now().UTC()
				ids := []string{pair[0].request.ID, pair[1].request.ID}
				for _, member := range pair {
					zero, finished := int64(0), finish.Sub(member.entered).Nanoseconds()
					service := finish.Sub(start).Nanoseconds()
					progress := native.Progress{
						RequestID: member.request.ID, WorkloadUID: "pod", Definition: "definition", Phase: "finished",
						At: finish, BatchID: batch, BatchSize: 2, BatchRequestIDs: ids,
						BatchStartedAt: &start, BatchFinishedAt: &finish,
						ServiceSample: &native.ServiceSample{
							ServiceDurationNanos: service, PrecedingIdleNanos: &zero, TrialID: "pod",
						},
						BackendMS: float64(service) / 1e6, ServiceMS: float64(service) / 1e6,
						Units: 1, NativeElapsedMS: float64(finished) / 1e6, ExecutionComplete: true,
						NativeHTTP: &native.HTTPRequestTiming{
							HandlerEnteredAt: member.entered,
							ReceivedNanos:    &zero, EnqueueNanos: &zero, FinishedNanos: &finished,
						},
					}
					member.response <- native.Response{
						ID: member.request.ID, ModelName: "classifier",
						Outputs:  []native.Field{{Name: "class", Data: [][]byte{{3}}}},
						Progress: progress, ExecutionComplete: true,
					}
				}
			}()
		}
		mu.Unlock()
		select {
		case response := <-item.response:
			_ = json.NewEncoder(w).Encode(response)
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == profilingSupplyPath {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, `{"workers": 5}`)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		_ = json.NewEncoder(w).Encode(map[string]string{"at": time.Now().UTC().Format(time.RFC3339Nano)})
	}))
	defer control.Close()
	plan := profilingTestPlan(server.URL)
	plan.Batch = 2
	driver := newProfilingLoadDriver(plan, profilingTestRecords(t), control.URL)
	defer driver.close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := driver.run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Phases) != 3 || result.Phases[1].Workers != 5 {
		t.Fatalf("B=2 K=5 was rejected: %+v", result)
	}
	for _, phase := range result.Phases {
		if phase.CompletedInputs%2 != 0 {
			t.Fatalf("phase stopped between complete batches: %+v", phase)
		}
		stored := profilingReadStoredPhase(t, driver, phase)
		if len(stored.Requests) != phase.CompletedInputs || len(stored.NativeSamples)*2 != phase.CompletedInputs {
			t.Fatalf("phase lost fixed batch membership: %+v", phase)
		}
	}
}

type profilingByteCounter int64

func (n *profilingByteCounter) Write(data []byte) (int, error) {
	*n += profilingByteCounter(len(data))
	return len(data), nil
}
