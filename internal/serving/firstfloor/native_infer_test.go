package firstfloor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
)

func TestInferNativeUsesGatewayEncodingAndPoolWithoutProfile(t *testing.T) {
	input := []byte{0, 1, 127, 128, 255}
	var reused atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/models/model/infer" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("wrong native exchange: %s %v", r.URL, r.Header)
		}
		var request native.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.ID != "request" || request.Context.RequestID != "request" || len(request.Inputs) != 1 || string(request.Inputs[0].Data[0]) != string(input) {
			t.Errorf("native request encoding changed: %+v", request)
		}
		_, _ = w.Write([]byte(`{"executionComplete":true}`))
	}))
	defer server.Close()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &Client{HTTPClient: &http.Client{Transport: transport}}
	ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		if info.Reused {
			reused.Add(1)
		}
	}})
	for range 2 {
		body, status, err := client.InferNative(ctx, server.URL+"/v2/models/model/infer", native.Request{ID: "request", Context: native.RequestContext{RequestID: "request"}, Inputs: []native.Field{{Name: "image", Data: [][]byte{input}}}})
		if err != nil || status != http.StatusOK || string(body) != `{"executionComplete":true}` {
			t.Fatalf("native exchange failed without profile: status=%d body=%s err=%v", status, body, err)
		}
	}
	if reused.Load() != 1 {
		t.Fatalf("native request did not reuse gateway pool: %d", reused.Load())
	}
}

func TestNativeRequestBuffersFenceLateTransportReads(t *testing.T) {
	var replay io.ReadCloser
	client := &Client{HTTPClient: &http.Client{Transport: traceTransport(func(request *http.Request) (*http.Response, error) {
		var err error
		replay, err = request.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader("{}"))}, nil
	})}}
	if _, _, err := client.InferNative(context.Background(), "http://native/infer", native.Request{ID: "r"}); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(replay); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("request buffer remains readable after pool return: %v", err)
	}
}
