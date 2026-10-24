package httpapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

type diagnosticDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
}
func (w *diagnosticDeadlineWriter) SetReadDeadline(at time.Time) error {
	w.deadline = at
	return nil
}

func TestDiagnosticInferenceWriterPreservesDeliveryAndController(t *testing.T) {
	d := serving.NewDiagnostics()
	d.Start()
	original := &diagnosticDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	writer := &diagnosticInferenceWriter{ResponseWriter: original, diagnostics: d, requestID: "0123456789abcdef0123456789abcdef"}
	controller := http.NewResponseController(writer)
	deadline := time.Now().Add(time.Second)
	if err := controller.SetReadDeadline(deadline); err != nil || !original.deadline.Equal(deadline) {
		t.Fatal("ResponseController lost the original deadline owner", err)
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusCreated)
	if n, err := writer.Write([]byte("{}")); n != 2 || err != nil { t.Fatal(n, err) }
	if err := flushInference(controller, d, writer.requestID); err != nil { t.Fatal(err) }
	if original.Code != http.StatusCreated || original.Body.String() != "{}" || !original.Flushed {
		t.Fatal("diagnostic delivery changed the HTTP response", original)
	}
	meta, err := d.WriteEvents(io.Discard)
	if err != nil || meta.Retained != 6 { t.Fatalf("actual WriteHeader/Write/Flush boundaries missing: %+v %v", meta, err) }
	if _, err = writer.Write([]byte("after")); err != nil { t.Fatal(err) }
	if err = flushInference(controller, d, writer.requestID); err != nil { t.Fatal(err) }
	if d.Active() || original.Body.String() != "{}after" { t.Fatal("post-window response changed or reactivated capture") }
}
