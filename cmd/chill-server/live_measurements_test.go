package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	liveMeasurementAccepted     = "Accepted"
	liveMeasurementNotSubmitted = "NOT_SUBMITTED"
	liveMeasurementPath         = "/api/v1/namespaces/default/batch-energy-optimizations"
	liveMeasurementBody         = `{"apiVersion":"edge.dacs.io/v1alpha1","kind":"BatchEnergyOptimization",` +
		`"metadata":{"name":"task"}}`
)

type measurementLiveFrame struct {
	Type string    `json:"type"`
	At   time.Time `json:"at"`
	Seq  uint64    `json:"seq"`
	live.Event
}

func readMeasurementLiveFrame(t *testing.T, ctx context.Context, conn *websocket.Conn) measurementLiveFrame {
	t.Helper()
	_, raw, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var value measurementLiveFrame
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertMeasurementLiveHistory(t *testing.T, handler http.Handler, event measurementLiveFrame) {
	t.Helper()
	if event.Action.Status == liveMeasurementNotSubmitted &&
		(event.Action.Phase != "completed" || event.Action.Result.Status != liveMeasurementNotSubmitted) {
		t.Fatalf("preflight was projected as a failure: %+v", event.Action)
	}
	path := "/api/v1/observations/events?from=" + event.At.Format(time.RFC3339Nano) +
		"&to=" + event.At.Add(time.Nanosecond).Format(time.RFC3339Nano) + "&limit=100"
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("Authorization", "Bearer test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var history struct {
		Events []measurementLiveFrame `json:"events"`
	}
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &history) != nil ||
		len(history.Events) != 1 || !reflect.DeepEqual(history.Events[0].Event, event.Event) ||
		!history.Events[0].At.Equal(event.At) {
		t.Fatalf("history/live diverged: %s / %+v", response.Body, event)
	}
}

func TestMeasurementMutationEventsMatchHistoryAndReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	publisher := newLivePublisher(ctx)
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	client := fake.NewSimpleClientset()
	client.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
			Authenticated: true, User: authentication.UserInfo{Username: "operator"},
		}}, nil
	})
	client.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	callbackCount := 0
	config := httpapi.HandlerConfig{
		MaxRequestBytes: 1 << 20, RequestTimeout: time.Second, OperationTimeout: time.Second, MaxQueryEvents: 100,
		Live: publisher.hub,
		OnMutationAction: func(action serving.SchedulerActionObservation, at time.Time) {
			records, err := log.EventObservations(ctx, at, 10)
			if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0].Action, &action) {
				t.Fatalf("publication preceded durable commit: %+v err=%v", records, err)
			}
			callbackCount++
			publisher.action(action, at)
		},
		SubmitMeasurement: func(
			_ context.Context, task api.BatchEnergyOptimization, dry bool,
		) (httpapi.MeasurementMutationResult, error) {
			task.UID, task.ResourceVersion = "native", "7"
			status := liveMeasurementAccepted
			if dry {
				status = liveMeasurementNotSubmitted
			}
			return httpapi.MeasurementMutationResult{Request: status, Task: &task}, nil
		},
		DeleteMeasurement: func(context.Context, string, string, string, string) (httpapi.MeasurementMutationResult, error) {
			return httpapi.MeasurementMutationResult{Request: "DeletionRequested", Task: &api.BatchEnergyOptimization{}}, nil
		},
	}
	handler, err := httpapi.NewHandler(
		&servingkube.Manager{}, &serving.Runtime{}, &servingkube.Authorizer{Client: client},
		log, &servingkube.Observer{}, config,
	)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v1/live"
	conn, _, err := websocket.Dial(ctx, url,
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer test"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	read := func() measurementLiveFrame { return readMeasurementLiveFrame(t, ctx, conn) }
	if read().Type != "hello" {
		t.Fatal("missing hello")
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resume","from":0}`)); err != nil {
		t.Fatal(err)
	}
	if read().Type != "snapshot" {
		t.Fatal("missing baseline")
	}
	var received []measurementLiveFrame
	var sequence uint64
	for _, tc := range []struct{ method, suffix, status string }{
		{http.MethodPost, "", liveMeasurementAccepted},
		{http.MethodDelete, "/task?uid=native", "DeletionRequested"},
		{http.MethodPost, "?dryRun=All", liveMeasurementNotSubmitted},
	} {
		request := httptest.NewRequest(tc.method, liveMeasurementPath+tc.suffix, strings.NewReader(liveMeasurementBody))
		request.Header.Set("Authorization", "Bearer test")
		if tc.method == http.MethodDelete {
			request.Header.Set("If-Match", `"7"`)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted && response.Code != http.StatusOK {
			t.Fatalf("mutation: %d %s", response.Code, response.Body)
		}
		for _, status := range []string{"validated", tc.status} {
			delta, event := read(), read()
			if delta.Type != "delta" || event.Type != "event" || delta.Seq != sequence+1 ||
				event.Seq != sequence+2 || event.Kind != "action" || event.Action.Status != status {
				t.Fatalf("missing ordered measurement transition: %+v / %+v", delta, event)
			}
			sequence = event.Seq
			received = append(received, event)
			assertMeasurementLiveHistory(t, handler, event)
		}
	}
	checkpoint := received[0]
	resume := fmt.Sprintf(`{"type":"resume","from":%d,"at":%q}`, checkpoint.Seq, checkpoint.At.Format(time.RFC3339Nano))
	if err := conn.Write(ctx, websocket.MessageText, []byte(resume)); err != nil {
		t.Fatal(err)
	}
	for _, expected := range received[1:] {
		if read().Type != "delta" {
			t.Fatal("missing replay delta")
		}
		if replay := read(); !reflect.DeepEqual(replay, expected) {
			t.Fatalf("replay changed event: %+v / %+v", replay, expected)
		}
	}
	if callbackCount != 6 {
		t.Fatalf("callbacks=%d", callbackCount)
	}
	if err := log.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, liveMeasurementPath, strings.NewReader(liveMeasurementBody))
	request.Header.Set("Authorization", "Bearer test")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if callbackCount != 6 {
		t.Fatal("failed append published a live event")
	}
}
