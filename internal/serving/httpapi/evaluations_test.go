package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestActiveEvaluationsAuthorization(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		calls := 0
		h := experimentHandler(t, allowed, HandlerConfig{ListActiveEvaluations: func(context.Context) (ActiveEvaluationList, error) {
			calls++
			return ActiveEvaluationList{Items: []ActiveEvaluation{}}, nil
		}})
		request := httptest.NewRequest(http.MethodGet, "/api/v1/evaluations/active", nil)
		request.Header.Set("Authorization", "Bearer caller")
		response := httptest.NewRecorder()
		h.activeEvaluations(response, request)
		if allowed {
			if response.Code != http.StatusOK || response.Body.String() != "{\"items\":[]}\n" || calls != 1 {
				t.Fatalf("code=%d body=%s calls=%d", response.Code, response.Body.String(), calls)
			}
		} else if response.Code != http.StatusForbidden || calls != 0 {
			t.Fatalf("code=%d calls=%d", response.Code, calls)
		}
	}
}
