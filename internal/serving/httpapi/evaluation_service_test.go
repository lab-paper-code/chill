package httpapi

import (
	"context"
	"encoding/json"
	"reflect"
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

func TestEvaluationViewRecordsProcedureHistory(t *testing.T) {
	start := metav1.Date(2026, 11, 4, 1, 0, 0, 0, time.UTC)
	finish := metav1.Date(2026, 11, 4, 1, 1, 0, 0, time.UTC)
	first := &api.EvaluationStage{
		ObjectMeta: metav1.ObjectMeta{Name: "z-stage", Namespace: "ns", UID: "first-uid"},
		Spec:       api.EvaluationStageSpec{StageID: "first"},
		Status: api.EvaluationStageStatus{
			CurrentProcedure: "stage", StartedAt: &start, GoAt: &start,
			ProvisionalD1: &api.EvaluationProvisionalD1{
				AsOf: "2026-11-04T01:00:59.123456789Z", ClosedThrough: "2026-11-04T01:00:00Z", Provisional: true,
				Workloads: map[string]api.EvaluationProvisionalRequests{
					"workload": {Offered: 2, DeadlineSuccess: 1, RequiredSuccessfulRank: 2, Outcomes: map[string]int64{"completed": 1},
						UnfinishedOrUnsent: 1, SLO: api.EvaluationRequestSLO{Percentile: 95, TargetLatencyNanos: 1},
						Reachable: true, Substages: []api.EvaluationProvisionalSubstage{}},
				},
				GeneratorQuality: map[string]api.EvaluationIssuanceFidelity{},
			},
			Procedures: []api.EvaluationProcedure{
				{Step: "factor", StartedAt: &start, FinishedAt: &finish, Reason: "RecordedReason", MeasurementVerdict: api.MeasurementPASS},
				{Step: "stage", StartedAt: &finish},
			},
			Conditions: []metav1.Condition{{
				Type: api.EvaluationConditionExecutionComplete, Status: metav1.ConditionTrue,
				Reason: "ExecutionRecorded", Message: "Recorded evidence", LastTransitionTime: finish,
			}},
			MeasurementVerdict: api.MeasurementPASS,
		},
	}
	second := &api.EvaluationStage{
		ObjectMeta: metav1.ObjectMeta{Name: "a-stage", Namespace: "ns", UID: "second-uid"},
		Spec:       api.EvaluationStageSpec{StageID: "second"},
	}
	run := &api.EvaluationRun{
		ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"},
		Status: api.EvaluationRunStatus{
			CurrentProcedure: "prepare",
			Procedures: []api.EvaluationProcedure{
				{Step: "prepare", StartedAt: &start, FinishedAt: &finish, Reason: "Prepared"},
			},
			ChildRefs: []api.EvaluationChildReference{
				{StageID: "first", EvaluationObjectReference: api.EvaluationObjectReference{Name: first.Name, UID: first.UID}},
				{StageID: "second", EvaluationObjectReference: api.EvaluationObjectReference{Name: second.Name, UID: second.UID}},
			},
		},
	}
	unreferenced := &api.EvaluationStage{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "ns", UID: "other-uid"}}
	for _, test := range []struct {
		name string
		run  *api.EvaluationRun
		want string
	}{
		{"recorded", run, `{
			"currentProcedure":"prepare",
			"procedures":[{"step":"prepare","startedAt":"2026-11-04T01:00:00Z","finishedAt":"2026-11-04T01:01:00Z","reason":"Prepared"}],
			"stages":[
				{"id":"first","name":"z-stage","uid":"first-uid","currentProcedure":"stage",
				 "conditions":[{"type":"ExecutionComplete","status":"True","lastTransitionTime":"2026-11-04T01:01:00Z","reason":"ExecutionRecorded","message":"Recorded evidence"}],
				 "measurementVerdict":"PASS",
				 "provisionalD1":{"asOf":"2026-11-04T01:00:59.123456789Z","closedThrough":"2026-11-04T01:00:00Z","provisional":true,
					"workloads":{"workload":{"offered":2,"deadlineSuccess":1,"requiredSuccessfulRank":2,"outcomes":{"completed":1},
						"unfinishedOrUnsent":1,"slo":{"percentile":95,"targetLatencyNanos":1},"reachable":true,"substages":[]}},"generatorQuality":{}},
				 "procedures":[
					{"step":"factor","startedAt":"2026-11-04T01:00:00Z","finishedAt":"2026-11-04T01:01:00Z","reason":"RecordedReason","measurementVerdict":"PASS"},
					{"step":"stage","startedAt":"2026-11-04T01:01:00Z","finishedAt":null}
				 ],"startedAt":"2026-11-04T01:00:00Z","finishedAt":null,"goAt":"2026-11-04T01:00:00Z"},
				{"id":"second","name":"a-stage","uid":"second-uid","conditions":null,"procedures":null,"startedAt":null,"finishedAt":null,"goAt":null}
			]
		}`},
		{"absent", &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: "empty", Namespace: "ns"}},
			`{"procedures":null,"stages":null}`},
		{"missing or replaced child", &api.EvaluationRun{
			ObjectMeta: metav1.ObjectMeta{Name: "stale", Namespace: "ns"},
			Status: api.EvaluationRunStatus{ChildRefs: []api.EvaluationChildReference{
				{StageID: "missing", EvaluationObjectReference: api.EvaluationObjectReference{Name: "missing", UID: "missing-uid"}},
				{StageID: "replaced", EvaluationObjectReference: api.EvaluationObjectReference{Name: first.Name, UID: "old-uid"}},
			}},
		}, `{"procedures":null,"stages":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := api.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			native := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test.run, first, second, unreferenced).Build()
			var config HandlerConfig
			ConfigureEvaluations(&config, evaluation.New(native, native, "ns", nil))
			detail, err := config.GetEvaluation(context.Background(), test.run.Name)
			if err != nil {
				t.Fatal(err)
			}
			list, err := config.ListEvaluations(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Items) != 1 {
				t.Fatalf("got %d runs, want 1", len(list.Items))
			}
			for _, view := range []EvaluationView{detail, list.Items[0]} {
				body, err := json.Marshal(view)
				if err != nil {
					t.Fatal(err)
				}
				var got, want map[string]any
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(test.want), &want); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"currentProcedure", "procedures", "stages"} {
					value, exists := got[key]
					expected, wantExists := want[key]
					if exists != wantExists || !reflect.DeepEqual(value, expected) {
						t.Fatalf("%s = %#v, want %#v; body=%s", key, value, expected, body)
					}
				}
			}
		})
	}
}

func TestEvaluationCollectionEnvelopesKeepEmptyArrays(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	native := fake.NewClientBuilder().WithScheme(scheme).Build()
	var config HandlerConfig
	ConfigureEvaluations(&config, evaluation.New(native, native, "ns", nil))
	ConfigureLoadScenarios(&config, evaluation.NewLoadService(native, "ns", nil))
	ctx := context.Background()
	for name, list := range map[string]func() (any, error){
		"active":      func() (any, error) { return config.ListActiveEvaluations(ctx) },
		"evaluations": func() (any, error) { return config.ListEvaluations(ctx) },
		"loads":       func() (any, error) { return config.ListLoadScenarios(ctx) },
		"inputs":      func() (any, error) { return config.ListEvaluationInputs(ctx) },
	} {
		t.Run(name, func(t *testing.T) {
			value, err := list()
			if err != nil {
				t.Fatal(err)
			}
			body, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil {
				t.Fatal(err)
			}
			if string(fields["items"]) != "[]" {
				t.Fatalf("empty collection changed: %s", body)
			}
		})
	}
}
