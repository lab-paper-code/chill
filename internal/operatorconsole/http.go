package operatorconsole

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const evidencePassportPath = "GET /api/v1/namespaces/{namespace}/profiling-runs/{name}/evidence-passport"

type EvidencePassportReader interface {
	Read(context.Context, string, string) (EvidencePassportSnapshot, error)
}

// NewHTTPHandler exposes a read-only Console API. Authentication and routing
// belong to the deployment boundary in front of this handler.
func NewHTTPHandler(reader EvidencePassportReader) (http.Handler, error) {
	if reader == nil {
		return nil, errors.New("Evidence Passport reader is required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	mux.HandleFunc(evidencePassportPath, func(writer http.ResponseWriter, request *http.Request) {
		snapshot, err := reader.Read(
			request.Context(), request.PathValue("namespace"), request.PathValue("name"),
		)
		if err != nil {
			writeConsoleError(writer, queryStatus(err), "EvidencePassportUnavailable")
			return
		}
		passport, err := BuildEvidencePassport(snapshot)
		if err != nil {
			writeConsoleError(writer, http.StatusInternalServerError, "EvidencePassportProjectionFailed")
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json")
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(passport); err != nil {
			return
		}
	})
	return mux, nil
}

func queryStatus(err error) int {
	switch {
	case apierrors.IsNotFound(err):
		return http.StatusNotFound
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

func writeConsoleError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}
