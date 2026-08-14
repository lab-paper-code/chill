package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
)

// The table is only useful as the weight source the entry point reads through.
var _ entrypoint.Routes = staticRoutes{}

const twoModelTable = `
models:
  mobilenet:
    - node: jetsono
      service: chill-serving-jetsono
      weight: 1
    - node: orangepi5
      service: chill-serving-orangepi5
      weight: 3
  resnet:
    - node: genesis2
      service: chill-serving-genesis2
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

func TestReadRoutesReadsATable(t *testing.T) {
	routes, err := readRoutes(writeTable(t, twoModelTable))
	if err != nil {
		t.Fatalf("readRoutes returned %v", err)
	}

	if got, want := routes.modelNames(), []string{"mobilenet", "resnet"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the entry point reports %v, want %v", got, want)
	}
	weights, served := routes.WeightsFor("mobilenet")
	if !served {
		t.Fatal("the table does not serve mobilenet, want it served")
	}
	if len(weights) != 2 {
		t.Fatalf("mobilenet has %d nodes, want 2", len(weights))
	}
	if got := weights[1]; got.Node != "orangepi5" || got.Service != "chill-serving-orangepi5" || got.Weight != 3 {
		t.Errorf("second node read as %+v, want orangepi5 weighted 3", got)
	}
	if _, served := routes.WeightsFor("yolo"); served {
		t.Error("the table serves yolo, want a model absent from the table unserved")
	}
}

func TestReadRoutesRefusesToStart(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path func(*testing.T) string
	}{
		{
			name: "a routing table that is not there",
			path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.yaml") },
		},
		{
			name: "a table with no model in it",
			path: func(t *testing.T) string { return writeTable(t, "models: {}") },
		},
		{
			name: "a model no node carries weight for",
			path: func(t *testing.T) string {
				return writeTable(t,
					"models:\n  mobilenet:\n    - node: jetsono\n      service: s\n      weight: 0\n")
			},
		},
		{
			name: "a node entered twice for one model",
			path: func(t *testing.T) string {
				return writeTable(t, "models:\n  mobilenet:\n"+
					"    - node: jetsono\n      service: s\n      weight: 1\n"+
					"    - node: jetsono\n      service: s\n      weight: 2\n")
			},
		},
		{
			name: "a field the table does not define",
			path: func(t *testing.T) string {
				return writeTable(t,
					"models:\n  mobilenet:\n    - node: jetsono\n      service: s\n      weight: 1\n      slo: 100ms\n")
			},
		},
		{
			name: "a document that is not a table",
			path: func(t *testing.T) string { return writeTable(t, "models: mobilenet") },
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got, err := readRoutes(testCase.path(t)); err == nil {
				t.Fatalf("readRoutes accepted the table as %+v, want an error", got)
			}
		})
	}
}
