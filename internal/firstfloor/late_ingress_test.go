package firstfloor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"
)

// These gates delay only test transport/runner I/O. No native cancellation,
// admission, decoding or execution behavior is replaced.
type ingressGate struct {
	once sync.Once
	ch   chan struct{}
}

func newIngressGate() *ingressGate { return &ingressGate{ch: make(chan struct{})} }
func (g *ingressGate) release()    { g.once.Do(func() { close(g.ch) }) }
func waitIngress(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(4 * time.Second):
		t.Fatal("native TCP ordering gate did not complete")
	}
}

type bufferedIngressConn struct {
	net.Conn
	wireBytes int
	buffered  chan struct{}
	decode    *ingressGate
	eof       *ingressGate
	pending   []byte
	first     bool
}

func (c *bufferedIngressConn) Read(p []byte) (int, error) {
	if !c.first {
		c.first = true
		c.pending = make([]byte, c.wireBytes)
		if _, err := io.ReadFull(c.Conn, c.pending); err != nil {
			return 0, err
		}
		close(c.buffered)
		<-c.decode.ch
	}
	if len(c.pending) != 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		return n, nil
	}
	// The complete Content-Length body is already available to net/http;
	// defer its background socket EOF detection independently of decoding.
	<-c.eof.ch
	return c.Conn.Read(p)
}

type ingressListener struct {
	net.Listener
	wireBytes int
	buffered  chan struct{}
	decode    *ingressGate
	eof       *ingressGate
	first     bool
}

func (l *ingressListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil || l.first {
		return c, err
	}
	l.first = true
	return &bufferedIngressConn{Conn: c, wireBytes: l.wireBytes, buffered: l.buffered, decode: l.decode, eof: l.eof}, nil
}

type ingressBody struct {
	io.ReadCloser
	remaining int64
	read      chan struct{}
	proceed   *ingressGate
}

func (b *ingressBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	if b.remaining == 0 {
		// Inner body EOF has started net/http's close detector, but the JSON
		// decoder has not yet received these final buffered bytes.
		b.remaining = -1
		close(b.read)
		<-b.proceed.ch
	}
	return n, err
}

type ingressRunnerInput struct {
	io.WriteCloser
	proceed *ingressGate
}

func (w ingressRunnerInput) Write(p []byte) (int, error) {
	<-w.proceed.ch
	return w.WriteCloser.Write(p)
}

func nativeIngressRequest(id string) Request {
	return Request{ID: id, Context: RequestContext{RequestID: id, WorkloadUID: id, Definition: id + "/1", ReceivedAt: time.Now().UTC(), SLO: SLO{Percentile: 95, TargetMS: 10000}}, Inputs: rawInputs("sample")}
}

// Queued cancellation and retained active occupancy are also covered by
// TestLocalActorCancellationSnapshotsAndTypedOutputs; deferred cancellation by
// TestDeferredQueueSharesBoundsCancellationAndState. Here the real TCP close,
// complete buffered body and independent HTTP State reader establish the two
// pre-admission orderings those actor-only tests cannot prove.
func TestNativeTCPBufferedLateIngressCancellationOrdering(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		name := "admission-before-cancel"
		if cancelFirst {
			name = "cancel-before-admission"
		}
		t.Run(name, func(t *testing.T) {
			progressFile, err := os.CreateTemp(t.TempDir(), "native-progress")
			if err != nil {
				t.Fatal(err)
			}
			progressMu.Lock()
			previousStdout := os.Stdout
			os.Stdout = progressFile
			progressMu.Unlock()
			t.Cleanup(func() {
				progressMu.Lock()
				os.Stdout = previousStdout
				progressMu.Unlock()
				_ = progressFile.Close()
			})
			s, _ := startTestActor(t, modeServing, "0", nil)
			decode, eof, body, execution := newIngressGate(), newIngressGate(), newIngressGate(), newIngressGate()
			for _, gate := range []*ingressGate{decode, eof, body, execution} {
				t.Cleanup(gate.release)
			}
			s.runner.input = ingressRunnerInput{s.runner.input, execution}
			payload, err := json.Marshal(nativeIngressRequest("old"))
			if err != nil {
				t.Fatal(err)
			}
			wire := []byte(fmt.Sprintf("POST /v2/models/synthetic/infer HTTP/1.1\r\nHost: native\r\nX-Old: yes\r\nContent-Length: %d\r\n\r\n%s", len(payload), payload))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			buffered, read, canceled, returned := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
			h := NewHandler(s)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Old") != "yes" {
					h.ServeHTTP(w, r)
					return
				}
				go func() { <-r.Context().Done(); close(canceled) }()
				r.Body = &ingressBody{ReadCloser: r.Body, remaining: r.ContentLength, read: read, proceed: body}
				defer close(returned)
				h.ServeHTTP(w, r)
			})}
			served := make(chan struct{})
			go func() {
				defer close(served)
				_ = server.Serve(&ingressListener{Listener: listener, wireBytes: len(wire), buffered: buffered, decode: decode, eof: eof})
			}()
			t.Cleanup(func() {
				decode.release()
				eof.release()
				body.release()
				execution.release()
				_ = server.Close()
				waitIngress(t, served)
			})
			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 4 * time.Second}
			defer client.CloseIdleConnections()
			base := "http://" + listener.Addr().String() + "/v2/models/synthetic"
			state := func() State {
				t.Helper()
				resp, err := client.Get(base + "/state")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = resp.Body.Close() }()
				var v State
				if err := json.NewDecoder(resp.Body).Decode(&v); err != nil || resp.StatusCode != http.StatusOK {
					t.Fatalf("native state: %d %v", resp.StatusCode, err)
				}
				return v
			}
			old, err := net.Dial("tcp", listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = old.Close() }()
			if _, err := io.Copy(old, bytes.NewReader(wire)); err != nil {
				t.Fatal(err)
			}
			waitIngress(t, buffered)
			// Full close, not CloseWrite: this gateway retains no response reader.
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			clear := state()
			if len(clear.Requests) != 0 || clear.AcceptedRequests != 0 {
				t.Fatalf("independent actor-empty observation before late decode: %+v", clear)
			}
			decode.release()
			waitIngress(t, read)
			clear = state()
			if len(clear.Requests) != 0 || clear.AcceptedRequests != 0 {
				t.Fatalf("independent actor-empty observation after body EOF, before actor send: %+v", clear)
			}
			if cancelFirst {
				eof.release()
				waitIngress(t, canceled)
				body.release()
				waitIngress(t, returned)
				v := state()
				if len(v.Requests) != 0 || v.AcceptedRequests != 0 || v.Executions != 0 {
					t.Fatalf("already-delivered cancellation admitted inference: %+v", v)
				}
				return
			}
			body.release()
			awaitActorState(t, s, func(v State) bool { return len(v.Requests) == 1 && v.Requests[0].Phase == phaseRunning })
			eof.release()
			waitIngress(t, canceled)
			waitIngress(t, returned)
			assertPostClearNativeIngress(t, s, state, client, base, old, execution, clear)
			assertLateNativeProgress(t, progressFile.Name(), clear.At)
		})
	}
}

func assertPostClearNativeIngress(t *testing.T, s *Scheduler, state func() State, client *http.Client, base string, old net.Conn, execution *ingressGate, clear State) {
	t.Helper()
	// Handler termination cannot release physical work held at runner I/O.
	v := state()
	if len(v.Requests) != 1 || v.Requests[0].Context.RequestID != "old" || v.Requests[0].Phase != phaseRunning || !v.At.After(clear.At) {
		t.Fatalf("late old work must remain beyond actor-empty CLEAR: %+v", v)
	}
	fresh := postFreshNativeIngress(client, base)
	awaitActorState(t, s, func(v State) bool { return len(v.Requests) == 2 })
	v = state()
	for _, r := range v.Requests {
		if r.Context.RequestID == "fresh" && r.Phase != phaseQueued {
			t.Fatalf("first post-CLEAR request bypassed late physical work: %+v", v)
		}
	}
	select {
	case result := <-fresh:
		t.Fatalf("fresh response before late execution released: %+v", result)
	default:
	}
	var responseBytes [1]byte
	if n, err := old.Read(responseBytes[:]); n != 0 || err == nil {
		t.Fatalf("closed gateway received native result: n=%d err=%v", n, err)
	}
	execution.release()
	select {
	case result := <-fresh:
		if result.err != nil || result.response.ID != "fresh" || !result.response.ExecutionComplete || !reflect.DeepEqual(result.response.Outputs, echoOutputs("sample")) || result.response.Progress.QueueMS <= 0 {
			t.Fatalf("fresh inference after late completion: %+v", result)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("fresh request did not complete after late work")
	}
	awaitActorState(t, s, func(v State) bool { return len(v.Requests) == 0 && v.CompletedRequests == 2 })
	v = state()
	if v.Executions != 2 || !reflect.DeepEqual(v.Metadata, clear.Metadata) {
		t.Fatalf("inference changed metadata/model/policy or lost physical accounting: %+v", v)
	}
}

type freshIngressResult struct {
	response Response
	err      error
}

func postFreshNativeIngress(client *http.Client, base string) <-chan freshIngressResult {
	fresh := make(chan freshIngressResult, 1)
	go func() {
		payload, err := json.Marshal(nativeIngressRequest("fresh"))
		if err != nil {
			fresh <- freshIngressResult{err: err}
			return
		}
		resp, err := client.Post(base+"/infer", "application/json", bytes.NewReader(payload))
		if err != nil {
			fresh <- freshIngressResult{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		var result Response
		err = json.NewDecoder(resp.Body).Decode(&result)
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("fresh HTTP status %d", resp.StatusCode)
		}
		fresh <- freshIngressResult{result, err}
	}()
	return fresh
}

func assertLateNativeProgress(t *testing.T, path string, clear time.Time) {
	t.Helper()
	progressBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lateFinished Progress
	for _, line := range bytes.Split(progressBytes, []byte("\n")) {
		if !bytes.HasPrefix(line, []byte("CHILL_PROGRESS ")) {
			continue
		}
		var p Progress
		if err := json.Unmarshal(bytes.TrimPrefix(line, []byte("CHILL_PROGRESS ")), &p); err != nil {
			t.Fatal(err)
		}
		if p.RequestID == "old" && p.Phase == phaseFinished {
			lateFinished = p
		}
	}
	// Direct native completion, not a reconstructed old HTTP receipt:
	// E5 influence lasts beyond CLEAR through this actual completion.
	if !lateFinished.ExecutionComplete || !lateFinished.At.After(clear) || lateFinished.BatchID == 0 || lateFinished.Units != 1 || lateFinished.Outcome != context.Canceled.Error() {
		t.Fatalf("missing late native completion beyond CLEAR: %+v", lateFinished)
	}
}

func TestNativeServingHTTPRejectsControlAndInferenceMutation(t *testing.T) {
	s, _ := startTestActor(t, modeServing, "0", nil)
	before, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(s))
	defer server.Close()
	client := server.Client()
	client.Timeout = 4 * time.Second
	base := server.URL + "/v2/models/synthetic"
	for _, path := range []string{"profile", "load", "unload", "prepare", "route", "policy", "config", "batch-config", "power"} {
		resp, err := client.Post(base+"/"+path, "application/json", bytes.NewBufferString(`{"model":"replacement","policy":{"decisionID":"forged"}}`))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		want := http.StatusNotFound
		if path == "profile" {
			want = http.StatusConflict
		}
		if resp.StatusCode != want {
			t.Fatalf("serving %s: status %d, want %d", path, resp.StatusCode, want)
		}
	}
	for _, field := range []string{"model", "policy", "config", "batchSize", "runnerCommand"} {
		body, _ := json.Marshal(nativeIngressRequest("mutation"))
		var object map[string]any
		if err := json.Unmarshal(body, &object); err != nil {
			t.Fatal(err)
		}
		object[field] = "forged"
		body, _ = json.Marshal(object)
		resp, err := client.Post(base+"/infer", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("inference accepted %s mutation: %d", field, resp.StatusCode)
		}
	}
	after, err := s.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Metadata, after.Metadata) || after.AcceptedRequests != 0 || after.Executions != 0 {
		t.Fatalf("serving HTTP mutated native configuration: before=%+v after=%+v", before, after)
	}
}
