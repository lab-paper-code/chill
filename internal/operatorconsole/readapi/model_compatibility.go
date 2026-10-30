package readapi

import (
	"context"
	"net/http"
	"net/url"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// RegisterModelCompatibilityQuery composes with the existing query mux. The
// reader owns semantic name/fact validation; this boundary checks query shape.
func RegisterModelCompatibilityQuery(
	mux *http.ServeMux,
	read func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error),
) {
	mux.HandleFunc("GET /api/v1/model-compatibility", func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.RawQuery) > 4096 {
			writeError(w, http.StatusBadRequest, "InvalidQuery")
			return
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(values) != 4 {
			writeError(w, http.StatusBadRequest, "InvalidQuery")
			return
		}
		for _, key := range []string{"modelSpec", "executionPath", "deviceClass", "node"} {
			if len(values[key]) != 1 || values.Get(key) == "" {
				writeError(w, http.StatusBadRequest, "InvalidQuery")
				return
			}
		}
		if read == nil {
			writeError(w, http.StatusServiceUnavailable, "ModelCompatibilityUnavailable")
			return
		}
		snapshot, err := read(r.Context(), operatorconsole.ModelCompatibilitySelection{
			ModelSpec: values.Get("modelSpec"), ExecutionPath: values.Get("executionPath"),
			DeviceClass: values.Get("deviceClass"), Node: values.Get("node"),
		})
		if err != nil {
			status := http.StatusServiceUnavailable
			switch {
			case apierrors.IsBadRequest(err):
				status = http.StatusBadRequest
			case apierrors.IsForbidden(err):
				status = http.StatusForbidden
			case apierrors.IsNotFound(err):
				status = http.StatusNotFound
			}
			writeError(w, status, "ModelCompatibilityUnavailable")
			return
		}
		writeJSON(w, operatorconsole.BuildModelCompatibility(snapshot))
	})
}
