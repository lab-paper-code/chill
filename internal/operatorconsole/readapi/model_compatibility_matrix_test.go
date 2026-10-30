package readapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	consolekube "github.com/lab-paper-code/chill/internal/operatorconsole/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCompatibilityMatrixSharesPowerModesAndPreservesUnknown(t *testing.T) {
	inventory := operatorconsole.InventorySnapshot{
		ReadAt: time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC),
		Models: []edgev1alpha1.ModelSpec{{ObjectMeta: metav1.ObjectMeta{Name: "model", UID: "model-uid"}, Spec: edgev1alpha1.ModelSpecSpec{ExecutionPaths: []edgev1alpha1.ModelExecutionPath{{Name: "cpu"}, {Name: "npu"}}}}},
		Classes: []deviceclass.InventoryClass{
			{Name: "jetson", UID: "class-uid", Nodes: []deviceclass.InventoryNode{{Name: "z-node"}, {Name: "a-node"}}},
			{Name: "no-node", UID: "empty-uid"},
		},
	}
	calls := 0
	handler := NewQueryHandler(QueryReaders{
		ModelCompatibilityMatrix: func(ctx context.Context) (operatorconsole.ModelCompatibilityMatrixView, error) {
			return consolekube.BuildModelCompatibilityMatrix(ctx, inventory, func(_ context.Context, selection operatorconsole.ModelCompatibilitySelection) (operatorconsole.ModelCompatibilitySnapshot, error) {
				calls++
				if selection.ExecutionPath == "npu" {
					return operatorconsole.ModelCompatibilitySnapshot{}, errors.New("secret lookup detail")
				}
				return operatorconsole.ModelCompatibilitySnapshot{ReadAt: inventory.ReadAt, Selection: selection, Verdict: "Compatible"}, nil
			})
		},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/model-compatibility-matrix", nil))
	var body struct {
		Rows     []operatorconsole.ModelCompatibilityView `json:"rows"`
		NodeRows []operatorconsole.ModelCompatibilityView `json:"nodeRows"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != 200 || calls != 4 || len(body.Rows) != 4 || len(body.NodeRows) != 4 {
		t.Fatalf("status=%d calls=%d rows=%d nodeRows=%d", response.Code, calls, len(body.Rows), len(body.NodeRows))
	}
	if body.Rows[0].Verdict != "Compatible" ||
		body.Rows[1].Verdict != compatibilityUnknown || body.Rows[2].Verdict != compatibilityUnknown {
		t.Fatalf("verdicts: %+v", body.Rows)
	}
	if body.Rows[2].Reasons[0].Code != "CompatibilityReadFailed" {
		t.Fatalf("failure not preserved: %+v", body.Rows[2])
	}
	if body.Rows[0].Selection.Node != "a-node" || body.NodeRows[0].Selection.Node != "z-node" || body.NodeRows[1].Selection.Node != "a-node" {
		t.Fatalf("representative/actual Node rows: %+v / %+v", body.Rows, body.NodeRows)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("matrix must not be cached")
	}
	for _, target := range []string{"/api/v1/model-compatibility-matrix?node=a-node", "/api/v1/model-compatibility-matrix?x=%zz"} {
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != 400 {
			t.Fatalf("query accepted: %s", target)
		}
	}
}

func TestCompatibilityMatrixUnavailableIsNotEmptySuccess(t *testing.T) {
	response := httptest.NewRecorder()
	NewQueryHandler(QueryReaders{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/model-compatibility-matrix", nil))
	if response.Code != 503 {
		t.Fatalf("status = %d", response.Code)
	}
}
