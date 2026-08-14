package entrypoint

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const twoNodeTable = `
models:
  mobilenet:
    - node: jetsono
      service: chill-serving-jetsono
      weight: 1
    - node: orangepi5
      service: chill-serving-orangepi5
      weight: 3
`

func TestParseStaticRoutesReadsATable(t *testing.T) {
	routes, err := ParseStaticRoutes([]byte(twoNodeTable))
	if err != nil {
		t.Fatalf("ParseStaticRoutes returned %v", err)
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
	if _, served := routes.WeightsFor("resnet"); served {
		t.Error("the table serves resnet, want a model absent from the table unserved")
	}
}

func TestParseStaticRoutesRejects(t *testing.T) {
	for _, testCase := range []struct {
		name string
		raw  string
	}{
		{name: "a table with no model in it", raw: "models: {}"},
		{
			name: "a model no node carries weight for",
			raw:  "models:\n  mobilenet:\n    - node: jetsono\n      service: chill-serving-jetsono\n      weight: 0\n",
		},
		{
			name: "a node entered twice for one model",
			raw: "models:\n  mobilenet:\n" +
				"    - node: jetsono\n      service: chill-serving-jetsono\n      weight: 1\n" +
				"    - node: jetsono\n      service: chill-serving-jetsono\n      weight: 2\n",
		},
		{
			name: "a field the table does not define",
			raw: "models:\n  mobilenet:\n" +
				"    - node: jetsono\n      service: chill-serving-jetsono\n      weight: 1\n      slo: 100ms\n",
		},
		{name: "a document that is not a table", raw: "models: mobilenet"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got, err := ParseStaticRoutes([]byte(testCase.raw)); err == nil {
				t.Fatalf("ParseStaticRoutes accepted the table as %+v, want an error", got)
			}
		})
	}
}

// TestStaticRoutesDriveTheHandler exercises the stand-in through the interface
// the scheduler will later satisfy, so replacing it changes nothing else.
func TestStaticRoutesDriveTheHandler(t *testing.T) {
	routes, err := ParseStaticRoutes([]byte(twoNodeTable))
	if err != nil {
		t.Fatalf("ParseStaticRoutes returned %v", err)
	}
	transport := &recordingTransport{}
	entryPoint := &Handler{
		Routes:    routes,
		Namespace: "chill-system",
		Port:      8000,
		Draw:      func() uint64 { return 0 },
		Transport: transport,
	}

	entryPoint.ServeHTTP(httptest.NewRecorder(), inferenceRequest("/v2/models/mobilenet/infer", "{}"))

	if transport.forwarded == nil {
		t.Fatal("nothing was forwarded")
	}
	if got, want := transport.forwarded.URL.Host, "chill-serving-jetsono.chill-system.svc:8000"; got != want {
		t.Errorf("forwarded to %q, want the node the table's first share names, %q", got, want)
	}
}

func TestStaticRoutesRefuseAModelTheTableOmits(t *testing.T) {
	routes, err := ParseStaticRoutes([]byte(twoNodeTable))
	if err != nil {
		t.Fatalf("ParseStaticRoutes returned %v", err)
	}
	transport := &recordingTransport{}
	recorder := httptest.NewRecorder()
	entryPoint := &Handler{Routes: routes, Namespace: "chill-system", Port: 8000, Transport: transport}

	entryPoint.ServeHTTP(recorder, inferenceRequest("/v2/models/resnet/infer", "{}"))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("the consumer received %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if transport.forwarded != nil {
		t.Errorf("the request was forwarded to %q, want it refused at the entry point",
			transport.forwarded.URL.Host)
	}
}
