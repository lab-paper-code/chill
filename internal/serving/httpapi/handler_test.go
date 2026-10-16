package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestExpiredOperationCanDeliverTimeoutResponse(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			mux := http.NewServeMux()
			handler := &Handler{config: HandlerConfig{RequestTimeout: 200 * time.Millisecond, MaxRequestBytes: 1024}, mux: mux}
			mux.HandleFunc(method+" /slow", func(w http.ResponseWriter, r *http.Request) {
				if method == http.MethodPost {
					var body map[string]any
					if err := handler.decode(w, r, &body); err != nil {
						t.Error(err)
						return
					}
				}
				<-r.Context().Done()
				sendError(w, contextRequestError(r.Context().Err()))
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("timeout response flush failed: %v", err)
				}
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			var input io.Reader
			if method == http.MethodPost {
				input = strings.NewReader("{}")
			}
			request, err := http.NewRequest(method, server.URL+"/slow", input)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: 2 * time.Second}
			response, err := client.Do(request)
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
		})
	}
}
