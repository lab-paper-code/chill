package main

import (
	"context"
	"errors"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
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
func (e loadEvidence) List(context.Context, string) ([]evidencestore.ObjectIdentity, error) {
	return []evidencestore.ObjectIdentity{
		{Store: "s", Key: "experiments/_inputs/set/bundle.json", Size: 1},
		{Store: "s", Key: "experiments/_inputs/not-a-set", Size: 1},
	}, nil
}
func loadRequest() httpapi.LoadScenarioRequest {
	return httpapi.LoadScenarioRequest{Name: "load", Spec: api.LoadScenarioSpec{
		Workloads: []api.EvaluationWorkloadReference{{Name: "work", UID: "work-uid", Generation: 1}},
		Parts: []api.LoadScenarioPart{{
			ID: "part", DurationSeconds: 1, Rates: map[string]api.EvaluationNonNegative{"work": 1},
		}},
		ArrivalDistribution: map[string]string{"work": "poisson"},
		SeedRef:             api.EvaluationEvidenceReference{Store: "s", Key: "seed", Size: 1},
		PayloadManifestRef:  api.EvaluationEvidenceReference{Store: "s", Key: "payload", Size: 1},
	}}
}
func loadService(t *testing.T, objects ...client.Object) *evaluationLoadService {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return &evaluationLoadService{
		client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build(),
		namespace: "ns", evidence: loadEvidence{}, lister: loadEvidence{},
	}
}
func TestLoadPreflightLiveGenerationAndMissingEvidence(t *testing.T) {
	s := loadService(t, &api.Workload{ObjectMeta: metav1.ObjectMeta{
		Name: "work", Namespace: "ns", UID: "work-uid", Generation: 2,
	}})
	result, err := s.preflight(context.Background(), loadRequest())
	if err != nil || result.LiveWorkloads["work"].Generation != 2 {
		t.Fatalf("live=%+v error=%v", result, err)
	}
	if result.Checks[2].Passed {
		t.Fatal("generation mismatch accepted")
	}
	s.evidence = loadEvidence{missing: true}
	result, err = s.preflight(context.Background(), loadRequest())
	if err != nil || result.Checks[3].Passed {
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
	if _, err := s.delete(context.Background(), "load", "old"); err == nil {
		t.Fatal("deleted in-use scenario")
	}
	if _, err := s.replace(context.Background(), "load", "old", loadRequest(), "actor"); err == nil {
		t.Fatal("replaced in-use scenario")
	}
}
func TestEvaluationInputKinds(t *testing.T) {
	for filename, want := range map[string]string{
		"bundle.json": "payload", "seeds.json": "seed", "rate-basis.json": "rateBasis",
		"kappa.json": "kappa", "not-seeds.json": "other", "trace.json": "other",
	} {
		if got := evaluationInputKind("experiments/_inputs/set/" + filename); got != want {
			t.Fatalf("%s: %s", filename, got)
		}
	}
	s := loadService(t)
	result, err := s.inputs(context.Background())
	if err != nil || len(result.Items) != 1 {
		t.Fatalf("inputs=%+v error=%v", result, err)
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
	if _, err := s.replace(context.Background(), "load", "old", request, "actor"); err == nil {
		t.Fatal("mismatched body name accepted")
	}
	var original api.LoadScenario
	key := client.ObjectKey{Namespace: "ns", Name: "load"}
	if err := s.client.Get(context.Background(), key, &original); err != nil || original.UID != "old" {
		t.Fatalf("mismatch mutated original: uid=%s error=%v", original.UID, err)
	}
	request.Name = ""
	identity, err := s.replace(context.Background(), "load", "old", request, "actor")
	if err != nil || identity.UID != "new" {
		t.Fatalf("replacement=%+v error=%v", identity, err)
	}
	var obj api.LoadScenario
	err = s.client.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "load"}, &obj)
	if err != nil || obj.Annotations["edge.dacs.io/requested-by"] != "actor" {
		t.Fatalf("replacement actor=%v error=%v", obj.Annotations, err)
	}
}
