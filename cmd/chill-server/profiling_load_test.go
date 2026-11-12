package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"math"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func profilingTestPlan(endpoint string) profilingLoadPlan {
	return profilingLoadPlan{NativeEndpoint: endpoint, InferPath: "/v2/models/classifier/infer", Model: "classifier", Fields: []string{"image"}, Batch: 1, PodUID: "pod", WorkloadResourceVersion: "definition", SLOPercentile: 99, SLOTargetLatencyNanos: 1e9, MaxQueueRequests: 64, MaxQueueBytes: 4 << 20, MinServiceSamples: 1, MaxLatencySamples: 10000, MaxResultBytes: 32 << 20,
		OperatingPoints: profilingOperatingPoints{Utilizations: []float64{.5}, SparseMeanGapMilliseconds: 2, PhaseDurationSeconds: .04, PostLoadDurationSeconds: .01, RequestLifetimeSeconds: 2, DeliveryMarginMilliseconds: 10, FramedDecodedRequestUpperBytes: 20 << 10, QueueBytesCap: 4 << 20}}
}

func profilingTestRecords(t testing.TB) []profilingRecord {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "record")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "image"), make([]byte, 16<<10), 0600); err != nil {
		t.Fatal(err)
	}
	return []profilingRecord{{ID: "record", directory: directory}}
}

// This server emits real native wire proof, not compact retained evidence.
func profilingNativeStub(t testing.TB, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	var batches atomic.Uint64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered := time.Now().UTC()
		var request native.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode native request: %v", err)
			w.WriteHeader(400)
			return
		}
		if len(request.Inputs) != 1 || len(request.Inputs[0].Data) != 1 || len(request.Inputs[0].Data[0]) != 16<<10 {
			t.Errorf("gateway serializer lost raw field: %+v", request.Inputs)
		}
		requests.Add(1)
		enqueued := time.Since(entered).Nanoseconds()
		started := time.Now().UTC()
		time.Sleep(150 * time.Microsecond)
		finished := time.Now().UTC()
		zero := int64(0)
		finishOffset := finished.Sub(entered).Nanoseconds()
		service := finished.Sub(started).Nanoseconds()
		progress := native.Progress{RequestID: request.ID, WorkloadUID: request.Context.WorkloadUID, Definition: request.Context.Definition, Phase: "finished", At: finished, BatchID: batches.Add(1), BatchSize: 1, BatchRequestIDs: []string{request.ID}, BatchStartedAt: &started, BatchFinishedAt: &finished, ServiceSample: &native.ServiceSample{ServiceDurationNanos: service, PrecedingIdleNanos: &zero, TrialID: request.Context.WorkloadUID}, BackendMS: float64(service) / 1e6, ServiceMS: float64(service) / 1e6, Units: 1, NativeElapsedMS: float64(finishOffset) / 1e6, ExecutionComplete: true, Outcome: "completed", NativeHTTP: &native.HTTPRequestTiming{HandlerEnteredAt: entered, ReceivedNanos: &zero, EnqueueNanos: &enqueued, FinishedNanos: &finishOffset}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(native.Response{ID: request.ID, ModelName: "classifier", Outputs: []native.Field{{Name: "probabilities", Data: [][]byte{[]byte(`[0.02,0.03,0.93,0.02]`)}}}, Progress: progress, ExecutionComplete: true})
	}))
	return server
}

func TestProfilingLoadPoissonLagRetainsGatewayClientClock(t *testing.T) {
	var count atomic.Int64
	server := profilingNativeStub(t, &count)
	var lock sync.Mutex
	origins := make(map[string]native.RequestContext)
	handler := server.Config.Handler
	server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var request native.Request
		if err := json.Unmarshal(raw, &request); err != nil {
			t.Error(err)
			return
		}
		lock.Lock()
		origins[request.ID] = request.Context
		lock.Unlock()
		handler.ServeHTTP(w, r)
	})
	server.Start()
	defer server.Close()
	// Stall one pre-send allocation like a rare scheduler/GC pause.
	// Keep arrivals independent; dispatch delay consumes the client lifetime.
	oldNew := profilingResponseBuffers.New
	var delayed atomic.Bool
	profilingResponseBuffers = sync.Pool{New: func() any {
		if delayed.CompareAndSwap(false, true) {
			time.Sleep(11 * time.Millisecond)
		}
		return new(bytes.Buffer)
	}}
	defer func() { profilingResponseBuffers = sync.Pool{New: oldNew} }()
	plan := profilingTestPlan(server.URL)
	driver := newProfilingLoadDriver(plan, profilingTestRecords(t), "")
	defer driver.close()
	phase, err := profilingTestRunPhase(t, driver, context.Background(), profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 500})
	if err != nil {
		t.Fatal(err)
	}
	if phase.IssueLag.Max < 11 || phase.DriverGC == nil || phase.ScheduledInputs != phase.SentInputs || phase.SentInputs != phase.CompletedInputs {
		t.Fatalf("diagnostic gap lost or arrivals dropped: %+v", phase)
	}
	lock.Lock()
	defer lock.Unlock()
	for _, row := range phase.Requests {
		origin := origins[row.RequestID]
		if !origin.ReceivedAt.Equal(row.ScheduledAt) || origin.SenderElapsedMS <= 0 || origin.SenderElapsedMS > row.LatencyMilliseconds || origin.LifetimeMS != plan.OperatingPoints.RequestLifetimeSeconds*1000 {
			t.Fatalf("dispatch lost gateway client arrival/elapsed authority: row=%+v origin=%+v", row, origin)
		}
	}
}

func TestProfilingLoadPoissonWaitsFIFOForNativeAdmissionSlots(t *testing.T) {
	for _, blocked := range []string{"outstanding", "pool"} {
		t.Run(blocked, func(t *testing.T) {
			var count atomic.Int64
			server := profilingNativeStub(t, &count)
			server.Start()
			defer server.Close()
			plan := profilingTestPlan(server.URL)
			plan.MaxQueueRequests = 2
			driver := newProfilingLoadDriver(plan, profilingTestRecords(t), "")
			defer driver.close()
			if blocked == "pool" {
				driver.transport.MaxConnsPerHost = 1
			}
			driver.native.HTTPClient.Transport = profilingDelayedTransport{base: driver.transport, delay: 30 * time.Millisecond}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			phase, err := profilingTestRunPhase(t, driver, ctx, profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if phase.ScheduledInputs != phase.SentInputs || phase.SentInputs != phase.CompletedInputs || int64(phase.CompletedInputs) != count.Load() {
				t.Fatalf("native-slot waiting dropped client arrivals: %+v", phase)
			}
			var previous, drained time.Time
			var longestHold float64
			for _, row := range phase.Requests {
				if row.DispatchedAt.Before(previous) || row.DispatchedAt.Before(row.ScheduledAt) {
					t.Fatalf("native-slot dispatch is not FIFO: %+v", row)
				}
				previous = row.DispatchedAt
				longestHold = max(longestHold, row.DispatchedAt.Sub(row.ScheduledAt).Seconds())
				if row.CompletedAt.After(drained) {
					drained = row.CompletedAt
				}
			}
			if longestHold < .03 || !phase.OfferingEndedAt.Equal(phase.Requests[len(phase.Requests)-1].ScheduledAt) || !phase.DrainEndedAt.Equal(drained) || phase.AchievedArrivalRatePerSecond != float64(phase.ScheduledInputs)/phase.IssueElapsedSeconds {
				t.Fatalf("client wait or explicit offering/drain evidence was lost: %+v", phase)
			}
		})
	}
}

func TestProfilingLoadFIFOConsumesResultBudgetBeforeAnyCompletion(t *testing.T) {
	plan := profilingTestPlan("http://native")
	plan.MaxQueueRequests = 1
	plan.MaxResultBytes = 8 << 10
	plan.MaxLatencySamples = 1_000_000
	driver := newProfilingLoadDriver(plan, profilingTestRecords(t), "")
	defer driver.close()
	driver.native.HTTPClient.Transport = profilingDelayedTransport{base: driver.transport, delay: time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	phase, err := driver.runPhase(ctx, profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 1_000_000})
	if !errors.Is(err, errProfilingResultBudget) {
		t.Fatalf("FIFO did not fail with the result budget: phase=%+v err=%v", phase, err)
	}
	if phase.CompletedInputs != 0 || phase.ScheduledInputs >= plan.MaxLatencySamples {
		t.Fatalf("FIFO budget waited for responses or the sample limit: %+v", phase)
	}
}

func TestProfilingLoadPoissonReplaysEveryArrivalAndExcludesDrain(t *testing.T) {
	var count atomic.Int64
	server := profilingNativeStub(t, &count)
	server.Start()
	defer server.Close()
	plan := profilingTestPlan(server.URL)
	driver := newProfilingLoadDriver(plan, profilingTestRecords(t), "")
	defer driver.close()
	driver.native.HTTPClient.Transport = profilingDelayedTransport{base: driver.transport, delay: 20 * time.Millisecond}
	phase, err := profilingTestRunPhase(t, driver, context.Background(), profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 500})
	if err != nil {
		t.Fatal(err)
	}
	if phase.ArrivalSeed != plan.PodUID+"/"+phase.ID || phase.IssueElapsedSeconds >= phase.MeasurementElapsedSeconds || phase.AchievedArrivalRatePerSecond != float64(phase.SentInputs)/phase.IssueElapsedSeconds {
		t.Fatalf("incorrect arrival identity or offering exposure: %+v", phase)
	}
	hash := fnv.New128a()
	_, _ = io.WriteString(hash, phase.ArrivalSeed)
	seed := hash.Sum(nil)
	rng := rand.New(rand.NewPCG(binary.BigEndian.Uint64(seed[:8]), binary.BigEndian.Uint64(seed[8:])))
	start, err := time.Parse(time.RFC3339Nano, phase.MeasurementStartedAt)
	if err != nil {
		t.Fatal(err)
	}
	arrivals := make([]time.Time, 0, len(phase.Requests))
	for _, row := range phase.Requests {
		arrivals = append(arrivals, row.ScheduledAt)
	}
	slices.SortFunc(arrivals, func(a, b time.Time) int { return a.Compare(b) })
	next := start
	for index, arrival := range arrivals {
		next = next.Add(time.Duration(max(1, math.Round(rng.ExpFloat64()/phase.ArrivalRatePerSecond*1e9))))
		if !next.Equal(arrival) {
			t.Fatalf("scheduled arrival %d skipped or changed: want=%s got=%s", index, next, arrival)
		}
	}
	if phase.ScheduledInputs != len(arrivals) || phase.SentInputs != len(arrivals) || phase.IssueElapsedSeconds != next.Sub(start).Seconds() {
		t.Fatalf("offering exposure does not end at last covered scheduled arrival: %+v", phase)
	}
}

func TestProfilingLoadPoissonKeepsKnownGapSampleFloor(t *testing.T) {
	var count atomic.Int64
	server := profilingNativeStub(t, &count)
	server.Start()
	defer server.Close()
	plan := profilingTestPlan(server.URL)
	plan.OperatingPoints.PhaseDurationSeconds = .005
	plan.MinServiceSamples = 59
	driver := newProfilingLoadDriver(plan, profilingTestRecords(t), "")
	defer driver.close()
	phase, err := profilingTestRunPhase(t, driver, context.Background(), profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 500})
	if err != nil {
		t.Fatal(err)
	}
	known := 0
	for _, sample := range phase.NativeSamples {
		if sample.ServiceSample.PrecedingIdleNanos != nil {
			known++
		}
	}
	if known < plan.MinServiceSamples || phase.MeasurementElapsedSeconds <= plan.OperatingPoints.PhaseDurationSeconds || phase.ScheduledInputs != phase.CompletedInputs {
		t.Fatalf("duration stop weakened known-gap sample floor: %+v known=%d", phase, known)
	}
}

func TestProfilingDriverGCSummaryUsesIssuingInterval(t *testing.T) {
	before := runtime.MemStats{NumGC: 255, PauseTotalNs: 9_000_000}
	after := runtime.MemStats{NumGC: 257, PauseTotalNs: 12_000_000}
	after.PauseNs[254], after.PauseNs[255], after.PauseNs[0] = 50_000_000, 2_000_000, 1_000_000
	gc := profilingDriverGCSummary(before, after)
	if gc.Cycles != 2 || gc.PauseTotalMilliseconds != 3 || gc.MaxPauseMilliseconds != 2 {
		t.Fatalf("GC summary includes pauses outside issuing interval: %+v", gc)
	}
}

type profilingDelayedTransport struct {
	base  http.RoundTripper
	delay time.Duration
}

func (d profilingDelayedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	timer := time.NewTimer(d.delay)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
		return nil, r.Context().Err()
	case <-timer.C:
		return d.base.RoundTrip(r)
	}
}

func TestProfilingLoadReusesGatewayConnectionsAndCompactsProof(t *testing.T) {
	var requests, connections atomic.Int64
	server := profilingNativeStub(t, &requests)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	driver := newProfilingLoadDriver(profilingTestPlan(server.URL), profilingTestRecords(t), "")
	defer driver.close()
	phase, err := profilingTestRunPhase(t, driver, context.Background(), profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 500})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 5 || connections.Load() >= requests.Load()/2 {
		t.Fatalf("native connections not reused: requests=%d connections=%d", requests.Load(), connections.Load())
	}
	if phase.IssueLag.Count != phase.SentInputs || phase.ScheduledInputs != phase.SentInputs || phase.SentInputs != phase.CompletedInputs || phase.AchievedArrivalRatePerSecond != float64(phase.SentInputs)/phase.IssueElapsedSeconds {
		t.Fatalf("issue evidence lost scheduled or actual arrivals: %+v", phase)
	}
	encoded, err := json.Marshal(phase)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"batchRequestIDs", "batchStartedAt", "batchFinishedAt", "serviceSample", "preprocessMS", "backendMS", "postprocessMS", "firstTokenMS"} {
		if bytes.Contains(encoded, []byte(`"`+forbidden+`"`)) {
			t.Fatalf("retained redundant batch field %s", forbidden)
		}
	}
	for index, sample := range phase.NativeSamples {
		if len(sample.RequestIDs) != 1 || len(sample.RecordIDs) != 1 || sample.RequestIDs[0] == "" || sample.StartedAt.IsZero() || sample.FinishedAt.IsZero() || sample.EarliestEnqueuedAt.IsZero() || phase.LatencyMilliseconds[index] <= 0 {
			t.Fatalf("compact evidence lost membership or native timestamps: %+v", sample)
		}
	}
	if len(phase.Requests[0].Response.Outputs[0].Data[0]) == 0 {
		t.Fatal("compact raw discarded native output")
	}
}

func TestProfilingLoadRejectsExactBudgetDuringFirstPhase(t *testing.T) {
	var requests atomic.Int64
	server := profilingNativeStub(t, &requests)
	server.Start()
	defer server.Close()
	plan := profilingTestPlan(server.URL)
	plan.MaxResultBytes = 128
	var phaseIDs []string
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/measurement/phase":
			var boundary struct{ ID, Action string }
			_ = json.NewDecoder(r.Body).Decode(&boundary)
			if boundary.Action == "start" {
				phaseIDs = append(phaseIDs, boundary.ID)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "/measurement/supply":
			_ = json.NewEncoder(w).Encode(map[string]int{"workers": 1})
		default:
			w.WriteHeader(404)
		}
	}))
	defer control.Close()
	driver := newProfilingLoadDriver(plan, profilingTestRecords(t), control.URL)
	defer driver.close()
	_, err := driver.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "result budget") || strings.Join(phaseIDs, ",") != "sparse" {
		t.Fatalf("late or missing exact byte-budget rejection: phases=%v err=%v", phaseIDs, err)
	}
}

func TestProfilingLoadAbsoluteDeadlineCancelsControlAndNative(t *testing.T) {
	for _, blocked := range []string{"control", "native"} {
		t.Run(blocked, func(t *testing.T) {
			entered := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(entered)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
			defer cancel()
			start := time.Now()
			var err error
			if blocked == "control" {
				err = profilingLoad(ctx, server.URL, t.TempDir())
			} else {
				driver := newProfilingLoadDriver(profilingTestPlan(server.URL), profilingTestRecords(t), "")
				defer driver.close()
				_, err = driver.runPhase(ctx, profilingrun.Phase{ID: "saturation", Kind: "saturation", Workers: 1})
				var failure *profilingLoadFailure
				if !errors.As(err, &failure) || failure.IssueEvidence == nil || failure.IssueEvidence.DriverGC == nil {
					t.Fatalf("failed saturation lost driver GC evidence: %v", err)
				}
				raw, marshalErr := json.Marshal(failure.IssueEvidence)
				var summary map[string]json.RawMessage
				if marshalErr != nil || json.Unmarshal(raw, &summary) != nil || len(summary) != 11 || summary["driverGC"] == nil || summary["scheduledInputs"] == nil || summary["sentInputs"] == nil || summary["completedInputs"] == nil || summary["offeringStartedAt"] == nil || summary["offeringEndedAt"] == nil || summary["drainEndedAt"] == nil || summary["arrivalSeed"] != nil || summary["issueLagMs"] != nil {
					t.Fatalf("failed saturation lost common offering/drain evidence: %s err=%v", raw, marshalErr)
				}
			}
			if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
				t.Fatalf("absolute deadline did not abort %s: %v", blocked, err)
			}
			select {
			case <-entered:
			default:
				t.Fatal("request never reached blocking endpoint")
			}
		})
	}
}

func TestProfilingLoadPreservesNativeAllocationFailureWhileWaitingForSlots(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(native.ErrorResponse{Error: "BFCArena allocation failed", FailureReason: "MemoryAllocationFailed", ExecutionComplete: true})
	}))
	defer server.Close()
	driver := newProfilingLoadDriver(profilingTestPlan(server.URL), profilingTestRecords(t), "")
	driver.plan.MaxQueueRequests = 1
	defer driver.close()
	driver.native.HTTPClient.Transport = profilingDelayedTransport{base: driver.native.HTTPClient.Transport, delay: 20 * time.Millisecond}
	_, err := driver.runPhase(context.Background(), profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: 500})
	var failure *profilingLoadFailure
	if !errors.As(err, &failure) || failure.FailureReason != "MemoryAllocationFailed" || failure.IssueEvidence == nil {
		t.Fatalf("native allocation failure lost while waiting for admission: %v", err)
	}
}

func TestProfilingLoadProtocolUploadsMeasuredLadder(t *testing.T) {
	var count atomic.Int64
	server := profilingNativeStub(t, &count)
	server.Start()
	defer server.Close()
	plan := profilingTestPlan(server.URL)
	dataset := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataset, "record"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataset, "record", "image"), make([]byte, 16<<10), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var boundaries []string
	var result profilingLoadResult
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == http.MethodPost && r.ContentLength <= 0 {
			t.Error("LoadControl requires an exact Content-Length for streamed uploads")
		}
		switch r.URL.Path {
		case "/measurement/load":
			_ = json.NewEncoder(w).Encode(plan)
		case "/measurement/phase":
			var value struct{ ID, Action string }
			_ = json.NewDecoder(r.Body).Decode(&value)
			boundaries = append(boundaries, value.ID+":"+value.Action)
			_ = json.NewEncoder(w).Encode(map[string]string{"at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "/measurement/supply":
			var supplied struct {
				NativeSamples []profilingrun.NativeServiceSample `json:"nativeSamples"`
			}
			_ = json.NewDecoder(r.Body).Decode(&supplied)
			if len(supplied.NativeSamples) < 1 {
				t.Error("supply lacks measured native samples")
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"workers": 1})
		case "/measurement/load-result":
			if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer control.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := profilingLoad(ctx, control.URL, dataset); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if result.Status != "Succeeded" || len(result.Phases) != 3 || strings.Join(boundaries, ",") != "sparse:start,sparse:end,saturation:start,saturation:end,utilization-0:start,utilization-0:end" {
		t.Fatalf("control sequence incomplete: %+v %v", result, boundaries)
	}
	saturation := result.Phases[1]
	if saturation.IssueLag != nil || saturation.ArrivalSeed != "" || saturation.ScheduledInputs != saturation.CompletedInputs || saturation.SentInputs != saturation.CompletedInputs || saturation.OfferingStartedAt.IsZero() || saturation.OfferingEndedAt.IsZero() || saturation.DrainEndedAt.IsZero() || saturation.DriverGC == nil || result.Phases[2].ArrivalRatePerSecond != .5*float64(saturation.CompletedInputs)/saturation.MeasurementElapsedSeconds {
		t.Fatalf("ladder did not use measured completed-input saturation or retained wrong driver fields: %+v", result.Phases)
	}
}

func TestProfilingLoadDatasetFieldOrderAndFramedBound(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "one"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"z", "a"} {
		if err := os.WriteFile(filepath.Join(dir, "one", name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan := profilingTestPlan("http://native")
	plan.Fields = []string{"z", "a"}
	plan.OperatingPoints.FramedDecodedRequestUpperBytes = 10
	records, err := loadProfilingRecords(context.Background(), dir, plan)
	if err != nil || len(records) != 1 {
		t.Fatalf("dataset records: %+v %v", records, err)
	}
	driver := newProfilingLoadDriver(plan, records, "")
	defer driver.close()
	inputs, err := driver.recordInputs(context.Background(), records[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inputs)
	var fields []native.Field
	if err == nil {
		err = json.Unmarshal(raw, &fields)
	}
	if err != nil || len(fields) != 2 || fields[0].Name != "z" || fields[1].Name != "a" {
		t.Fatalf("dataset ignored frozen field order: %+v %v", fields, err)
	}
	plan.OperatingPoints.FramedDecodedRequestUpperBytes = 9
	if _, err := loadProfilingRecords(context.Background(), dir, plan); err == nil {
		t.Fatal("accepted raw fields exceeding framed decoded request upper bound")
	}
}
