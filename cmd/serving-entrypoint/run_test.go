package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestBuildHandlerServesTheRoutesItWasGiven checks the assembly rather than the
// forwarding: the table reaches the handler as an entrypoint.Routes, and both
// refusals are decided before anything leaves the process.
func TestBuildHandlerServesTheRoutesItWasGiven(t *testing.T) {
	routes, err := readRoutes(writeTable(t, twoModelTable))
	if err != nil {
		t.Fatalf("readRoutes returned %v", err)
	}
	handler := buildHandler(routes, validOptions())

	for _, testCase := range []struct {
		name string
		path string
		want int
	}{
		{name: "a model the table omits", path: "/v2/models/yolo/infer", want: http.StatusServiceUnavailable},
		{name: "a path that asks for no inference", path: "/v2/health/ready", want: http.StatusNotFound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, testCase.path, strings.NewReader("{}")))
			if recorder.Code != testCase.want {
				t.Errorf("the consumer received %d, want %d", recorder.Code, testCase.want)
			}
		})
	}
}
