package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func profilingProofPair(id string) (profilingrun.PhaseRequest, native.Response) {
	start := time.Date(2026, 1, 1, 0, 0, 1, 0, time.UTC)
	finish := start.Add(200 * time.Microsecond)
	zero, enqueue, done := int64(0), int64(100000), int64(700000)
	p := native.Progress{RequestID: id, WorkloadUID: "pod", Definition: "definition", Phase: "finished", At: finish, BatchID: 7, BatchSize: 2, BatchRequestIDs: []string{"right", "left"}, BatchStartedAt: &start, BatchFinishedAt: &finish, ServiceSample: &native.ServiceSample{ServiceDurationNanos: 200000, PrecedingIdleNanos: &zero, TrialID: "pod"}, BackendMS: .2, ServiceMS: .2, Units: 1, NativeElapsedMS: .7, ExecutionComplete: true, NativeHTTP: &native.HTTPRequestTiming{HandlerEnteredAt: start.Add(-500 * time.Microsecond), ReceivedNanos: &zero, EnqueueNanos: &enqueue, FinishedNanos: &done}}
	row := profilingrun.PhaseRequest{RequestID: id, RecordID: "record-" + id, ArrivalAt: start.Add(-time.Millisecond), SentAt: start.Add(-time.Millisecond), CompletedAt: start.Add(time.Millisecond), LatencyMilliseconds: 2}
	return row, native.Response{ID: id, ModelName: "classifier", Outputs: []native.Field{{Name: "class", Data: [][]byte{{3}}}}, Progress: p, ExecutionComplete: true}
}

func TestProfilingLoadCompactMembershipKeepsNativeOrder(t *testing.T) {
	plan := profilingTestPlan("http://native")
	plan.Batch = 2
	collector := profilingPhaseCollector{plan: plan, groups: make(map[uint64]native.Progress), requests: make(map[string]int)}
	for _, id := range []string{"left", "right"} {
		row, response := profilingProofPair(id)
		if _, err := collector.add(row, response); err != nil {
			t.Fatal(err)
		}
	}
	rows, samples, latencies, err := collector.finish()
	if err != nil || len(rows) != 2 || len(samples) != 1 || len(latencies) != 1 || strings.Join(samples[0].RequestIDs, ",") != "right,left" || strings.Join(samples[0].RecordIDs, ",") != "record-right,record-left" {
		t.Fatalf("compact evidence lost native membership order: samples=%+v err=%v", samples, err)
	}
	_, original := profilingProofPair("left")
	if samples[0].ServiceDurationNanos != 200000 || samples[0].StartedAt != *original.Progress.BatchStartedAt {
		t.Fatalf("native batch proof changed: %+v", samples[0])
	}
}

func TestProfilingLoadRetainsActualSendOrder(t *testing.T) {
	plan := profilingTestPlan("http://native")
	plan.Batch = 2
	collector := profilingPhaseCollector{plan: plan, groups: make(map[uint64]native.Progress), requests: make(map[string]int)}
	for _, id := range []string{"left", "right"} {
		row, response := profilingProofPair(id)
		if id == "left" {
			row.SentAt = row.SentAt.Add(100 * time.Microsecond)
		} else {
			row.ArrivalAt = row.ArrivalAt.Add(50 * time.Microsecond)
			row.SentAt = row.ArrivalAt
		}
		if _, err := collector.add(row, response); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, _, err := collector.finish()
	if err != nil || len(rows) != 2 || rows[0].RequestID != "right" || rows[1].RequestID != "left" || !rows[0].ArrivalAt.After(rows[1].ArrivalAt) {
		t.Fatalf("diagnostic schedule overrode actual send order: rows=%+v err=%v", rows, err)
	}
}

func TestProfilingLoadValidatesFullProofBeforeCompaction(t *testing.T) {
	for _, corrupt := range []string{"membership", "timing", "identity", "disagreement", "incomplete"} {
		t.Run(corrupt, func(t *testing.T) {
			plan := profilingTestPlan("http://native")
			plan.Batch = 2
			collector := profilingPhaseCollector{plan: plan, groups: make(map[uint64]native.Progress), requests: make(map[string]int)}
			row, response := profilingProofPair("left")
			switch corrupt {
			case "membership":
				response.Progress.BatchRequestIDs = []string{"left", "left"}
			case "timing":
				response.Progress.BackendMS = 10
			case "identity":
				response.Progress.WorkloadUID = "other-pod"
			}
			_, err := collector.add(row, response)
			if corrupt == "incomplete" {
				if err == nil {
					_, _, _, err = collector.finish()
				}
			} else if corrupt == "disagreement" && err == nil {
				row, response = profilingProofPair("right")
				response.Progress.ServiceSample.PrecedingIdleNanos = nil
				_, err = collector.add(row, response)
			}
			if err == nil {
				t.Fatal("accepted invalid full native proof")
			}
		})
	}
}

func TestProfilingLoadUploadsFirstNativeAllocationFailure(t *testing.T) {
	nativeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(500)
		_ = json.NewEncoder(w).Encode(native.ErrorResponse{Error: "BFCArena allocation failed", FailureReason: "MemoryAllocationFailed", ExecutionComplete: true})
	}))
	defer nativeServer.Close()
	plan := profilingTestPlan(nativeServer.URL)
	dataset := t.TempDir()
	if err := os.Mkdir(filepath.Join(dataset, "record"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataset, "record", "image"), make([]byte, 16<<10), 0600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var uploaded profilingLoadFailure
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/measurement/load":
			_ = json.NewEncoder(w).Encode(plan)
		case "/measurement/phase":
			_ = json.NewEncoder(w).Encode(map[string]string{"at": time.Now().UTC().Format(time.RFC3339Nano)})
		case "/measurement/load-result":
			mu.Lock()
			defer mu.Unlock()
			if err := json.NewDecoder(r.Body).Decode(&uploaded); err != nil {
				t.Error(err)
			}
			_, _ = io.WriteString(w, `{}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer control.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := profilingLoad(ctx, control.URL, dataset, io.Discard); err == nil {
		t.Fatal("allocation failure succeeded")
	}
	mu.Lock()
	defer mu.Unlock()
	if uploaded.Status != "Failed" || uploaded.FailureReason != "MemoryAllocationFailed" || uploaded.Message == "" || uploaded.IssueEvidence == nil || uploaded.IssueEvidence.DriverGC == nil || uploaded.IssueEvidence.ArrivalSeed != plan.PodUID+"/sparse" || uploaded.IssueEvidence.SentInputs < 1 {
		t.Fatalf("first native allocation failure or driver evidence not uploaded: %+v", uploaded)
	}
}

func TestProfilingLoadDeadlineStopsSchedulerBeforeNextArrival(t *testing.T) {
	driver := newProfilingLoadDriver(profilingTestPlan("http://127.0.0.1:1"), profilingTestRecords(), "", io.Discard)
	defer driver.close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	phase, err := driver.runPhase(ctx, profilingrun.Phase{ID: "sparse", Kind: "sparse", ArrivalRatePerSecond: .000001})
	if err == nil || time.Since(start) > time.Second || phase.IssueLag.Count != 0 || phase.ScheduledInputs != 0 || phase.SentInputs != 0 || phase.DriverGC == nil {
		t.Fatalf("absolute deadline did not interrupt independent scheduler wait: %+v %v", phase, err)
	}
}

func TestProfilingLoadCommandRequiresAbsoluteDeadline(t *testing.T) {
	for _, args := range [][]string{
		{"--control-endpoint=http://127.0.0.1:1", "--dataset-dir=/dataset"},
		{"--control-endpoint=http://127.0.0.1:1", "--dataset-dir=/dataset", "--deadline=invalid"},
		{"--control-endpoint=http://127.0.0.1:1", "--dataset-dir=/dataset", "--deadline=2000-01-01T00:00:00Z"},
	} {
		if err := profilingLoadCommand(args); err == nil {
			t.Fatalf("accepted missing/invalid absolute deadline: %v", args)
		}
	}
}

type profilingControlTransport func(*http.Request) (*http.Response, error)

func (f profilingControlTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProfilingLoadReadinessLongPollKeepsRootDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for _, path := range []string{"/measurement/load", "/measurement/phase"} {
		method := http.MethodPost
		if path == "/measurement/load" {
			method = http.MethodGet
		}
		client := &http.Client{Transport: profilingControlTransport(func(r *http.Request) (*http.Response, error) {
			deadline, exists := r.Context().Deadline()
			remaining := time.Until(deadline)
			if !exists || path == "/measurement/load" && remaining < 45*time.Second || path != "/measurement/load" && remaining > 30*time.Second {
				t.Errorf("incorrect control deadline for %s: %v", path, remaining)
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})}
		if err := profilingControlCall(ctx, client, "http://control", method, path, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
}
