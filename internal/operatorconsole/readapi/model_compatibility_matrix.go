package readapi

import "net/http"

const compatibilityUnknown = "Unknown"

func registerModelCompatibilityMatrix(mux *http.ServeMux, readers QueryReaders) {
	mux.HandleFunc("GET /api/v1/model-compatibility-matrix", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			writeError(w, http.StatusBadRequest, "InvalidQuery")
			return
		}
		if readers.ModelCompatibilityMatrix == nil {
			writeError(w, http.StatusServiceUnavailable, "ModelCompatibilityUnavailable")
			return
		}
		matrix, err := readers.ModelCompatibilityMatrix(r.Context())
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "ModelCompatibilityUnavailable")
			return
		}
		writeJSON(w, matrix)
	})
}
