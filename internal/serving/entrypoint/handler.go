// Package entrypoint serves CHILL's routing decision over HTTP at the one point
// every external inference request crosses.
package entrypoint

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httputil"
	"net/url"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Routes reports the routing weights a model is served with at the moment a
// request arrives. Absence means no node serves that model, which a drain of
// every node holding it produces.
type Routes interface {
	WeightsFor(model string) (serving.Weights, bool)
}

// Handler forwards one inference request to the node the weights chose. It adds
// transport and nothing else: the request reaches the backend with its method,
// path, and payload intact, and the backend's response reaches the consumer
// unread, so neither end observes that a choice happened.
type Handler struct {
	// Routes supplies the weights in force when a request arrives.
	Routes Routes
	// Namespace holds the per-node Services those weights name.
	Namespace string
	// Port is the port those Services expose.
	Port int
	// Draw returns the value the weighted choice is reduced from. A nil Draw
	// takes one from the runtime's random source.
	Draw func() uint64
	// Transport carries the forwarded request. A nil Transport uses the
	// runtime default.
	Transport http.RoundTripper
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	inference, err := serving.ParseInferencePath(request.URL.Path)
	if err != nil {
		http.Error(writer, err.Error(), http.StatusNotFound)
		return
	}

	weights, served := h.Routes.WeightsFor(inference.Model)
	if !served {
		http.Error(writer, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}

	backend, err := weights.Choose(h.draw())
	if err != nil {
		http.Error(writer, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	proxy := httputil.NewSingleHostReverseProxy(h.target(backend))
	proxy.Transport = h.Transport
	proxy.ServeHTTP(writer, request)
}

// target addresses the chosen node's Service by the name Kubernetes resolves it
// under. The Service carries a single endpoint, so the cluster network has
// nothing left to select and the choice made here is the one that holds.
func (h *Handler) target(backend serving.Backend) *url.URL {
	return &url.URL{
		Scheme: "http",
		Host:   fmt.Sprintf("%s.%s.svc:%d", backend.Service, h.Namespace, h.Port),
	}
}

func (h *Handler) draw() uint64 {
	if h.Draw != nil {
		return h.Draw()
	}
	return rand.Uint64()
}
