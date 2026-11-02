package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestLoadScenarioWriteAuthorization(t *testing.T) {
	called := false
	config := HandlerConfig{}
	config.CreateLoadScenario = func(context.Context, LoadScenarioRequest, string) (EvaluationIdentity, error) {
		called = true
		return EvaluationIdentity{}, nil
	}
	h := experimentHandler(t, false, config)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/evaluations/load-scenarios",
		strings.NewReader(`{"name":"load","spec":{}}`))
	request.Header.Set("Authorization", "Bearer caller")
	response := httptest.NewRecorder()
	h.evaluationLoads(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("status=%d mutation=%v", response.Code, called)
	}
}

func TestEvaluationInputsPaging(t *testing.T) {
	items := []EvaluationInput{
		{Key: "a/bundle.json", Set: "a", Kind: "payload"},
		{Key: "a/seeds.json", Set: "a", Kind: "seed"},
		{Key: "b/seeds.json", Set: "b", Kind: "seed"},
	}
	config := HandlerConfig{ListEvaluationInputs: func(context.Context) (EvaluationInputList, error) {
		return EvaluationInputList{Items: items}, nil
	}}
	h := experimentHandler(t, true, config)
	for _, tc := range []struct {
		query                       string
		total, count, offset, limit int
		next                        bool
	}{
		{"", 3, 3, 0, 25, false},
		{"?kind=seed&limit=1", 2, 1, 0, 1, true},
		{"?kind=seed&limit=1&offset=1", 2, 1, 1, 1, false},
		{"?kind=seed&offset=2", 2, 0, 2, 25, false},
		{"?offset=9", 3, 0, 9, 25, false},
		{"?set=a&kind=seed", 1, 1, 0, 25, false},
		{"?set=missing", 0, 0, 0, 25, false},
	} {
		t.Run(tc.query, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/evaluations/inputs"+tc.query, nil)
			request.Header.Set("Authorization", "Bearer caller")
			response := httptest.NewRecorder()
			h.evaluationLoads(response, request)
			var got EvaluationInputList
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Page.Total != tc.total || len(got.Items) != tc.count || got.Page.Offset != tc.offset ||
				got.Page.Limit != tc.limit || (got.Page.NextOffset != nil) != tc.next {
				t.Fatalf("unexpected page: %+v", got)
			}
			if tc.next && *got.Page.NextOffset != tc.offset+tc.count {
				t.Fatalf("nextOffset=%d", *got.Page.NextOffset)
			}
		})
	}
	for _, query := range []string{
		"kind=unknown", "kind=", "kind=seed&kind=payload", "set=", "set=..", "set=a/b",
		"limit=0", "limit=51", "offset=-1", "offset=1000001", "offset=x", "unknown=x",
	} {
		t.Run(query, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/v1/evaluations/inputs?"+query, nil)
			request.Header.Set("Authorization", "Bearer caller")
			response := httptest.NewRecorder()
			h.evaluationLoads(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
		})
	}
}

func TestEvaluationInputsWithoutPagingReturnsWholeList(t *testing.T) {
	items := make([]EvaluationInput, 30)
	for i := range items {
		items[i] = EvaluationInput{Kind: "seed", Set: "demo"}
	}
	for _, query := range []url.Values{{}, {"kind": {"seed"}, "set": {"demo"}}} {
		got, err := pageEvaluationInputs(EvaluationInputList{Items: items}, query)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Items) != len(items) || got.Page.Total != len(items) || got.Page.NextOffset != nil {
			t.Fatalf("whole-list query %v returned %+v", query, got)
		}
	}
	got, err := pageEvaluationInputs(EvaluationInputList{Items: items}, url.Values{"offset": {"0"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 25 || got.Page.Total != 30 || got.Page.NextOffset == nil || *got.Page.NextOffset != 25 {
		t.Fatalf("explicit paging returned %+v", got)
	}
}
