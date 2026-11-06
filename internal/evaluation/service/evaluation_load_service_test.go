package service

import (
	"context"
	"errors"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type loadEvidence struct{ missing bool }

func (e loadEvidence) Stat(
	_ context.Context, location evidencestore.ObjectLocation,
) (evidencestore.ObjectIdentity, error) {
	if e.missing {
		return evidencestore.ObjectIdentity{}, errors.New("missing evidence")
	}
	return evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: 1}, nil
}
func loadRequest() LoadScenarioRequest {
	return LoadScenarioRequest{Name: "load", Spec: api.LoadScenarioSpec{
		Workloads: []api.EvaluationWorkloadReference{{Name: "work", UID: "work-uid", Generation: 1}},
		Parts: []api.LoadScenarioPart{{
			ID: "part", DurationSeconds: 1, Rates: map[string]api.EvaluationNonNegative{"work": 1},
		}},
		ArrivalDistribution: map[string]string{"work": "poisson"},
		Seeds:               map[string]api.EvaluationSeed{"work": "101"},
		DatasetRefs:         map[string]api.EvaluationObjectReference{"work": {Name: "dataset", UID: "dataset-uid"}},
		TraceRefs:           map[string]api.EvaluationEvidenceReference{"work": {Store: "s", Key: "trace", Size: 1}},
	}}
}
func loadService(t *testing.T, objects ...client.Object) *LoadService {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects = append(objects, &api.Dataset{ObjectMeta: metav1.ObjectMeta{
		Name: "dataset", Namespace: "ns", UID: "dataset-uid",
	}})
	return &LoadService{
		client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		namespace: "ns", evidence: loadEvidence{},
	}
}
func TestLoadPreflightLiveGenerationAndMissingEvidence(t *testing.T) {
	s := loadService(t, &api.Workload{ObjectMeta: metav1.ObjectMeta{
		Name: "work", Namespace: "ns", UID: "work-uid", Generation: 2,
	}})
	result, err := s.Preflight(context.Background(), loadRequest())
	if err != nil || result.LiveWorkloads["work"].Generation != 2 {
		t.Fatalf("live=%+v error=%v", result, err)
	}
	if !result.Checks[2].Passed {
		t.Fatal("same Workload UID with a newer generation must remain usable")
	}
	if _, err := s.Create(context.Background(), loadRequest(), "actor"); err != nil {
		t.Fatalf("create with current applied Workload: %v", err)
	}
	unversioned := loadRequest()
	unversioned.Name = "unversioned"
	unversioned.Spec.Workloads[0].Generation = 0
	if _, err := s.Create(context.Background(), unversioned, "actor"); err != nil {
		t.Fatalf("create without requested generation: %v", err)
	}
	unversioned.Name = "foreign-workload"
	unversioned.Spec.Workloads[0].UID = "replacement-workload"
	if _, err := s.Create(context.Background(), unversioned, "actor"); err == nil {
		t.Fatal("wrong Workload UID accepted")
	}
	s.evidence = loadEvidence{missing: true}
	result, err = s.Preflight(context.Background(), loadRequest())
	if err != nil || result.Checks[4].Passed {
		t.Fatalf("missing evidence accepted: %+v %v", result, err)
	}
}
func TestLoadInUseRejectsDeleteAndReplace(t *testing.T) {
	s := loadService(t,
		&api.Workload{ObjectMeta: metav1.ObjectMeta{
			Name: "work", Namespace: "ns", UID: "work-uid", Generation: 1,
		}},
		&api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "load", Namespace: "ns", UID: "old"}},
		&api.EvaluationRun{
			ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "ns"},
			Spec: api.EvaluationRunSpec{Stages: []api.StageTemplate{{
				LoadScenarioRef: api.EvaluationObjectReference{Name: "load", UID: "old"},
			}}},
		})
	if _, err := s.Delete(context.Background(), "load", "old"); err == nil {
		t.Fatal("deleted in-use scenario")
	}
	if _, err := s.Replace(context.Background(), "load", "old", loadRequest(), "actor"); err == nil {
		t.Fatal("replaced in-use scenario")
	}
}
func TestEvaluationInputsOnlyDeclaredTraces(t *testing.T) {
	load := &api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "load", Namespace: "ns"}, Spec: loadRequest().Spec}
	s := loadService(t, load)
	result, err := s.Inputs(context.Background())
	if err != nil || len(result) != 1 {
		t.Fatalf("inputs=%+v error=%v", result, err)
	}
	if got := result[0]; got.Set != "load" || got.Name != "load / work" ||
		got.Store != "s" || got.Key != "trace" || got.Size != 1 || got.Kind != "trace" {
		t.Fatalf("input metadata=%+v", got)
	}
}

type loadUIDClient struct{ client.Client }

func (c loadUIDClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	obj.SetUID("new")
	return c.Client.Create(ctx, obj, opts...)
}
func TestLoadReplacementGetsNewUID(t *testing.T) {
	s := loadService(t,
		&api.Workload{ObjectMeta: metav1.ObjectMeta{
			Name: "work", Namespace: "ns", UID: "work-uid", Generation: 1,
		}},
		&api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "load", Namespace: "ns", UID: "old"}},
	)
	s.client = loadUIDClient{s.client}
	request := loadRequest()
	request.Name = "different"
	if _, err := s.Replace(context.Background(), "load", "old", request, "actor"); err == nil {
		t.Fatal("mismatched body name accepted")
	}
	var original api.LoadScenario
	key := client.ObjectKey{Namespace: "ns", Name: "load"}
	if err := s.client.Get(context.Background(), key, &original); err != nil || original.UID != "old" {
		t.Fatalf("mismatch mutated original: uid=%s error=%v", original.UID, err)
	}
	request.Name = ""
	identity, err := s.Replace(context.Background(), "load", "old", request, "actor")
	if err != nil || identity.UID != "new" {
		t.Fatalf("replacement=%+v error=%v", identity, err)
	}
	var obj api.LoadScenario
	err = s.client.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "load"}, &obj)
	if err != nil || obj.Annotations["edge.dacs.io/requested-by"] != "actor" {
		t.Fatalf("replacement actor=%v error=%v", obj.Annotations, err)
	}
}
