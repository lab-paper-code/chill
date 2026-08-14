// Package serving contains the Kubernetes-independent routing decision the
// inference entry point applies to one consumer request: what CHILL reads from
// the request, and which serving node receives it.
package serving

import (
	"errors"
	"fmt"
	"strings"
)

const inferencePathPrefix = "/v2/models/"

// Request is everything the entry point reads from an inbound inference
// request. The payload is absent on purpose: CHILL routes on the declared model
// and forwards the body untouched, so a backend receives the request the
// consumer would have sent it directly.
type Request struct {
	// Model is the model identity the consumer declared.
	Model string
	// Version is the model version the consumer pinned, empty when it pinned
	// none and the backend may serve whichever version it holds.
	Version string
}

// ParseInferencePath reads an Open Inference Protocol inference path, published
// as the KServe V2 predict protocol, in either of the two forms that protocol
// defines: /v2/models/{model}/infer and
// /v2/models/{model}/versions/{version}/infer.
//
// Every other path the protocol defines describes a single server rather than
// an inference, so it is not a request this decision applies to.
func ParseInferencePath(path string) (Request, error) {
	rest, isModelPath := strings.CutPrefix(path, inferencePathPrefix)
	if !isModelPath {
		return Request{}, fmt.Errorf("%q is not an inference path", path)
	}

	var request Request
	switch segments := strings.Split(rest, "/"); {
	case len(segments) == 2 && segments[1] == "infer":
		request = Request{Model: segments[0]}
	case len(segments) == 4 && segments[1] == "versions" && segments[3] == "infer":
		if segments[2] == "" {
			return Request{}, errors.New("an inference path pins an empty model version")
		}
		request = Request{Model: segments[0], Version: segments[2]}
	default:
		return Request{}, fmt.Errorf("%q is not an inference path", path)
	}

	if request.Model == "" {
		return Request{}, errors.New("an inference path names no model")
	}
	return request, nil
}
