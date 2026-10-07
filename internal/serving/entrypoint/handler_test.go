package entrypoint

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestExpiredOperationCanDeliverTimeoutResponse(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
		sendError(w, contextRequestError(r.Context().Err()))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("timeout response flush failed: %v", err)
		}
	})
	handler := &Handler{config: HandlerConfig{RequestTimeout: 200 * time.Millisecond}, mux: mux}
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(server.URL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Code string `json:"code"`
	}
	decodeErr := json.NewDecoder(response.Body).Decode(&body)
	closeErr := response.Body.Close()
	if decodeErr != nil || closeErr != nil ||
		response.StatusCode != http.StatusGatewayTimeout || body.Code != requestTimeoutCode {
		t.Fatalf("timeout response: status=%d code=%s decode=%v close=%v",
			response.StatusCode, body.Code, decodeErr, closeErr)
	}
}
