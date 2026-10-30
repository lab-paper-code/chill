package readapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	consolekube "github.com/lab-paper-code/chill/internal/operatorconsole/kube"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const compatibilityQuery = "modelSpec=model&executionPath=ort-cpu&deviceClass=class&node=node"

func TestModelCompatibilityHandlerRejectsInvalidQueries(t *testing.T) {
	calls := 0
	query, err := consolekube.NewKubernetesModelCompatibilityQuery(func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error) {
		calls++
		return operatorconsole.ModelCompatibilitySnapshot{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewQueryHandler(QueryReaders{ModelCompatibility: query.Read})
	for _, raw := range []string{
		"", "modelSpec=model", compatibilityQuery + "&node=node", compatibilityQuery + "&extra=1",
		strings.Replace(compatibilityQuery, "deviceClass=class", "deviceClass=", 1),
		strings.Replace(compatibilityQuery, "node=node", "node=Bad", 1),
		strings.Replace(compatibilityQuery, "executionPath=ort-cpu", "executionPath=+ort-cpu", 1),
		compatibilityQuery + "&bad=%zz", compatibilityQuery + "&" + strings.Repeat("x", 4096),
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/model-compatibility?"+raw, nil))
		if response.Code != http.StatusBadRequest {
			t.Fatalf("query %q: %d %s", raw, response.Code, response.Body.String())
		}
	}
	if calls != 0 {
		t.Fatal("invalid queries reached resolver")
	}
}

func TestModelCompatibilityHandlerClassifiesFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		want   int
		absent bool
	}{
		{"no reader", nil, 503, true},
		{"forbidden", apierrors.NewForbidden(schema.GroupResource{Resource: "modelspecs"}, "model", errors.New("secret error")), 403, false},
		{"not found", apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, "node"), 404, false},
		{"invalid reference", apierrors.NewBadRequest("foreign class Node"), 400, false},
		{"unavailable", errors.New("secret error"), 503, false},
		{"cancelled", context.Canceled, 503, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			readers := QueryReaders{}
			if !test.absent {
				readers.ModelCompatibility = func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error) {
					return operatorconsole.ModelCompatibilitySnapshot{}, test.err
				}
			}
			response := httptest.NewRecorder()
			NewQueryHandler(readers).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/model-compatibility?"+compatibilityQuery, nil))
			if response.Code != test.want || strings.Contains(response.Body.String(), "secret error") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestModelCompatibilityHandlerProjectsUnknownAndUTC(t *testing.T) {
	query, err := consolekube.NewKubernetesModelCompatibilityQuery(func(context.Context, operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error) {
		return operatorconsole.ModelCompatibilitySnapshot{
			Verdict: "Unknown", Reasons: []operatorconsole.ModelCompatibilityReason{{Kind: "Unknown", Code: "RuntimeDeclarationMissing", Message: "No trusted declaration for this Node"}},
		}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	NewQueryHandler(QueryReaders{ModelCompatibility: query.Read}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/model-compatibility?"+compatibilityQuery, nil))
	var view operatorconsole.ModelCompatibilityView
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	readAt, err := time.Parse(time.RFC3339Nano, view.ReadAt)
	if response.Code != 200 || err != nil || readAt.Location() != time.UTC || view.Selection.Node != "node" ||
		view.Verdict != "Unknown" || view.Inputs.RuntimeDeclaration != nil || len(view.Reasons) != 1 {
		t.Fatalf("response=%s, time error=%v", response.Body.String(), err)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("static facts may change; response must not be cached")
	}
	body, err := json.Marshal(operatorconsole.BuildModelCompatibility(operatorconsole.ModelCompatibilitySnapshot{Verdict: "Compatible"}))
	if err != nil || !strings.Contains(string(body), `"reasons":[]`) || !strings.Contains(string(body), `"runtimeDeclaration":null`) {
		t.Fatalf("empty collections/null contract: %s, %v", body, err)
	}
}
