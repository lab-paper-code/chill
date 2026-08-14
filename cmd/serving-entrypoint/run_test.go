package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const twoModelTable = `
models:
  mobilenet:
    - node: jetsono
      service: chill-serving-jetsono
      weight: 1
  resnet:
    - node: orangepi5
      service: chill-serving-orangepi5
      weight: 1
`

func writeTable(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write the routing table: %v", err)
	}
	return path
}

func TestLoadRoutesReadsATable(t *testing.T) {
	routes, err := loadRoutes(writeTable(t, twoModelTable))
	if err != nil {
		t.Fatalf("loadRoutes returned %v", err)
	}
	if got, want := servedModels(routes), []string{"mobilenet", "resnet"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the entry point reports %v, want %v", got, want)
	}
}

func TestLoadRoutesRefusesToStart(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path func(*testing.T) string
	}{
		{
			name: "a routing table that is not there",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.yaml") },
		},
		{
			name: "a routing table nothing can be served from",
			path: func(t *testing.T) string { return writeTable(t, "models: {}") },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := loadRoutes(testCase.path(t)); err == nil {
				t.Fatal("loadRoutes accepted the table, want an error")
			}
		})
	}
}

// TestBuildHandlerServesTheLoadedTable checks the assembly rather than the
// forwarding: both refusals are decided before anything leaves the process.
func TestBuildHandlerServesTheLoadedTable(t *testing.T) {
	routes, err := loadRoutes(writeTable(t, twoModelTable))
	if err != nil {
		t.Fatalf("loadRoutes returned %v", err)
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
