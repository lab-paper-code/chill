package entrypoint

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/serving"
)

type staticRoutes map[string]serving.Weights

func (s staticRoutes) WeightsFor(model string) (serving.Weights, bool) {
	weights, served := s[model]
	return weights, served
}

// recordingTransport stands in for the serving node so a test can read the
// request that left the entry point.
type recordingTransport struct {
	forwarded *http.Request
	body      string
}

func (t *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		t.body = string(body)
	}
	t.forwarded = request
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"outputs":[]}`)),
	}, nil
}

func backend(node string, weight int32) serving.Backend {
	return serving.Backend{Node: node, Service: "chill-serving-" + node, Weight: weight}
}

func handler(routes staticRoutes, transport http.RoundTripper, pick uint64) *Handler {
	return &Handler{
		Routes:    routes,
		Namespace: "chill-system",
		Port:      8000,
		Draw:      func() uint64 { return pick },
		Transport: transport,
	}
}

func inferenceRequest(path, body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
}

func TestServeForwardsTheRequestToTheChosenNode(t *testing.T) {
	transport := &recordingTransport{}
	routes := staticRoutes{"mobilenet": serving.Weights{backend("jetsono", 1)}}
	recorder := httptest.NewRecorder()

	handler(routes, transport, 0).ServeHTTP(
		recorder, inferenceRequest("/v2/models/mobilenet/infer", `{"inputs":[]}`))

	if transport.forwarded == nil {
		t.Fatal("nothing was forwarded")
	}
	if got, want := transport.forwarded.URL.Host, "chill-serving-jetsono.chill-system.svc:8000"; got != want {
		t.Errorf("forwarded to %q, want the chosen node's Service at %q", got, want)
	}
	if got, want := transport.forwarded.URL.Path, "/v2/models/mobilenet/infer"; got != want {
		t.Errorf("forwarded path %q, want the path the consumer sent, %q", got, want)
	}
	if got := transport.forwarded.Method; got != http.MethodPost {
		t.Errorf("forwarded method %q, want POST", got)
	}
	if got, want := transport.body, `{"inputs":[]}`; got != want {
		t.Errorf("forwarded payload %q, want it untouched at %q", got, want)
	}
	if got := recorder.Code; got != http.StatusOK {
		t.Errorf("the consumer received %d, want the backend's %d", got, http.StatusOK)
	}
	if got, want := recorder.Body.String(), `{"outputs":[]}`; got != want {
		t.Errorf("the consumer received %q, want the backend's response %q", got, want)
	}
}

func TestServeSendsAVersionedPathOnUnchanged(t *testing.T) {
	transport := &recordingTransport{}
	routes := staticRoutes{"mobilenet": serving.Weights{backend("jetsono", 1)}}

	handler(routes, transport, 0).ServeHTTP(
		httptest.NewRecorder(), inferenceRequest("/v2/models/mobilenet/versions/3/infer", "{}"))

	if transport.forwarded == nil {
		t.Fatal("nothing was forwarded")
	}
	if got, want := transport.forwarded.URL.Path, "/v2/models/mobilenet/versions/3/infer"; got != want {
		t.Errorf("forwarded path %q, want %q", got, want)
	}
}

func TestServeFollowsTheDraw(t *testing.T) {
	routes := staticRoutes{"mobilenet": serving.Weights{backend("jetsono", 1), backend("orangepi5", 1)}}

	for _, testCase := range []struct {
		name string
		pick uint64
		want string
	}{
		{name: "the first share", pick: 0, want: "chill-serving-jetsono.chill-system.svc:8000"},
		{name: "the second share", pick: 1, want: "chill-serving-orangepi5.chill-system.svc:8000"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &recordingTransport{}

			handler(routes, transport, testCase.pick).ServeHTTP(
				httptest.NewRecorder(), inferenceRequest("/v2/models/mobilenet/infer", "{}"))

			if transport.forwarded == nil {
				t.Fatal("nothing was forwarded")
			}
			if got := transport.forwarded.URL.Host; got != testCase.want {
				t.Errorf("forwarded to %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestServeRefusesWithoutForwarding(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		routes staticRoutes
		path   string
		want   int
	}{
		{
			name:   "a path that asks for no inference",
			routes: staticRoutes{"mobilenet": serving.Weights{backend("jetsono", 1)}},
			path:   "/v2/health/ready",
			want:   http.StatusNotFound,
		},
		{
			name:   "a model no node serves",
			routes: staticRoutes{},
			path:   "/v2/models/mobilenet/infer",
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "weights that cannot distribute a request",
			routes: staticRoutes{"mobilenet": serving.Weights{backend("jetsono", 0)}},
			path:   "/v2/models/mobilenet/infer",
			want:   http.StatusInternalServerError,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			transport := &recordingTransport{}
			recorder := httptest.NewRecorder()

			handler(testCase.routes, transport, 0).ServeHTTP(recorder, inferenceRequest(testCase.path, "{}"))

			if recorder.Code != testCase.want {
				t.Errorf("the consumer received %d, want %d", recorder.Code, testCase.want)
			}
			if transport.forwarded != nil {
				t.Errorf("the request was forwarded to %q, want it refused at the entry point",
					transport.forwarded.URL.Host)
			}
		})
	}
}
