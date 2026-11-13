package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	campaigncontroller "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/queryapi/readapi"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// These are the existing static compatibility/installation fixtures, not a
// serving profile or a reconstructed Evaluation authority. Public intent
// supplies no UID, historical profile, or frozen Workload identity.
func firstProfileInputs(t *testing.T) ([]client.Object, *api.BatchEnergyOptimization) {
	t.Helper()
	objects := validCandidateObjects()
	facts := objects[2].(*core.ConfigMap)
	for key, value := range validMetricSourceConfigMap().Data {
		facts.Data[key] = value
	}
	facts.Data["workload-observation.yaml"] = validWorkloadObservationFacts()
	facts.Data["execution-policies.yaml"] = string(controllerExecutionPolicyCatalog(t))
	node := validNode()
	node.Status.Allocatable[core.ResourceMemory] = resource.MustParse("8Gi")
	workload := &api.Workload{
		ObjectMeta: meta.ObjectMeta{Name: "first-profile", UID: "first-profile-workload", ResourceVersion: "1"},
		Spec: api.WorkloadSpec{
			ModelRef:        api.CatalogReference{Name: "mobilenet-v2-050"},
			PreprocessorRef: api.CatalogReference{Name: "mobilenet-imagenet"},
			DatasetRef:      &api.CatalogReference{Name: "vqav2-val-subset"},
			SLO:             api.WorkloadSLO{Percentile: 95, TargetLatency: meta.Duration{Duration: time.Second}},
		},
	}
	objects = append(objects, node, workload)
	objects = append(objects,
		&api.ChillSystem{ObjectMeta: meta.ObjectMeta{Name: "first-profile-system", Labels: map[string]string{"app.kubernetes.io/instance": "first-profile-installation"}}},
		&core.Pod{ObjectMeta: meta.ObjectMeta{Name: "first-profile-gateway", Namespace: testSystemNamespace, UID: "first-profile-gateway-uid",
			Labels: map[string]string{"app.kubernetes.io/instance": "first-profile-installation", "app.kubernetes.io/component": "serving-entrypoint"}},
			Spec: core.PodSpec{NodeName: "gateway-node"}, Status: core.PodStatus{Phase: core.PodRunning,
				Conditions: []core.PodCondition{{Type: core.PodReady, Status: core.ConditionTrue}}}},
	)
	observation := api.ProfilingRunMetricObservationSpec{IntervalMilliseconds: 1000, TimeoutMilliseconds: 500, DurationSeconds: 31}
	intent := &api.BatchEnergyOptimization{
		TypeMeta:   meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimization"},
		ObjectMeta: meta.ObjectMeta{Namespace: testSystemNamespace, Name: "first-profile"},
		Spec: api.BatchEnergyOptimizationSpec{
			State: api.MeasurementStateSpec{
				Candidate: api.ProfilingRunCandidateSpec{ModelSpec: workload.Spec.ModelRef.Name,
					Preprocessor: workload.Spec.PreprocessorRef.Name, DeviceClass: "lattepanda-3-delta-8g",
					ExecutionPath: "ort-cpu", PowerMode: "fixed"}, TargetNode: node.Name,
			},
			ActiveProtocol: api.ActiveProtocolSpec{Workload: workload.Name, Dataset: workload.Spec.DatasetRef.Name,
				WarmupIterations: 1, DurationSeconds: 30, MetricObservation: observation,
				OperatingPoints: api.MeasurementOperatingPointsSpec{Utilizations: []float64{.5}, SparseMeanGapMilliseconds: 1000,
					PhaseDurationSeconds: 30, PostLoadDurationSeconds: 30, RequestLifetimeSeconds: 30, DeliveryMarginMilliseconds: 100, FramedDecodedRequestUpperBytes: 1024}},
			IdleProtocol:   api.IdleProtocolSpec{SettlingDurationSeconds: 1, DurationSeconds: 30, MetricObservation: observation},
			TrialsPerBatch: 3,
		},
	}
	return objects, intent
}

func TestPublicFirstProfileResolvesWithNoServingOrEvaluationHistory(t *testing.T) {
	ctx := context.Background()
	objects, intent := firstProfileInputs(t)
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	candidates, err := NewKubernetesCandidateResolver(reader, candidateResolverOptions())
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := NewKubernetesNodeResolver(reader)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err := NewKubernetesMetricSourceResolver(reader, metricSourceResolverOptions())
	if err != nil {
		t.Fatal(err)
	}
	workloads, err := NewKubernetesWorkloadObservationResolver(reader, WorkloadObservationResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: "chill-profiler-facts", ConfigMapKey: "workload-observation.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	actuation, err := NewKubernetesActuationPolicyResolver(reader, ActuationPolicyResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: "chill-profiler-facts", ConfigMapKey: "actuation-policies.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := NewKubernetesExecutionPolicyResolver(reader, ExecutionPolicyResolverOptions{
		Namespace: testSystemNamespace, ConfigMapName: "chill-profiler-facts", ConfigMapKey: "execution-policies.yaml", MeasurementMemoryFraction: .8})
	if err != nil {
		t.Fatal(err)
	}
	preparer, err := profiler.NewPreparer(candidates, nodes, metrics, workloads, actuation, execution,
		func(ctx context.Context, targetNode string) (string, profilingrun.LoadOriginGatewayPodContract, error) {
			return ResolveGatewayLoadOrigin(ctx, reader, testSystemNamespace, "first-profile-system", targetNode)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	projector, err := campaigncontroller.NewCurvePlanProjector(reader, candidates, preparer)
	if err != nil {
		t.Fatal(err)
	}
	preflight, err := querykube.NewKubernetesBatchEnergyOptimizationPreflightQuery(reader,
		func(ctx context.Context, proposed *api.BatchEnergyOptimization) (derivation.CurvePlan, error) {
			plan, _, err := projector.Project(ctx, proposed)
			return plan, err
		})
	if err != nil {
		t.Fatal(err)
	}
	// Only preflight is exercised; the other real read-service types satisfy the
	// public router's unrelated GET routes and are never given fabricated data.
	handler, err := readapi.NewHandler(&querykube.KubernetesEvidencePassportQuery{},
		&querykube.KubernetesBatchEnergyOptimizationResultQuery{}, preflight)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost,
		"/api/v1/namespaces/"+intent.Namespace+"/batch-energy-optimizations/preflight", bytes.NewReader(payload)))
	var view queryapi.BatchEnergyOptimizationPreflight
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !view.Resolves {
		t.Fatalf("first public profile required serving/Evaluation history: HTTP%d %s", response.Code, response.Body.String())
	}
	snapshot, err := preflight.Read(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	shared := snapshot.Plan.Shared()
	if shared.ActiveProtocol.MinServiceSamples != 59 || shared.ActiveProtocol.SLOPercentile != 95 ||
		shared.ActiveProtocol.WorkloadUID != "first-profile-workload" || shared.Actor.RuntimeImage != testRuntimeImage {
		t.Fatalf("public first-profile plan lost real recipe/Workload stop facts: %+v", shared)
	}

	config := resolveAdmittedFirstProfile(t, ctx, intent, reader, preparer, execution)
	if config.Measurement.MinServiceSamples != 59 || config.Measurement.WorkloadUID != shared.ActiveProtocol.WorkloadUID ||
		config.Actor.RuntimeImage != shared.Actor.RuntimeImage || config.Actor.MemoryLimitBytes != shared.Actor.MemoryLimitBytes {
		t.Fatalf("standalone and public campaign disagree on real first-profile recipe: %+v", config)
	}

	writeFirstProfileWitness(t, scheme, objects, payload)
}

func resolveAdmittedFirstProfile(
	t *testing.T, ctx context.Context, intent *api.BatchEnergyOptimization, reader client.Reader,
	preparer profilercontroller.RunPreparer, execution profiler.ExecutionPolicyResolver,
) profilingrun.RunConfig {
	t.Helper()
	// Standalone execution uses actual admitted-root authority, never an
	// owner-reference shortcut or an invented campaign plan.
	standalone, err := campaigncontroller.BuildRun(intent,
		campaign.Measurement{WorkloadMode: campaign.WorkloadModeActive, Batch: 1}, 1, "")
	if err != nil {
		t.Fatal(err)
	}
	standalone.OwnerReferences = nil
	standalone.Name, standalone.UID = "standalone-first-profile", "standalone-root"
	standalone.Annotations = map[string]string{profilingrun.AdmissionAnnotation: profilingrun.GrantedAdmission(time.Unix(0, 0))}
	resolver, err := profilercontroller.NewRunConfigResolver(preparer, execution, reader,
		func(context.Context, types.UID) (derivation.CurvePlan, error) {
			return derivation.CurvePlan{}, fmt.Errorf("standalone first profile has no historical plan")
		})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolver.Resolve(ctx, standalone)
	if err != nil {
		t.Fatalf("admitted standalone first profile failed: %v", err)
	}
	unadmitted := standalone.DeepCopy()
	unadmitted.Annotations = nil
	if _, err := resolver.Resolve(ctx, unadmitted); err == nil {
		t.Fatal("no-history cutover removed standalone admission")
	}
	return resolved.Prepared.RunConfig
}

func writeFirstProfileWitness(t *testing.T, scheme *runtime.Scheme, objects []client.Object, payload []byte) {
	t.Helper()
	// The same public input exercises the unchanged production Query constructor
	// on the base; this optional test output is not a production setting.
	directory := os.Getenv("CHILL_FIRST_PROFILE_FIXTURE_OUTPUT")
	if directory == "" {
		return
	}
	if err := os.MkdirAll(directory, 0755); err != nil {
		t.Fatal(err)
	}
	items := make([]json.RawMessage, 0, len(objects))
	for _, object := range objects {
		kinds, _, err := scheme.ObjectKinds(object)
		if err != nil || len(kinds) == 0 {
			t.Fatalf("fixture kind: %v", err)
		}
		object.GetObjectKind().SetGroupVersionKind(kinds[0])
		raw, err := json.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, raw)
	}
	data, err := json.Marshal(struct {
		Items []json.RawMessage `json:"items"`
	}{items})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"objects.json": data, "intent.json": payload} {
		if err := os.WriteFile(filepath.Join(directory, name), body, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
