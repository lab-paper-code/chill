package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoadScenarioWriteAuthorization(t *testing.T) {
	called := false
	h := experimentHandler(t, false, HandlerConfig{CreateLoadScenario: func(context.Context, LoadScenarioRequest, string) (EvaluationIdentity, error) {
		called = true
		return EvaluationIdentity{}, nil
	}})
	request := httptest.NewRequest(http.MethodPost, "/api/v1/evaluations/load-scenarios", strings.NewReader(`{"name":"load","spec":{}}`))
	request.Header.Set("Authorization", "Bearer caller")
	response := httptest.NewRecorder()
	h.evaluationLoads(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("status=%d mutation=%v", response.Code, called)
	}
}
