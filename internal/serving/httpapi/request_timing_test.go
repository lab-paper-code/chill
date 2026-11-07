package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func timingHandler(t *testing.T, execute func(context.Context, serving.Dispatch) (serving.BackendResult, error)) (*Handler, *observation.ObservationLog) {
	t.Helper()
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	runtime, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{QueueLimit: 8, ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	definition := &serving.Definition{Name: "workload", UID: "uid", Generation: 1, Model: serving.Model{Paths: []serving.ModelPath{{}}}, Dataset: &serving.DatasetReference{Name: "data", UID: "data"}, SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second}}
	profile := &serving.Profile{ID: "profile"}
	runtime.SetProfiles([]serving.Profile{*profile})
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := runtime.InstallInstances([]serving.ReadyInstance{{State: serving.InstanceState{ID: "instance", Node: serving.NodeReference{Name: "node", UID: "node"}, Ready: true}, Profile: profile, Capacity: 1, Routing: map[string]int32{"uid": 1}, Execute: execute}}); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, k8sruntime.Object, error) {
		return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{Authenticated: true, User: authentication.UserInfo{Username: "client"}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, k8sruntime.Object, error) {
		return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	h := &Handler{runtime: runtime, authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{MaxRequestBytes: 1024, RequestTimeout: time.Second}, mux: http.NewServeMux()}
	h.mux.HandleFunc("POST /api/workloads/{id}/infer", h.infer)
	return h, log
}

func timingRow(t *testing.T, log *observation.ObservationLog, id string) serving.RequestRow {
	t.Helper()
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	result, err := log.Query(context.Background(), "uid", time.Time{}, time.Time{}, nil, nil, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Requests {
		if row.RequestID == id {
			return row
		}
	}
	t.Fatalf("request %s missing from query", id)
	return serving.RequestRow{}
}

func requireTiming(t *testing.T, row serving.RequestRow, stage serving.ServerStage, present bool) time.Duration {
	t.Helper()
	elapsed, ok := row.ServerTiming.Elapsed(stage)
	if ok != present {
		t.Fatalf("stage %v presence=%v, want %v; outcome=%s", stage, ok, present, row.Outcome)
	}
	return elapsed
}

// The handler runs the real runtime and persists its result through the query reader.
func TestInferenceTimingErrorBoundaries(t *testing.T) {
	for _, scenario := range []string{"success", "auth", "decode", "admission", "worker", "timeout", "cancel", "write", "flush", "error-write", "error-flush"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			h, log := timingHandler(t, func(ctx context.Context, dispatch serving.Dispatch) (serving.BackendResult, error) {
				result := serving.BackendResult{Finished: true, ContentType: "application/json", Body: []byte(`{}`)}
				result.ServerTiming.Mark(serving.ServerWorkerHTTPStart, dispatch.ReceivedAt)
				result.ServerTiming.Mark(serving.ServerWorkerHTTPDone, dispatch.ReceivedAt)
				if scenario == "cancel" || scenario == "timeout" {
					if scenario == "cancel" {
						cancel()
					}
					<-ctx.Done()
					return result, ctx.Err()
				}
				if scenario == "worker" {
					return result, &serving.Error{Code: "BackendError", Message: "worker failed"}
				}
				return result, nil
			})
			body := `{}`
			if scenario == "decode" {
				body = `{`
			}
			if scenario == "admission" {
				if err := h.runtime.StopAdmission("uid", "stopped"); err != nil {
					t.Fatal(err)
				}
			}
			request := httptest.NewRequest(http.MethodPost, "/api/workloads/workload/infer", strings.NewReader(body)).WithContext(ctx)
			if scenario != "auth" && scenario != "error-write" && scenario != "error-flush" {
				request.Header.Set("Authorization", "Bearer client")
			}
			if scenario == "timeout" {
				request.Header.Set("X-Chill-Request-Timeout", "20ms")
			}
			writer := &timingFailureWriter{ResponseRecorder: httptest.NewRecorder(), writeFailure: scenario == "write" || scenario == "error-write", flushFailure: scenario == "flush" || scenario == "error-flush"}
			h.ServeHTTP(writer, request)
			row := timingRow(t, log, writer.Header().Get("X-Chill-Request-ID"))
			wantOutcome := "completed"
			switch scenario {
			case "auth", "decode", "admission":
				wantOutcome = "rejected"
			case "worker":
				wantOutcome = "error"
			case "timeout":
				wantOutcome = "timeout"
			case "cancel":
				wantOutcome = "canceled"
			case "write", "flush", "error-write", "error-flush":
				wantOutcome = "delivery_failed"
			}
			if row.Outcome != wantOutcome {
				t.Fatalf("outcome=%s, want %s", row.Outcome, wantOutcome)
			}
			requireTiming(t, row, serving.ServerReceiveDone, true)
			requireTiming(t, row, serving.ServerAuthStart, true)
			requireTiming(t, row, serving.ServerAuthDone, true)
			decoded := scenario != "auth" && scenario != "error-write" && scenario != "error-flush"
			requireTiming(t, row, serving.ServerBodyDecodeStart, decoded)
			requireTiming(t, row, serving.ServerBodyDecodeDone, decoded)
			requireTiming(t, row, serving.ServerAcceptDone, decoded && scenario != "decode")
			wrote := scenario != "cancel"
			writeStart := requireTiming(t, row, serving.ServerResponseWriteStart, wrote)
			writeDone := requireTiming(t, row, serving.ServerResponseWriteDone, wrote)
			flushed := wrote && !writer.writeFailure
			flushStart := requireTiming(t, row, serving.ServerResponseFlushStart, flushed)
			flushDone := requireTiming(t, row, serving.ServerResponseFlushDone, flushed)
			if writeDone < writeStart || flushDone < flushStart {
				t.Fatal("response boundaries reversed")
			}
			if scenario == "worker" || scenario == "success" {
				requireTiming(t, row, serving.ServerWorkerHTTPStart, true)
				requireTiming(t, row, serving.ServerWorkerHTTPDone, true)
				requireTiming(t, row, serving.ServerReplyReceived, true)
			}
		})
	}
}

type timingFailureWriter struct {
	*httptest.ResponseRecorder
	writeFailure bool
	flushFailure bool
}

func (w *timingFailureWriter) Write(body []byte) (int, error) {
	if w.writeFailure {
		return 0, io.ErrClosedPipe
	}
	return w.ResponseRecorder.Write(body)
}

func (w *timingFailureWriter) FlushError() error {
	if w.flushFailure {
		return errors.New("flush failed")
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestInferenceTimingAttributesAuthAndBodyWaits(t *testing.T) {
	h, log := timingHandler(t, func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
		return serving.BackendResult{Finished: true, ContentType: "application/json", Body: []byte(`{}`)}, nil
	})
	authEntered, authRelease := make(chan time.Time, 1), make(chan struct{})
	h.authorizer.Client.(*fake.Clientset).PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, k8sruntime.Object, error) {
		authEntered <- time.Now()
		<-authRelease
		return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{Authenticated: true, User: authentication.UserInfo{Username: "client"}}}, nil
	})
	bodyEntered, bodyRelease := make(chan time.Time, 1), make(chan struct{})
	body := &timingBarrierBody{entered: bodyEntered, release: bodyRelease, Reader: strings.NewReader(`{}`)}
	request := httptest.NewRequest(http.MethodPost, "/api/workloads/workload/infer", body)
	request.Header.Set("Authorization", "Bearer client")
	writer := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(writer, request)
		close(done)
	}()
	authArrived := <-authEntered
	authReleased := time.Now()
	close(authRelease)
	bodyArrived := <-bodyEntered
	bodyReleased := time.Now()
	close(bodyRelease)
	<-done
	row := timingRow(t, log, writer.Header().Get("X-Chill-Request-ID"))
	authStart := requireTiming(t, row, serving.ServerAuthStart, true)
	authDone := requireTiming(t, row, serving.ServerAuthDone, true)
	bodyStart := requireTiming(t, row, serving.ServerBodyDecodeStart, true)
	bodyDone := requireTiming(t, row, serving.ServerBodyDecodeDone, true)
	if authStart > authArrived.Sub(row.ReceivedAt) || authDone < authReleased.Sub(row.ReceivedAt) ||
		bodyStart < authDone || bodyStart > bodyArrived.Sub(row.ReceivedAt) || bodyDone < bodyReleased.Sub(row.ReceivedAt) {
		t.Fatal("auth/body barrier wait attributed outside its module")
	}
	if row.Outcome != "completed" {
		t.Fatalf("barriers changed request outcome: %s", row.Outcome)
	}
}

type timingBarrierBody struct {
	io.Reader
	entered chan time.Time
	release <-chan struct{}
}

func (b *timingBarrierBody) Read(data []byte) (int, error) {
	if b.entered != nil {
		b.entered <- time.Now()
		b.entered = nil
		<-b.release
	}
	return b.Reader.Read(data)
}
