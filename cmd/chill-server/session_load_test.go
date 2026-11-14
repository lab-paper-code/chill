package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/test/fixtures"
)

// sessionFake is a fixed-batch native executor: it answers once B requests
// are queued, reports the given service time and records per-level
// outstanding requests. A failure answers every member of that batch.
type sessionFake struct {
	t          testing.TB
	batch      int
	service    func(batch uint64) (reported, sleep time.Duration)
	failure    func(kind string, ordinal int) (int, string)
	calls      chan sessionFakeCall
	done       chan struct{}
	mu         sync.Mutex
	open       map[string]int
	maxOpen    map[string]int
	finished   map[string]int
	active     map[string]native.RequestState
	nativeRows map[string][]profilederivation.NativeBatchRow
	busyStarts map[string]bool
	neverIdle  bool
	server     *httptest.Server
}

type sessionFakeCall struct {
	id, kind, trial string
	reply           chan sessionFakeReply
}

type sessionFakeReply struct {
	status int
	body   []byte
}

func newSessionFake(
	t testing.TB, batch int, service func(uint64) (time.Duration, time.Duration), failure func(string, int) (int, string),
) *sessionFake {
	f := &sessionFake{
		t: t, batch: batch, service: service, failure: failure, calls: make(chan sessionFakeCall),
		done: make(chan struct{}), open: map[string]int{}, maxOpen: map[string]int{}, finished: map[string]int{},
		active: map[string]native.RequestState{}, nativeRows: map[string][]profilederivation.NativeBatchRow{},
		busyStarts: map[string]bool{},
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	go f.execute()
	t.Cleanup(func() {
		close(f.done)
		f.server.Close()
	})
	return f
}

func (f *sessionFake) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/state") {
		f.mu.Lock()
		state := native.State{At: time.Now(), Ready: true}
		for _, request := range f.active {
			state.Requests = append(state.Requests, request)
		}
		if f.neverIdle {
			state.Requests = append(state.Requests, native.RequestState{Phase: "running"})
		}
		f.mu.Unlock()
		if err := json.NewEncoder(w).Encode(state); err != nil {
			f.t.Error(err)
		}
		return
	}
	var request native.Request
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		f.t.Errorf("decode native request: %v", err)
		return
	}
	kind := request.ID[:strings.LastIndexByte(request.ID, '-')]
	if want := "sess/" + strconv.Itoa(f.batch) + "/" + kind; request.Context.WorkloadUID != want {
		f.t.Errorf("request %s workload %q, want %q", request.ID, request.Context.WorkloadUID, want)
	}
	f.mu.Lock()
	for _, active := range f.active {
		if active.Context.WorkloadUID != request.Context.WorkloadUID {
			f.busyStarts[kind] = true
		}
	}
	f.active[request.ID] = native.RequestState{Context: request.Context, Phase: "queued"}
	f.open[kind]++
	f.maxOpen[kind] = max(f.maxOpen[kind], f.open[kind])
	f.mu.Unlock()
	call := sessionFakeCall{
		id: request.ID, kind: kind, trial: request.Context.WorkloadUID, reply: make(chan sessionFakeReply, 1),
	}
	var reply sessionFakeReply
	select {
	case f.calls <- call:
		select {
		case reply = <-call.reply:
		case <-r.Context().Done():
		case <-f.done:
		}
	case <-r.Context().Done():
	case <-f.done:
	}
	// Release the slot before the client can observe the answer.
	f.mu.Lock()
	f.open[kind]--
	f.mu.Unlock()
	if reply.status != 0 {
		w.WriteHeader(reply.status)
		_, _ = w.Write(reply.body)
	}
}

func (f *sessionFake) execute() {
	var lastEnd time.Time
	ordinals := map[string]int{}
	for id := uint64(1); ; id++ {
		members := make([]sessionFakeCall, 0, f.batch)
		for len(members) < f.batch {
			select {
			case call := <-f.calls:
				members = append(members, call)
			case <-f.done:
				return
			}
		}
		kind := members[0].kind
		ids := make([]string, len(members))
		f.mu.Lock()
		for index, member := range members {
			if member.kind != kind {
				f.t.Errorf("native batch mixes levels %s and %s", kind, member.kind)
			}
			ids[index] = member.id
			state := f.active[member.id]
			state.Phase = "running"
			f.active[member.id] = state
		}
		f.mu.Unlock()
		ordinals[kind]++
		status, failure := 0, ""
		if f.failure != nil {
			status, failure = f.failure(kind, ordinals[kind])
		}
		expired := status == http.StatusRequestTimeout
		if expired {
			for _, member := range members {
				member.reply <- sessionFakeReply{status: status, body: []byte(failure)}
			}
		}
		reported, sleep := f.service(id)
		started := time.Now()
		time.Sleep(sleep)
		var idle *int64
		if !lastEnd.IsZero() {
			gap := started.Sub(lastEnd).Nanoseconds()
			idle = &gap
		}
		lastEnd = time.Now()
		f.mu.Lock()
		if status == 0 || expired {
			f.finished[kind]++
			f.nativeRows[kind] = append(f.nativeRows[kind], profilederivation.NativeBatchRow{BatchID: id, BatchSize: f.batch,
				StartedAt: lastEnd.Add(-reported), FinishedAt: lastEnd, ServiceDurationNanos: int64(reported),
				PrecedingIdleNanos: idle, LatestEnqueuedAt: started})
		}
		for _, member := range members {
			delete(f.active, member.id)
		}
		f.mu.Unlock()
		for index, member := range members {
			if expired {
				continue
			}
			if status != 0 {
				member.reply <- sessionFakeReply{status: status, body: []byte(failure)}
				continue
			}
			body, err := json.Marshal(native.Response{
				ID: member.id, ModelName: "classifier", ExecutionComplete: true,
				Outputs: []native.Field{{Name: "probabilities", Data: [][]byte{[]byte(`[1]`)}}},
				Progress: native.Progress{
					RequestID: member.id, WorkloadUID: member.trial, Phase: "finished",
					BatchID: id, BatchSize: f.batch, BatchRequestIDs: ids,
					ServiceSample: &native.ServiceSample{
						ServiceDurationNanos: int64(reported), PrecedingIdleNanos: idle, TrialID: member.trial,
					},
					At: lastEnd,
					NativeHTTP: &native.HTTPRequestTiming{
						HandlerEnteredAt: started.Add(-time.Duration(f.batch-index) * time.Nanosecond),
						EnqueueNanos:     sessionNanos(1),
					},
				},
			})
			if err != nil {
				f.t.Error(err)
			}
			member.reply <- sessionFakeReply{status: http.StatusOK, body: body}
		}
	}
}

func (f *sessionFake) maxOutstanding(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maxOpen[kind]
}

func fixedService(service time.Duration) func(uint64) (time.Duration, time.Duration) {
	return func(uint64) (time.Duration, time.Duration) { return service, service }
}

func sessionTestConfig(t testing.TB, endpoint string, batch int) sessionLoadConfig {
	t.Helper()
	dataset := t.TempDir()
	record := filepath.Join(dataset, "record")
	if err := os.Mkdir(record, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(record, "image"), make([]byte, 1<<10), 0o600); err != nil {
		t.Fatal(err)
	}
	return sessionLoadConfig{
		endpoint: endpoint, model: "classifier", dataset: dataset, session: "sess", definition: "definition",
		fields: []string{"image"}, batch: batch, minSamples: 5, maxQueueRequests: 64,
		maxQueueBytes: 64 << 20, requestUpperBytes: 2 << 20, sloPercentile: 95, midRatio: .5,
		sloTarget: time.Second, lifetime: 30 * time.Second, margin: 100 * time.Millisecond,
		meterLag: 20 * time.Millisecond, powerWindow: 100 * time.Millisecond,
		sparseMeanGap: 2 * time.Millisecond, levelLimit: 5 * time.Second,
	}
}

func sessionTestArgs(c sessionLoadConfig) []string {
	return []string{
		"--native-endpoint", c.endpoint, "--model", c.model, "--fields", "image", "--dataset-dir", c.dataset,
		"--batch", strconv.Itoa(c.batch), "--session", c.session, "--definition", c.definition,
		"--slo-percentile", "95", "--slo-target", "1s", "--request-lifetime", "30s", "--delivery-margin", "100ms",
		"--min-samples", "5", "--meter-lag", "0s", "--min-power-window", "50ms", "--mid-ratio", ".5",
		"--sparse", "--sparse-mean-gap", "2ms", "--level-limit", "5s",
		"--max-queue-requests", strconv.Itoa(c.maxQueueRequests), "--max-queue-bytes", strconv.FormatInt(c.maxQueueBytes, 10),
		"--request-upper-bytes", strconv.FormatInt(c.requestUpperBytes, 10),
	}
}

func sessionTestLevel(t testing.TB, c sessionLoadConfig, kind string, rate float64) sessionLevel {
	t.Helper()
	records, err := readProfilingRecords(context.Background(), c.dataset, c.fields, c.requestUpperBytes)
	if err != nil {
		t.Fatal(err)
	}
	s := newSessionLoad(c, records)
	defer s.transport.CloseIdleConnections()
	row, err := s.level(context.Background(), kind, rate)
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestSessionLoadLevelsWaitForPowerWindowAfterSamples(t *testing.T) {
	for _, sparse := range []bool{false, true} {
		t.Run("sparse="+strconv.FormatBool(sparse), func(t *testing.T) {
			fake := newSessionFake(t, 2, fixedService(time.Millisecond), nil)
			c := sessionTestConfig(t, fake.server.URL, 2)
			c.sparse = sparse
			var stdout bytes.Buffer
			levels, err := runSessionLoad(context.Background(), c, &stdout)
			if err != nil {
				t.Fatal(err)
			}
			kinds := []string{sessionSaturation, sessionMid}
			if sparse {
				kinds = append(kinds, sessionSparse)
			}
			if len(levels) != len(kinds) {
				t.Fatalf("levels = %+v", levels)
			}
			assertSessionBatchOutput(t, stdout.String(), levels, fake)
			for index, kind := range kinds {
				level := levels[index]
				if level.Kind != kind || level.WorkloadUID != "sess/2/"+kind || level.EndReason != sessionSamplesReached ||
					level.CompletedBatches < c.minSamples || level.EndedAt.Sub(level.StartedAt) < c.meterLag+c.powerWindow ||
					level.SentInputs != level.CompletedInputs || level.CompletedInputs != level.CompletedBatches*c.batch ||
					level.PredictedSeconds <= 0 || level.PredictedSeconds > level.LimitSeconds {
					t.Fatalf("level %d = %+v", index, level)
				}
				if index > 0 && level.StartedAt.Before(levels[index-1].EndedAt) {
					t.Fatalf("level %s started before %s ended", kind, levels[index-1].Kind)
				}
			}
			if mid := levels[1]; mid.TargetInputsPerSecond != c.midRatio*levels[0].AchievedInputsPerSecond ||
				mid.AchievedInputsPerSecond <= 0 {
				t.Fatalf("mid rate = %+v, saturation = %+v", mid, levels[0])
			}
		})
	}
}

func TestSessionLevelDeadlineRetainsCompletedCounts(t *testing.T) {
	fake := newSessionFake(t, 2, func(uint64) (time.Duration, time.Duration) {
		return 10 * time.Second, time.Millisecond
	}, nil)
	config := sessionTestConfig(t, fake.server.URL, 2)
	config.minSamples, config.levelLimit = 1000, 150*time.Millisecond
	levels, _ := runSessionLoad(context.Background(), config, io.Discard)
	if len(levels) != 1 {
		t.Fatalf("level limit lost its actual outcome: %+v", levels)
	}
	row := levels[0]
	if row.CompletedBatches < 2 || row.CompletedInputs < 2*row.CompletedBatches ||
		row.PredictedSeconds <= row.LimitSeconds || row.EndReason != sessionLevelLimitReached ||
		!strings.Contains(row.Detail, "reached the 150ms level limit") {
		t.Fatalf("level limit lost completed measurements: %+v", row)
	}
}

func TestSessionLoadSaturationKeepsTwoBatchesOutstanding(t *testing.T) {
	// The failed runs: B=4, 64-request and 64 MiB limits at 2 MiB per request,
	// a first execution of 1.97 s, then 0.13 s. Their sizing rejected this supply.
	fake := newSessionFake(t, 4, func(batch uint64) (time.Duration, time.Duration) {
		if batch == 1 {
			return 1970 * time.Millisecond, 5 * time.Millisecond
		}
		return 130 * time.Millisecond, 5 * time.Millisecond
	}, nil)
	c := sessionTestConfig(t, fake.server.URL, 4)
	c.minSamples, c.meterLag, c.levelLimit = 20, 0, time.Minute
	row := sessionTestLevel(t, c, sessionSaturation, 0)
	if row.Outstanding != 8 || row.EndReason != sessionSamplesReached {
		t.Fatalf("saturation = %+v", row)
	}
	if got := fake.maxOutstanding(sessionSaturation); got != 8 {
		t.Fatalf("native saw %d outstanding saturation requests, want 2B=8", got)
	}
}

func TestSessionLoadSparseNeverOverlapsSlowBatches(t *testing.T) {
	fake := newSessionFake(t, 2, fixedService(30*time.Millisecond), nil)
	c := sessionTestConfig(t, fake.server.URL, 2)
	c.minSamples, c.meterLag, c.powerWindow, c.sparseMeanGap = 4, 0, 50*time.Millisecond, time.Millisecond
	row := sessionTestLevel(t, c, sessionSparse, 0)
	if row.EndReason != sessionSamplesReached || row.CompletedBatches < c.minSamples {
		t.Fatalf("sparse = %+v", row)
	}
	if got := fake.maxOutstanding(sessionSparse); got != c.batch {
		t.Fatalf("native saw %d outstanding sparse requests, want one batch of %d", got, c.batch)
	}
}

func TestSessionLoadSparseBatchBeyondLifetimeEndsRun(t *testing.T) {
	const body = "{\"error\":\"context deadline exceeded\",\"executionComplete\":false,\"code\":\"RequestTimeout\"}\n"
	fake := newSessionFake(t, 2, fixedService(time.Millisecond), func(kind string, ordinal int) (int, string) {
		if kind == sessionSparse && ordinal == 3 {
			return http.StatusRequestTimeout, body
		}
		return 0, ""
	})
	c := sessionTestConfig(t, fake.server.URL, 2)
	c.minSamples, c.sparse = 50, true
	levels, err := runSessionLoad(context.Background(), c, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	sparse := levels[len(levels)-1]
	if len(levels) != 3 || sparse.EndReason != sessionRequestLifetimeExpired || sparse.CompletedBatches != 2 ||
		sparse.Detail != "native inference HTTP 408: "+strings.TrimSpace(body) {
		t.Fatalf("sparse = %+v", sparse)
	}
}

func TestSessionLoadAllocationFailureSaysWhetherABatchCompleted(t *testing.T) {
	const body = `{"error":"Failed to allocate memory","failureReason":"MemoryAllocationFailed","executionComplete":true}`
	for _, test := range []struct {
		ordinal int
		want    string
	}{
		{1, sessionAllocationBeforeBatch},
		{3, sessionAllocationAfterBatch},
	} {
		t.Run(test.want, func(t *testing.T) {
			fake := newSessionFake(t, 1, fixedService(time.Millisecond), func(kind string, ordinal int) (int, string) {
				if kind == sessionSaturation && ordinal == test.ordinal {
					return http.StatusInternalServerError, body
				}
				return 0, ""
			})
			levels, err := runSessionLoad(context.Background(), sessionTestConfig(t, fake.server.URL, 1), io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if len(levels) != 1 || levels[0].EndReason != test.want ||
				levels[0].Detail != "native inference HTTP 500: "+body {
				t.Fatalf("levels = %+v", levels)
			}
		})
	}
}

func TestSessionLoadCommandLeadsWithNativeFailureWithinBound(t *testing.T) {
	for _, test := range []struct {
		name, body, want string
		table            bool
	}{
		{"trimmed first line", "\n  {\"error\":\"overloaded\"}\n{\"second\":1}\n",
			`native inference HTTP 503: {"error":"overloaded"}`, true},
		{"long body", `{"error":"` + strings.Repeat("x", 6000) + `"}`, `native inference HTTP 503: {"error":"xxx`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := newSessionFake(t, 1, fixedService(time.Millisecond), func(kind string, ordinal int) (int, string) {
				if ordinal == 2 {
					return http.StatusServiceUnavailable, test.body
				}
				return 0, ""
			})
			termination := filepath.Join(t.TempDir(), "termination-log")
			var stdout bytes.Buffer
			err := sessionLoadCommand(sessionTestArgs(sessionTestConfig(t, fake.server.URL, 1)), termination, &stdout)
			message, readErr := os.ReadFile(termination)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var report struct {
				Error  string         `json:"error"`
				Levels []sessionLevel `json:"levels"`
			}
			if err == nil || !strings.HasPrefix(err.Error(), test.want) || len(message) > sessionTerminationBytes ||
				!bytes.HasPrefix(message, []byte(`{"error":"native inference HTTP 503: `)) ||
				!strings.HasPrefix(stdout.String(), profilederivation.SessionBatchRowPrefix) ||
				json.Unmarshal(message, &report) != nil ||
				!strings.HasPrefix(report.Error, test.want) || len(report.Error) > sessionErrorBytes {
				t.Fatalf("error %v, termination message %d bytes: %s", err, len(message), message)
			}
			if test.table && (report.Error != test.want || len(report.Levels) != 1 ||
				report.Levels[0].EndReason != sessionFailed) {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestSessionTerminationMessageStaysBounded(t *testing.T) {
	levels := make([]sessionLevel, 3)
	for index := range levels {
		levels[index] = sessionLevel{
			Kind: sessionSaturation, WorkloadUID: strings.Repeat("s", 256),
			EndReason: sessionFailed, Detail: sessionBounded(strings.Repeat("\"", 4096), sessionDetailBytes),
		}
	}
	for _, failure := range []string{"", strings.Repeat("\x01", 4096), strings.Repeat("é", 4096)} {
		var err error
		if failure != "" {
			err = &sessionNativeError{message: failure}
		}
		message, encodeErr := sessionTerminationMessage(levels, err)
		var report struct {
			Levels []sessionLevel `json:"levels"`
		}
		if encodeErr != nil || len(message) > sessionTerminationBytes || json.Unmarshal(message, &report) != nil {
			t.Fatalf("termination message %d bytes, %v", len(message), encodeErr)
		}
		if len(report.Levels) != len(levels) {
			t.Fatalf("level table dropped before details: %s", message)
		}
	}
}

func TestSessionCounterRetainsFixedStateOverRequests(t *testing.T) {
	counter := newSessionCounter()
	idle := int64(1)
	for id := uint64(1); counter.batches < 400_000; id += 2 {
		first := sessionProgress{BatchID: id, BatchSize: 4, At: time.Unix(100, 0),
			NativeHTTP:    &native.HTTPRequestTiming{HandlerEnteredAt: time.Unix(99, 0), EnqueueNanos: &idle},
			ServiceSample: &native.ServiceSample{ServiceDurationNanos: 1e6, PrecedingIdleNanos: &idle}}
		second := first
		second.BatchID++
		// Two batches answer interleaved, as outstanding batches do.
		for range 4 {
			if _, _, err := counter.add(first); err != nil {
				t.Fatal(err)
			}
			if _, _, err := counter.add(second); err != nil {
				t.Fatal(err)
			}
		}
		if len(counter.open) != 0 {
			t.Fatalf("%d completed batches still retained after batch %d", len(counter.open), id)
		}
	}
	if counter.inputs != 1_600_000 || counter.samples != 400_000 {
		t.Fatalf("counter = %+v", counter)
	}
}

func TestSessionLoadMidLifetimeExpiryContinuesToSparse(t *testing.T) {
	const body = `{"error":"request lifetime expired"}`
	fake := newSessionFake(t, 1, fixedService(time.Millisecond), func(kind string, _ int) (int, string) {
		if kind == sessionMid {
			return http.StatusRequestTimeout, body
		}
		return 0, ""
	})
	config := sessionTestConfig(t, fake.server.URL, 1)
	config.sparse = true
	levels, err := runSessionLoad(context.Background(), config, io.Discard)
	if err != nil || len(levels) != 3 || levels[1].EndReason != "RequestLifetimeExpired" ||
		levels[1].Detail != "native inference HTTP 408: "+body || levels[2].EndReason != sessionSamplesReached {
		t.Fatalf("levels %+v error %v", levels, err)
	}
}

func TestSessionLoadMidExpiryWaitsForPhysicalCompletion(t *testing.T) {
	fake := newSessionFake(t, 1, fixedService(5*time.Millisecond), func(kind string, ordinal int) (int, string) {
		if kind == sessionMid && ordinal == 15 {
			return http.StatusRequestTimeout, `{"error":"request lifetime expired"}`
		}
		return 0, ""
	})
	config := sessionTestConfig(t, fake.server.URL, 1)
	config.sparse, config.midRatio, config.minSamples = true, .99, 100
	levels, err := runSessionLoad(context.Background(), config, io.Discard)
	if err != nil || len(levels) != 3 {
		t.Fatalf("levels %+v error %v", levels, err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	mid := levels[1]
	if mid.EndReason != sessionRequestLifetimeExpired || mid.CompletedBatches+1 != fake.finished[sessionMid] ||
		mid.SentInputs != fake.finished[sessionMid] || fake.busyStarts[sessionSparse] ||
		levels[2].CompletedBatches != fake.finished[sessionSparse] {
		t.Fatalf("mid %+v native finished %v busy starts %v", mid, fake.finished, fake.busyStarts)
	}
	input := profilederivation.SessionInput{
		Settings: profilederivation.SessionSettings{MeterLagSeconds: config.meterLag.Seconds(),
			MinimumPowerWindowSeconds: config.powerWindow.Seconds(), PostLoadWindowSeconds: 1,
			MinimumSamples: config.minSamples, ConfidenceLevel: .95},
		MeterSource: json.RawMessage(`{"kind":"fixture"}`), MaxResultBytes: 64 << 20,
		PostLoadStart: levels[2].EndedAt, PostLoadEnd: levels[2].EndedAt.Add(time.Second),
	}
	batch := profilederivation.SessionBatchInput{Batch: 1}
	for _, level := range levels {
		row := profilederivation.DriverLevelRow{ID: level.WorkloadUID, Kind: level.Kind,
			Start: level.StartedAt, End: level.EndedAt, CompletedInputs: level.CompletedInputs,
			CompletedBatches: level.CompletedBatches, EndReason: level.EndReason, Detail: level.Detail,
			Excluded: level.EndReason == sessionRequestLifetimeExpired}
		batch.Levels = append(batch.Levels, profilederivation.SessionLevelInput{
			Row: row, Native: fake.nativeRows[level.Kind],
		})
	}
	input.Batches = []profilederivation.SessionBatchInput{batch}
	for at := levels[0].StartedAt; !at.After(input.PostLoadEnd); at = at.Add(time.Millisecond) {
		watts := 100.0
		if !at.Before(input.PostLoadStart) {
			watts = 10
		}
		input.Power = append(input.Power, profilederivation.Sample{ObservedAt: at, Watts: watts})
	}
	result, err := profilederivation.DeriveSession(input)
	if err != nil {
		t.Fatal(err)
	}
	stored := result.Batches[0].Levels[1]
	if !stored.Excluded || stored.Detail != mid.Detail || stored.MeanWatts != nil ||
		result.Batches[0].Levels[2].CompletedBatches != len(fake.nativeRows[sessionSparse]) {
		t.Fatalf("stored levels %+v", result.Batches[0].Levels)
	}
}

func TestSessionLoadExpiryIdleWaitIsBounded(t *testing.T) {
	fake := newSessionFake(t, 1, fixedService(time.Millisecond), func(kind string, _ int) (int, string) {
		if kind == sessionMid {
			return http.StatusRequestTimeout, `{"error":"request lifetime expired"}`
		}
		return 0, ""
	})
	fake.mu.Lock()
	fake.neverIdle = true
	fake.mu.Unlock()
	config := sessionTestConfig(t, fake.server.URL, 1)
	config.sparse, config.levelLimit = true, 200*time.Millisecond
	levels, err := runSessionLoad(context.Background(), config, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "did not become idle within 200ms") ||
		!strings.Contains(err.Error(), "native inference HTTP 408") || len(levels) != 2 {
		t.Fatalf("levels %+v error %v", levels, err)
	}
}

func sessionNanos(n int64) *int64 { return &n }

func assertSessionBatchOutput(t testing.TB, stdout string, levels []sessionLevel, fake *sessionFake) {
	t.Helper()
	byLevel := map[string][]profilederivation.NativeBatchRow{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		raw, found := strings.CutPrefix(line, profilederivation.SessionBatchRowPrefix)
		var record profilederivation.SessionBatchRow
		if !found || json.Unmarshal([]byte(raw), &record) != nil {
			t.Fatalf("invalid batch line %q", line)
		}
		byLevel[record.WorkloadUID] = append(byLevel[record.WorkloadUID], record.NativeRow())
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, level := range levels {
		rows := byLevel[level.WorkloadUID]
		expected := fake.nativeRows[level.Kind]
		if len(rows) != level.CompletedBatches || len(rows) != len(expected) {
			t.Fatalf("%s emitted %d batches, table %d, executor %d",
				level.Kind, len(rows), level.CompletedBatches, len(expected))
		}
		for index, row := range rows {
			want := expected[index]
			sameIdle := (row.PrecedingIdleNanos == nil) == (want.PrecedingIdleNanos == nil) &&
				(row.PrecedingIdleNanos == nil || *row.PrecedingIdleNanos == *want.PrecedingIdleNanos)
			if row.BatchID != want.BatchID || row.BatchSize != want.BatchSize || !row.FinishedAt.Equal(want.FinishedAt) ||
				row.ServiceDurationNanos != want.ServiceDurationNanos || !row.StartedAt.Equal(want.StartedAt) ||
				!sameIdle || !row.LatestEnqueuedAt.Equal(want.LatestEnqueuedAt) {
				t.Fatalf("emitted batch %+v, executor %+v", row, want)
			}
		}
	}
}

func TestSessionCounterRejectsConflictingBatchTiming(t *testing.T) {
	base := sessionProgress{BatchID: 1, BatchSize: 2, At: time.Unix(100, 0),
		ServiceSample: &native.ServiceSample{ServiceDurationNanos: 100, PrecedingIdleNanos: sessionNanos(5)},
		NativeHTTP:    &native.HTTPRequestTiming{HandlerEnteredAt: time.Unix(99, 0), EnqueueNanos: sessionNanos(1)}}
	for _, change := range []string{"size", "finish", "service", "idle presence", "idle value"} {
		t.Run(change, func(t *testing.T) {
			counter := newSessionCounter()
			if _, completed, err := counter.add(base); completed || err != nil {
				t.Fatalf("first member completed %v: %v", completed, err)
			}
			next := base
			sample := *base.ServiceSample
			next.ServiceSample = &sample
			switch change {
			case "size":
				next.BatchSize++
			case "finish":
				next.At = next.At.Add(time.Nanosecond)
			case "service":
				sample.ServiceDurationNanos++
			case "idle presence":
				sample.PrecedingIdleNanos = nil
			case "idle value":
				sample.PrecedingIdleNanos = sessionNanos(6)
			}
			if _, completed, err := counter.add(next); completed || err == nil ||
				err.Error() != "members of native batch 1 report different batch timing" {
				t.Fatalf("conflict completed %v: %v", completed, err)
			}
		})
	}
}

type sessionFailWriter struct{ err error }

func (w sessionFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestSessionLoadStdoutFailureFailsJob(t *testing.T) {
	fake := newSessionFake(t, 2, fixedService(time.Millisecond), nil)
	sentence := errors.New("stdout pipe closed")
	config := sessionTestConfig(t, fake.server.URL, 2)
	levels, err := runSessionLoad(context.Background(), config, sessionFailWriter{sentence})
	if !errors.Is(err, sentence) || len(levels) != 1 ||
		levels[0].EndReason != sessionFailed || levels[0].Detail != sentence.Error() {
		t.Fatalf("stdout failure lost: levels %+v error %v", levels, err)
	}
}

// The real actor, HTTP handler and ORT runner provide every batch row through
// the load driver's response path. No instance Pod log is read.
func TestSessionMeasurementRealNative(t *testing.T) {
	command := fixtures.RealFirstfloorCommand(t)
	root, directory := command[2], command[3]
	const batch = 2
	config := native.Config{Mode: "profiling", Backend: "ort-cpu", Model: "classifier", Inputs: []string{"image"},
		Outputs: []string{"label"}, BatchSizes: []int{batch}, MaxBatchSize: batch, WarmupIterations: 2,
		MaxQueueRequests: 64, MaxQueueBytes: 64 << 20, MaxRequestBytes: 4 << 20, RunnerTimeoutMS: 60_000,
		Policy: native.Policy{FixedBatchSize: batch}}
	config.BuildRunnerCommand()
	relocate := map[string]string{
		"/opt/chill/runtime/firstfloor/runner.py": filepath.Join(root, "runtime/firstfloor/runner.py"),
		"/model":        filepath.Join(directory, "model"),
		"/preprocessor": filepath.Join(directory, "preprocessor"),
	}
	for index, argument := range config.RunnerCommand {
		if path, ok := relocate[argument]; ok {
			config.RunnerCommand[index] = path
		}
	}
	if err := native.ValidateConfig(&config); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	runner, err := native.StartRunner(ctx, &config)
	if err != nil {
		t.Fatal(err)
	}
	scheduler := native.NewScheduler(ctx, &config, runner)
	defer func() {
		cancel()
		<-scheduler.Done()
	}()
	server := httptest.NewServer(native.NewHandler(scheduler))
	defer server.Close()
	for !scheduler.Ready() {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	load := sessionTestConfig(t, server.URL, batch)
	load.dataset = filepath.Join(directory, "dataset")
	var stdout bytes.Buffer
	termination := filepath.Join(t.TempDir(), "termination-log")
	if err := sessionLoadCommand(sessionTestArgs(load), termination, &stdout); err != nil {
		t.Fatal(err)
	}
	message, err := os.ReadFile(termination)
	if err != nil {
		t.Fatal(err)
	}
	var table struct {
		Levels []sessionLevel `json:"levels"`
	}
	if err := json.Unmarshal(message, &table); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
		raw, ok := strings.CutPrefix(line, profilederivation.SessionBatchRowPrefix)
		var record profilederivation.SessionBatchRow
		if !ok || json.Unmarshal([]byte(raw), &record) != nil {
			t.Fatalf("invalid batch line %q", line)
		}
		row := record.NativeRow()
		if row.BatchID == 0 || row.BatchSize != batch || row.FinishedAt.IsZero() || row.ServiceDurationNanos <= 0 ||
			!row.StartedAt.Equal(row.FinishedAt.Add(-time.Duration(row.ServiceDurationNanos))) ||
			row.LatestEnqueuedAt.IsZero() || row.PrecedingIdleNanos == nil || *row.PrecedingIdleNanos < 0 {
			t.Fatalf("real executor batch lacks timing: %+v", row)
		}
		counts[record.WorkloadUID]++
	}
	if len(table.Levels) != 3 || len(counts) != len(table.Levels) {
		t.Fatalf("driver levels %+v emitted counts %v", table.Levels, counts)
	}
	for _, level := range table.Levels {
		if level.EndReason != sessionSamplesReached || counts[level.WorkloadUID] != level.CompletedBatches ||
			level.CompletedInputs != level.CompletedBatches*batch {
			t.Fatalf("real driver level %+v emitted %d batches", level, counts[level.WorkloadUID])
		}
	}
}

func TestSessionLoadUnexpectedBatchSizeFailsWithBatchTimingSentence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := time.Now().UTC()
		response := native.Response{Progress: native.Progress{BatchID: 1, BatchSize: 3, At: now,
			ServiceSample: &native.ServiceSample{ServiceDurationNanos: 1},
			NativeHTTP:    &native.HTTPRequestTiming{HandlerEnteredAt: now.Add(-time.Second), EnqueueNanos: sessionNanos(1)}}}
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	config := sessionTestConfig(t, server.URL, 2)
	levels, err := runSessionLoad(context.Background(), config, io.Discard)
	const sentence = "members of native batch 1 report different batch timing"
	if err == nil || err.Error() != sentence || len(levels) != 1 ||
		levels[0].EndReason != sessionFailed || levels[0].Detail != sentence || levels[0].CompletedBatches != 0 {
		t.Fatalf("batch disagreement lost: levels %+v error %v", levels, err)
	}
}
