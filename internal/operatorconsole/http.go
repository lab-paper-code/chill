package operatorconsole

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	evidencePassportPath      = "GET /api/v1/namespaces/{namespace}/profiling-runs/{name}/evidence-passport"
	batchSaturationResultPath = "GET /api/v1/namespaces/{namespace}/batch-saturations/{name}/result"
)

type EvidencePassportReader interface {
	Read(context.Context, string, string) (EvidencePassportSnapshot, error)
}

type BatchSaturationResultReader interface {
	Read(context.Context, string, string) (BatchSaturationResultSnapshot, error)
}

// NewHTTPHandler exposes a read-only Console API. Authentication and routing
// belong to the deployment boundary in front of this handler.
func NewHTTPHandler(
	passports EvidencePassportReader,
	results BatchSaturationResultReader,
) (http.Handler, error) {
	if passports == nil || results == nil {
		return nil, errors.New("Evidence Passport and batch-saturation result readers are required")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("{\"status\":\"ok\"}\n"))
	})
	mux.HandleFunc(evidencePassportPath, func(writer http.ResponseWriter, request *http.Request) {
		snapshot, err := passports.Read(
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
		writeConsoleJSON(writer, passport)
	})
	mux.HandleFunc(batchSaturationResultPath, func(writer http.ResponseWriter, request *http.Request) {
		snapshot, err := results.Read(
			request.Context(), request.PathValue("namespace"), request.PathValue("name"),
		)
		if err != nil {
			writeConsoleError(writer, queryStatus(err), "BatchSaturationResultUnavailable")
			return
		}
		result, err := BuildBatchSaturationResult(snapshot)
		if err != nil {
			writeConsoleError(writer, http.StatusInternalServerError, "BatchSaturationResultProjectionFailed")
			return
		}
		writeConsoleJSON(writer, result)
	})
	return mux, nil
}

// queryStatus classifies a read failure. A campaign that has not published its
// terminal result is a missing sub-resource, not a failing dependency, so it
// answers Not Found rather than the Bad Gateway a store failure earns.
func queryStatus(err error) int {
	switch {
	case apierrors.IsNotFound(err), errors.Is(err, ErrBatchSaturationResultUnavailable):
		return http.StatusNotFound
	case apierrors.IsForbidden(err):
		return http.StatusForbidden
	default:
		return http.StatusBadGateway
	}
}

func writeConsoleJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeConsoleError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}
