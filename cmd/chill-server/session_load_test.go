package main

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
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
)

// sessionFake is a fixed-batch native executor: it answers once B requests
// are queued, reports the given service time and records per-level
// outstanding requests. A failure answers every member of that batch.
type sessionFake struct {
	t       testing.TB
	batch   int
	service func(batch uint64) (reported, sleep time.Duration)
	failure func(kind string, ordinal int) (int, string)
	calls   chan sessionFakeCall
	done    chan struct{}
	mu      sync.Mutex
	open    map[string]int
	maxOpen map[string]int
	server  *httptest.Server
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
		done: make(chan struct{}), open: map[string]int{}, maxOpen: map[string]int{},
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
		for index, member := range members {
			if member.kind != kind {
				f.t.Errorf("native batch mixes levels %s and %s", kind, member.kind)
			}
			ids[index] = member.id
		}
		ordinals[kind]++
		reported, sleep := f.service(id)
		started := time.Now()
		time.Sleep(sleep)
		var idle *int64
		if !lastEnd.IsZero() {
			gap := started.Sub(lastEnd).Nanoseconds()
			idle = &gap
		}
		lastEnd = time.Now()
		for _, member := range members {
			if f.failure != nil {
				if status, body := f.failure(kind, ordinals[kind]); status != 0 {
					member.reply <- sessionFakeReply{status: status, body: []byte(body)}
					continue
				}
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
			levels, err := runSessionLoad(context.Background(), c)
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

func TestSessionLoadPredictedDurationEndsLevel(t *testing.T) {
	for _, test := range []struct {
		kind    string
		service time.Duration
		change  func(*sessionLoadConfig)
		batches int
	}{
		{sessionSaturation, 60 * time.Millisecond, func(c *sessionLoadConfig) { c.minSamples = 1000 }, 1},
		{sessionMid, time.Millisecond, func(c *sessionLoadConfig) { c.midRatio = 1e-4 }, 0},
		{sessionSparse, time.Millisecond, func(c *sessionLoadConfig) { c.sparseMeanGap = 10 * time.Second }, 1},
	} {
		t.Run(test.kind, func(t *testing.T) {
			fake := newSessionFake(t, 2, fixedService(test.service), nil)
			c := sessionTestConfig(t, fake.server.URL, 2)
			c.sparse, c.levelLimit = true, time.Second
			test.change(&c)
			levels, err := runSessionLoad(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			last := levels[len(levels)-1]
			if last.Kind != test.kind || last.EndReason != sessionPredictedOverLimit ||
				last.CompletedBatches != test.batches || last.LimitSeconds != 1 || last.PredictedSeconds <= 1 ||
				last.EndedAt.Sub(last.StartedAt) >= c.levelLimit ||
				!strings.Contains(last.Detail, "exceeds limit 1.000s after "+strconv.Itoa(test.batches)+" completed batches") {
				t.Fatalf("levels = %+v", levels)
			}
		})
	}
}

func TestParseSessionLoadRejectsWhatNativeCannotAdmit(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*sessionLoadConfig)
		want   string
	}{
		{"valid", func(*sessionLoadConfig) {}, ""},
		{"two batches over request limit", func(c *sessionLoadConfig) { c.maxQueueRequests = 7 },
			"saturation keeps 8 requests (two batches of 4) outstanding"},
		{"two batches over byte limit", func(c *sessionLoadConfig) { c.maxQueueBytes = 7 * c.requestUpperBytes },
			"saturation keeps 8 requests (two batches of 4) outstanding"},
		{"session with space", func(c *sessionLoadConfig) { c.session = "a b" }, "native workload identifier"},
		{"identifier over 256 bytes", func(c *sessionLoadConfig) { c.session = strings.Repeat("s", 245) },
			"native workload identifier"},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := sessionTestConfig(t, "http://native", 4)
			test.change(&c)
			_, err := parseSessionLoad(sessionTestArgs(c))
			if test.want == "" && err != nil || test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
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
	if row.Outstanding != 8 || row.MaxBatchServiceSeconds != .13 || row.EndReason != sessionSamplesReached {
		t.Fatalf("saturation = %+v", row)
	}
	if got := fake.maxOutstanding(sessionSaturation); got != 8 {
		t.Fatalf("native saw %d outstanding saturation requests, want 2B=8", got)
	}
}

func TestSessionLoadFirstSaturationBatchBeyondLifetimeEndsRun(t *testing.T) {
	// Two 15 s batches plus the 0.1 s margin exceed the 30 s lifetime.
	fake := newSessionFake(t, 4, func(uint64) (time.Duration, time.Duration) {
		return 15 * time.Second, 5 * time.Millisecond
	}, nil)
	c := sessionTestConfig(t, fake.server.URL, 4)
	c.sparse = true
	levels, err := runSessionLoad(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if len(levels) != 1 || levels[0].EndReason != sessionServiceOverLifetime || levels[0].CompletedBatches != 1 ||
		levels[0].Detail != "batch service 15s: two batches plus delivery margin 100ms exceed request lifetime 30s" {
		t.Fatalf("levels = %+v", levels)
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
	levels, err := runSessionLoad(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	sparse := levels[len(levels)-1]
	if len(levels) != 3 || sparse.EndReason != sessionServiceOverLifetime || sparse.CompletedBatches != 2 ||
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
			levels, err := runSessionLoad(context.Background(), sessionTestConfig(t, fake.server.URL, 1))
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
				stdout.String() != string(message)+"\n" || json.Unmarshal(message, &report) != nil ||
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
			Kind: sessionSaturation, WorkloadUID: strings.Repeat("s", sessionIdentifierBytes),
			EndReason: sessionPredictedOverLimit, Detail: sessionBounded(strings.Repeat("\"", 4096), sessionDetailBytes),
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
		if failure == "" && len(report.Levels) != len(levels) {
			t.Fatalf("level table dropped before details: %s", message)
		}
	}
}

func TestSessionCounterRetainsFixedStateOverRequests(t *testing.T) {
	counter := newSessionCounter()
	rng := rand.New(rand.NewPCG(1, 2))
	retained := func(batches int) (int, int) {
		for id := uint64(counter.batches + 1); counter.batches < batches; id += 2 {
			idle := int64(1)
			first := sessionProgress{BatchID: id, BatchSize: 4, ServiceSample: &native.ServiceSample{
				ServiceDurationNanos: 1e6 + rng.Int64N(1e8), PrecedingIdleNanos: &idle}}
			second := first
			second.BatchID++
			// Two batches answer interleaved, as outstanding batches do.
			for range 4 {
				counter.add(first)
				counter.add(second)
			}
		}
		return len(counter.open), len(counter.service.Samples())
	}
	openSmall, samplesSmall := retained(20_000)
	openLarge, samplesLarge := retained(400_000)
	if counter.inputs != 1_600_000 || openSmall != 0 || openLarge != 0 || samplesLarge > 2*samplesSmall {
		t.Fatalf("inputs %d: open %d then %d batches, median summary %d then %d samples",
			counter.inputs, openSmall, openLarge, samplesSmall, samplesLarge)
	}
	t.Logf("median summary retains %d then %d samples", samplesSmall, samplesLarge)
}
