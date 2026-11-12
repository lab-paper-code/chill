package firstfloor

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestGatewayAcceptsOldNativeResponseWithoutHTTPDiagnostics(t *testing.T) {
	definition := &serving.Definition{UID: "uid", Generation: 1, Model: serving.Model{UID: "model", Generation: 1}}
	p := native.Progress{RequestID: "request", WorkloadUID: definition.UID, Definition: definition.Identity(),
		Phase: "finished", At: time.Now().UTC(), BatchID: 1, BatchSize: 1, ProfileID: "profile", ExecutionComplete: true}
	body, err := json.Marshal(native.Response{ID: p.RequestID, ModelName: "model", ExecutionComplete: true,
		Outputs: []native.Field{{Name: "answer", Data: [][]byte{[]byte("yes")}}}, Progress: p})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("nativeHTTP")) || bytes.Contains(body, []byte("nativeDeferredAt")) {
		t.Fatal("old-native fixture contains A1-only fields")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	result, err := (&Client{HTTPClient: server.Client()}).Execute(context.Background(),
		Spec{Endpoint: server.URL, ModelName: "model", Profile: &serving.Profile{ID: "profile"}},
		serving.Dispatch{RequestID: p.RequestID, Definition: definition, Input: &serving.InferenceInput{}, ReceivedAt: time.Now()})
	if err != nil || !result.Finished || result.Progress == nil || result.Progress.NativeHTTP != nil || result.NativeHTTP != nil {
		t.Fatalf("new gateway rejected old-native completion: %+v, %v", result, err)
	}
}

func TestObserverAcceptsOldNativeProgressWithoutHTTPDiagnostics(t *testing.T) {
	for _, phase := range []string{"queued", "deferred", "running", "finished"} {
		t.Run(phase, func(t *testing.T) {
			p := native.Progress{RequestID: "request", WorkloadUID: "uid", Definition: "definition", Phase: phase,
				At: time.Now().UTC(), ExecutionComplete: phase == "finished"}
			body, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte("nativeHTTP")) || bytes.Contains(body, []byte("nativeDeferredAt")) {
				t.Fatal("old-native fixture contains A1-only fields")
			}
			progress, err := ParseProgress(append([]byte("CHILL_PROGRESS "), body...))
			if err != nil || progress.Phase != phase || progress.NativeHTTP != nil || progress.NativeDeferredAt != nil {
				t.Fatalf("new observer rejected old-native %s: %+v, %v", phase, progress, err)
			}
		})
	}
}
