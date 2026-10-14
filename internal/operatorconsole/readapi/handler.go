// Package readapi is the HTTP transport for the CHILL Console read surface.
// It owns routes, status classification and encoding; it owns no projection.
// Keeping it beside the service package rather than inside it is what stops a
// read model from being shaped by the protocol that happens to carry it.
package readapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	evidencePassportPath                                     = "GET /api/v1/namespaces/{namespace}/profiling-runs/{name}/evidence-passport"
	batchEnergyOptimizationResultPath                        = "GET /api/v1/namespaces/{namespace}/batch-energy-optimizations/{name}/result"
	batchEnergyOptimizationPreflightPath                     = "POST /api/v1/namespaces/{namespace}/batch-energy-optimizations/preflight"
	maximumPreflightRequestBytes                       int64 = 1 << 20
	batchEnergyOptimizationPreflightRequestInvalidCode       = "BatchEnergyOptimizationPreflightRequestInvalid"
)

type EvidencePassportReader interface {
	Read(context.Context, string, string) (operatorconsole.EvidencePassportSnapshot, error)
}

type BatchEnergyOptimizationResultReader interface {
	Read(context.Context, string, string) (operatorconsole.BatchEnergyOptimizationResultSnapshot, error)
}

type BatchEnergyOptimizationPreflightReader interface {
	Read(context.Context, *edgev1alpha1.BatchEnergyOptimization) (operatorconsole.BatchEnergyOptimizationPreflightSnapshot, error)
}

// NewHandler exposes a read-only Console API. Authentication and routing
// belong to the deployment boundary in front of this handler.
func NewHandler(
	passports EvidencePassportReader,
	results BatchEnergyOptimizationResultReader,
	preflights BatchEnergyOptimizationPreflightReader,
) (http.Handler, error) {
	if passports == nil || results == nil || preflights == nil {
		return nil, errors.New("Evidence Passport, batch-energy-optimization result, and batch-energy-optimization preflight readers are required")
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
			writeError(writer, queryStatus(err), "EvidencePassportUnavailable")
			return
		}
		passport, err := operatorconsole.BuildEvidencePassport(snapshot)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "EvidencePassportProjectionFailed")
			return
		}
		writeJSON(writer, passport)
	})
	mux.HandleFunc(batchEnergyOptimizationResultPath, func(writer http.ResponseWriter, request *http.Request) {
		snapshot, err := results.Read(
			request.Context(), request.PathValue("namespace"), request.PathValue("name"),
		)
		if err != nil {
			writeError(writer, queryStatus(err), "BatchEnergyOptimizationResultUnavailable")
			return
		}
		result, err := operatorconsole.BuildBatchEnergyOptimizationResult(snapshot)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "BatchEnergyOptimizationResultProjectionFailed")
			return
		}
		writeJSON(writer, result)
	})
	mux.HandleFunc(batchEnergyOptimizationPreflightPath, func(writer http.ResponseWriter, request *http.Request) {
		request.Body = http.MaxBytesReader(writer, request.Body, maximumPreflightRequestBytes)
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		var campaign edgev1alpha1.BatchEnergyOptimization
		if err := decoder.Decode(&campaign); err != nil {
			writeError(writer, http.StatusBadRequest, batchEnergyOptimizationPreflightRequestInvalidCode)
			return
		}
		if campaign.Kind != "" && campaign.Kind != "BatchEnergyOptimization" ||
			campaign.APIVersion != "" && campaign.APIVersion != edgev1alpha1.GroupVersion.String() {
			writeError(writer, http.StatusBadRequest, batchEnergyOptimizationPreflightRequestInvalidCode)
			return
		}
		namespace := request.PathValue("namespace")
		if campaign.Namespace == "" {
			campaign.Namespace = namespace
		} else if campaign.Namespace != namespace {
			writeError(writer, http.StatusBadRequest, batchEnergyOptimizationPreflightRequestInvalidCode)
			return
		}
		snapshot, err := preflights.Read(request.Context(), &campaign)
		if err != nil {
			writeError(writer, queryStatus(err), "BatchEnergyOptimizationPreflightUnavailable")
			return
		}
		preflight, err := operatorconsole.BuildBatchEnergyOptimizationPreflight(snapshot)
		if err != nil {
			writeError(writer, http.StatusInternalServerError, "BatchEnergyOptimizationPreflightProjectionFailed")
			return
		}
		writeJSON(writer, preflight)
	})
	return mux, nil
}

// queryStatus distinguishes a missing object from forbidden access or a failing dependency.
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

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(struct {
		Code string `json:"code"`
	}{Code: code})
}
