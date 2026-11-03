package httpapi

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	evaluation "github.com/lab-paper-code/chill/internal/evaluation/service"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEvaluationViewFlattensStatus(t *testing.T) {
	now := metav1.NewTime(time.Now().UTC())
	run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"}}
	run.Status = api.EvaluationRunStatus{
		CurrentStage: "stage", StartedAt: &now, FinishedAt: &now,
		Conditions: []metav1.Condition{{Type: api.EvaluationConditionPublished, Status: metav1.ConditionTrue}},
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
	s := evaluation.New(native, native, "ns", nil)
	var config HandlerConfig
	ConfigureEvaluations(&config, s)
	detail, err := config.GetEvaluation(context.Background(), run.Name)
	if err != nil {
		t.Fatal(err)
	}
	list, err := config.ListEvaluations(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range []EvaluationView{detail, list.Items[0]} {
		body, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"conditions", "currentStage", "startedAt", "finishedAt", "liveTarget"} {
			if len(fields[key]) == 0 {
				t.Fatalf("missing top-level %s: %s", key, body)
			}
		}
		if _, exists := fields["status"]; exists {
			t.Fatal("nested status leaked into projection")
		}
	}
}

func TestEvaluationViewProjectsOnlyRecordedLiveTarget(t *testing.T) {
	recorded := `[{"component":"server","resource":` +
		`{"apiVersion":"apps/v1","kind":"Deployment","namespace":"ns","name":"server"},` +
		`"liveImageIDs":["recorded-image"],"ready":false}]`
	for _, test := range []struct{ name, annotation, want string }{
		{"absent", "", "null"},
		{"invalid JSON", "[", "null"},
		{"wrong shape", `{"component":"server"}`, "null"},
		{"partial decode", `[{"component":"server"},{"ready":42}]`, "null"},
		{"recorded null", "null", "null"},
		{"recorded empty", "[]", "[]"},
		{"recorded identity", recorded, recorded},
	} {
		t.Run(test.name, func(t *testing.T) {
			run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{
				Annotations: map[string]string{"edge.dacs.io/evaluation-live-target": test.annotation},
			}}
			run.Name, run.Namespace = "run", "ns"
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
			s := evaluation.New(native, native, "ns", nil)
			value, err := s.Get(context.Background(), "run")
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(evaluationViewDTO(value))
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["liveTarget"]) != test.want {
				t.Fatalf("liveTarget=%s want=%s", fields["liveTarget"], test.want)
			}
		})
	}
}
