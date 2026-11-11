package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestExpiredOperationCanDeliverTimeoutResponse(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			mux := http.NewServeMux()
			handler := &Handler{config: HandlerConfig{RequestTimeout: 200 * time.Millisecond, MaxRequestBytes: 1024}, mux: mux}
			mux.HandleFunc(method+" /slow", func(w http.ResponseWriter, r *http.Request) {
				if method == http.MethodPost {
					var body map[string]any
					if err := handler.decode(w, r, &body); err != nil {
						t.Error(err)
						return
					}
				}
				<-r.Context().Done()
				sendError(w, contextRequestError(r.Context().Err()))
				if err := http.NewResponseController(w).Flush(); err != nil {
					t.Errorf("timeout response flush failed: %v", err)
				}
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			var input io.Reader
			if method == http.MethodPost {
				input = strings.NewReader("{}")
			}
			request, err := http.NewRequest(method, server.URL+"/slow", input)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Timeout: 2 * time.Second}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			var body struct {
				Code string `json:"code"`
			}
			decodeErr := json.NewDecoder(response.Body).Decode(&body)
			closeErr := response.Body.Close()
			if decodeErr != nil || closeErr != nil ||
				response.StatusCode != http.StatusGatewayTimeout || body.Code != requestTimeoutCode {
				t.Fatalf("timeout response: status=%d code=%s decode=%v close=%v",
					response.StatusCode, body.Code, decodeErr, closeErr)
			}
		})
	}
}

func TestWorkloadAdmissionWriteDefaults(t *testing.T) {
	for _, tc := range []struct {
		input  string
		policy api.AdmissionPolicy
		queued int32
	}{
		{`{"spec":{}}`, api.AdmissionPolicyReject, 1024},
		{`{"spec":{"admission":{}}}`, api.AdmissionPolicyReject, 1024},
		{`{"spec":{"admission":{"policy":"Delay"}}}`, api.AdmissionPolicyDelay, 1024},
		{`{"spec":{"admission":{"maxQueuedRequests":1}}}`, api.AdmissionPolicyReject, 1},
		{`{"spec":{"admission":{"maxQueuedRequests":0}}}`, api.AdmissionPolicyReject, 0},
	} {
		var body struct {
			Spec api.WorkloadSpec `json:"spec"`
		}
		body.Spec.Admission = api.DefaultWorkloadAdmission()
		h := &Handler{config: HandlerConfig{MaxRequestBytes: 1024}}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/workloads", strings.NewReader(tc.input))
		if err := h.decode(httptest.NewRecorder(), r, &body); err != nil {
			t.Fatal(err)
		}
		if body.Spec.Admission.Policy != tc.policy || body.Spec.Admission.MaxQueuedRequests != tc.queued {
			t.Fatalf("decoded %s: %+v", tc.input, body.Spec.Admission)
		}
	}
}

func TestOverloadErrorResponse(t *testing.T) {
	zero, negative := int64(0), int64(-1)
	for _, tc := range []struct {
		code   string
		status int
	}{
		{serving.CodeQueueFull, http.StatusTooManyRequests},
		{serving.CodeDeadlineInfeasible, http.StatusServiceUnavailable},
	} {
		for _, stage := range []string{serving.AdmissionStageGateway, serving.AdmissionStageExecutor} {
			t.Run(tc.code+"/"+stage, func(t *testing.T) {
				cause := &serving.Error{
					Code: tc.code, Message: "internal detail", Field: "internal",
					Stage: stage, Policy: serving.AdmissionPolicyReject,
					ExecutorWaitNanos: &zero,
				}
				if stage == serving.AdmissionStageGateway {
					cause.PredictedCompletionNanos, cause.RemainingSLONanos = &zero, &negative
				}
				writer := httptest.NewRecorder()
				if err := writeError(writer, fmt.Errorf("wrapped: %w", cause)); err != nil {
					t.Fatal(err)
				}
				var body map[string]any
				if err := json.Unmarshal(writer.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if writer.Code != tc.status || body["code"] != tc.code ||
					body["stage"] != stage || body["policy"] != "Reject" {
					t.Fatalf("response status=%d body=%s", writer.Code, writer.Body.String())
				}
				if stage == serving.AdmissionStageGateway {
					if len(body) != 8 || body["message"] != "internal detail" || body["predictedCompletionNanos"] != float64(0) ||
						body["remainingSLONanos"] != float64(-1) {
						t.Fatalf("decision values lost: %s", writer.Body.String())
					}
				} else if len(body) != 6 || body["message"] != "internal detail" {
					t.Fatalf("absent decision values fabricated: %s", writer.Body.String())
				}
			})
		}
	}
}

func TestOrdinaryErrorResponsePreservesContract(t *testing.T) {
	writer := httptest.NewRecorder()
	cause := &serving.Error{Code: "InvalidInput", Message: "invalid value", Field: "input"}
	if err := writeError(writer, cause); err != nil {
		t.Fatal(err)
	}
	if writer.Code != http.StatusBadRequest || strings.TrimSpace(writer.Body.String()) !=
		`{"code":"InvalidInput","message":"invalid value","field":"input"}` {
		t.Fatalf("ordinary error contract changed: %d %s", writer.Code, writer.Body.String())
	}
}

func TestInferenceOverloadOutcome(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{serving.CodeQueueFull, http.StatusTooManyRequests},
		{serving.CodeDeadlineInfeasible, http.StatusServiceUnavailable},
	} {
		t.Run(tc.code, func(t *testing.T) {
			remaining, wait := int64(-1), int64(0)
			h, log := timingHandler(t, func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
				return serving.BackendResult{Finished: true}, &serving.Error{
					Code: tc.code, Message: "not executed", Stage: serving.AdmissionStageExecutor,
					Policy: serving.AdmissionPolicyReject, RemainingSLONanos: &remaining, ExecutorWaitNanos: &wait,
				}
			})
			request := httptest.NewRequest(http.MethodPost, "/api/workloads/workload/infer", strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer client")
			writer := httptest.NewRecorder()
			h.ServeHTTP(writer, request)
			row := timingRow(t, log, writer.Header().Get("X-Chill-Request-ID"))
			if writer.Code != tc.status || row.Outcome != "rejected" || row.Error != tc.code {
				t.Fatalf("overload status=%d row=%+v", writer.Code, row)
			}
			if row.Rejection == nil || row.Rejection.Code != tc.code ||
				row.Rejection.Stage != serving.AdmissionStageExecutor ||
				row.Rejection.Policy != serving.AdmissionPolicyReject ||
				row.Rejection.RemainingSLONanos == nil || *row.Rejection.RemainingSLONanos != remaining {
				t.Fatalf("rejection metadata lost: %+v", row.Rejection)
			}
			if !row.ExecutionComplete || row.BatchSize != 0 || row.ServiceDurationNanos != 0 {
				t.Fatal("rejection lost physical-settlement proof or fabricated a batch execution")
			}
		})
	}
}
