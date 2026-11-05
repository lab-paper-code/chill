package kube_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	campaign "github.com/lab-paper-code/chill/internal/profiling/campaign"
	campaigncontroller "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	"github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	"github.com/lab-paper-code/chill/internal/serving"
	servingfirstfloor "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Exercise public BEO -> real child projection -> real preparer -> retained
// Kubernetes authority -> the production S3 Reader/List composition -> gzip
// native/final join -> frozen run. Only external Kubernetes/S3 and candidate
// installation facts are test boundaries; cadence resolution is not mocked.
func TestPublicCampaignWithoutCadenceFreezesActualServerTrace(t *testing.T) {
	config, candidate := servingCadenceRunFixture(t)
	resolver, data := retainedServingCadenceFixture(t, config)
	ports := cadencePreparationFacts{config: config, candidate: candidate}
	preparer, err := profiler.NewPreparer(ports, ports, ports, ports, ports, ports, resolver.ResolveCadence)
	if err != nil {
		t.Fatal(err)
	}
	parent := cadencePublicCampaign(t, config)
	run, err := campaigncontroller.BuildRun(parent, campaign.Measurement{WorkloadMode: campaign.WorkloadModeActive, Batch: 1}, 1, config.State.Backend)
	if err != nil {
		t.Fatal(err)
	}
	request, err := profilercontroller.ProjectRequest(run.Spec)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := preparer.Prepare(context.Background(), request.Preparation)
	if err != nil {
		t.Fatal(err)
	}
	zero, native, proxy := int64(0), int64(73), int64(1000000)
	consumer, err := firstfloor.NewServiceDistribution([]firstfloor.ServiceSample{
		{ServiceDurationNanos: 1000, PrecedingIdleNanos: &zero, TrialID: "target-trial"},
		{ServiceDurationNanos: 7000000, PrecedingIdleNanos: &native, TrialID: "target-trial"},
		{ServiceDurationNanos: 30000000, PrecedingIdleNanos: &proxy, TrialID: "target-trial"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index := range prepared.RunConfig.Measurement.Cadence.Provenance.Draws {
		draw := prepared.RunConfig.Measurement.Cadence.Draw(uint64(index))
		service, _, ok := consumer.QuantileNanos(&draw.TargetIdleNanos, 95)
		if !ok || service != 7000000 {
			t.Fatalf("public request selected zero-gap/actor-proxy or foreign source service: %dns", service)
		}
	}
	// A new public request must resolve its source again; owned child recovery
	// is covered with actual admitted root/immutable-plan fixtures in controller.
	for key := range data {
		delete(data, key)
	}
	_, err = preparer.Prepare(context.Background(), request.Preparation)
	var prerequisite *profiler.BootstrapServingTraceError
	if !errors.As(err, &prerequisite) || !errors.Is(err, profiler.ErrBootstrapServingTraceRequired) {
		t.Fatalf("new request hid bootstrap absence: %v", err)
	}
}

func TestRetainedCadenceRejectsAbsentBootstrapAndNonGzipArtifact(t *testing.T) {
	config, _ := servingCadenceRunFixture(t)
	resolver, data := retainedServingCadenceFixture(t, config)
	for key := range data {
		if strings.HasSuffix(key, "/requests.jsonl.gz") {
			data[key] = []byte("{\"kind\":\"final\"}\n")
		}
	}
	if _, err := resolver.ResolveCadence(context.Background(), config); err == nil || !strings.Contains(err.Error(), "gzip") {
		t.Fatalf("non-gzip artifact acquired an invented fallback: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	empty, err := profilerkube.NewKubernetesCadenceResolver(reader, "system", func(context.Context, *api.EvaluationRun) (evidencestore.Reader, error) {
		return nil, errors.New("no retained source")
	}, cadenceUnavailableArchive{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = empty.ResolveCadence(context.Background(), config)
	var prerequisite *profiler.BootstrapServingTraceError
	if !errors.As(err, &prerequisite) {
		t.Fatalf("bootstrap absence was not explicit: %v", err)
	}
}

func TestRetainedCadenceZeroLoadSeparatorIsNotAPacingPopulation(t *testing.T) {
	config, _ := servingCadenceRunFixture(t)
	workload := config.Measurement.WorkloadName
	resolver, _ := retainedServingCadenceFixture(t, config,
		api.LoadScenarioPart{ID: "low", DurationSeconds: 1, Rates: map[string]api.EvaluationNonNegative{workload: 2}},
		api.LoadScenarioPart{ID: "separator", DurationSeconds: 1, Rates: map[string]api.EvaluationNonNegative{workload: 0}},
		api.LoadScenarioPart{ID: "high", DurationSeconds: 1, Rates: map[string]api.EvaluationNonNegative{workload: 4}},
	)
	contracts, err := resolver.ResolveCadence(context.Background(), config)
	if err != nil {
		t.Fatalf("an actual zero-load separator forced bootstrap: %v", err)
	}
	short, long, separator := int64(73), int64(900), int64(1100000000)
	consumer, err := firstfloor.NewServiceDistribution([]firstfloor.ServiceSample{
		{ServiceDurationNanos: 7000000, PrecedingIdleNanos: &short, TrialID: "target-trial"},
		{ServiceDurationNanos: 30000000, PrecedingIdleNanos: &long, TrialID: "target-trial"},
		{ServiceDurationNanos: 900000000, PrecedingIdleNanos: &separator, TrialID: "target-trial"},
	})
	if err != nil {
		t.Fatal(err)
	}
	low, high := 0, 0
	contract := contracts[config.Measurement.Batch]
	for index := range contract.Provenance.Draws {
		draw := contract.Draw(uint64(index))
		service, _, ok := consumer.QuantileNanos(&draw.TargetIdleNanos, 95)
		if !ok {
			t.Fatal("selected phase lost its actual conditional service population")
		}
		switch service {
		case 7000000:
			low++
		case 30000000:
			high++
		default:
			t.Fatalf("source adjacency crossed the zero-load separator: service=%dns", service)
		}
	}
	if low == 0 || high == 0 {
		t.Fatalf("cursor did not preserve both actual offered phases: low=%d high=%d", low, high)
	}
}

func TestRetainedCadenceExcludesOnlyDefectiveRun(t *testing.T) {
	const unrelatedMissingScenario = "unrelated-missing"
	for _, defect := range []string{
		unrelatedMissingScenario, "unrelated-scenario-uid", "unrelated-stage-missing",
		"unrelated-snapshot-type", "unrelated-snapshot-syntax",
		"matching-snapshot-type", "matching-snapshot-syntax",
		"unrelated-snapshot-absent", "unrelated-snapshot-run",
		"matching-scenario-missing", "matching-scenario-uid", "matching-extra-scenario-missing",
		"matching-snapshot-ref", "matching-frozen-config", "matching-snapshot-absent",
		"matching-snapshot-run", "matching-snapshot-schema", "matching-snapshot-scenario",
		"matching-missing", "matching-uid", "matching-owner", "matching-snapshot",
	} {
		t.Run(defect, func(t *testing.T) {
			config, _ := servingCadenceRunFixture(t)
			resolver, _ := retainedServingCadenceFixtureWith(t, config, nil, func(reader client.Client, data map[string][]byte) {
				ctx := context.Background()
				var original api.EvaluationRun
				if err := reader.Get(ctx, types.NamespacedName{Namespace: "system", Name: "evaluation"}, &original); err != nil {
					t.Fatal(err)
				}
				broken := original.DeepCopy()
				broken.Name, broken.UID, broken.ResourceVersion = "broken-newer", "broken-newer-run", ""
				finished := metav1.NewTime(original.Status.FinishedAt.Add(time.Hour))
				broken.Status.FinishedAt = &finished
				broken.Status.ChildRefs[0].Name = "broken-stage"
				broken.Status.ChildRefs[0].UID = "broken-stage-uid"
				ref := brokenCadenceSnapshot(t, data, &original, broken, config, defect)
				if defect == unrelatedMissingScenario || defect == "unrelated-scenario-uid" || defect == "unrelated-stage-missing" {
					scenario := &api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: "system", UID: "unrelated"}, Spec: api.LoadScenarioSpec{Workloads: []api.EvaluationWorkloadReference{{Name: "unrelated-u", UID: "unrelated-u", Generation: 1}}}}
					broken.Spec.Stages[0].LoadScenarioRef = api.EvaluationObjectReference{Name: scenario.Name, UID: scenario.UID}
					if err := reader.Create(ctx, scenario); err != nil {
						t.Fatal(err)
					}
					if defect == unrelatedMissingScenario {
						if err := reader.Delete(ctx, scenario); err != nil {
							t.Fatal(err)
						}
					} else if defect == "unrelated-scenario-uid" {
						broken.Spec.Stages[0].LoadScenarioRef.UID = "previous-unrelated-uid"
					}
				}
				switch defect {
				case "matching-scenario-missing":
					broken.Spec.Stages[0].LoadScenarioRef.Name = "missing-scenario"
				case "matching-scenario-uid":
					broken.Spec.Stages[0].LoadScenarioRef.UID = "previous-scenario-uid"
				case "matching-extra-scenario-missing":
					broken.Spec.Stages = append(broken.Spec.Stages, api.StageTemplate{ID: "missing-stage", LoadScenarioRef: api.EvaluationObjectReference{Name: "missing-scenario", UID: "missing-scenario"}})
				case "matching-snapshot-ref":
					broken.Status.InputSnapshotRef = nil
				case "matching-frozen-config":
					broken.Status.FrozenConfig = nil
				case "matching-snapshot-absent", "unrelated-snapshot-absent":
					delete(data, ref.Store+"/"+ref.Key)
				}
				if err := reader.Create(ctx, broken); err != nil {
					t.Fatal(err)
				}
				if defect == unrelatedMissingScenario || defect == "unrelated-stage-missing" || defect == "matching-missing" {
					return
				}
				var stage api.EvaluationStage
				if err := reader.Get(ctx, types.NamespacedName{Namespace: "system", Name: "stage"}, &stage); err != nil {
					t.Fatal(err)
				}
				stage.Name, stage.UID, stage.ResourceVersion = "broken-stage", "broken-stage-uid", ""
				stage.OwnerReferences[0].Name, stage.OwnerReferences[0].UID = broken.Name, broken.UID
				stage.Spec.InputSnapshotRef = ref
				stage.Status.FinishedAt = &finished
				switch defect {
				case "matching-uid":
					stage.UID = "replaced-stage"
				case "matching-owner":
					stage.OwnerReferences[0].UID = "foreign-run"
				case "matching-snapshot":
					stage.Spec.InputSnapshotRef = *original.Status.InputSnapshotRef
				}
				if err := reader.Create(ctx, &stage); err != nil {
					t.Fatal(err)
				}
			})
			contracts, err := resolver.ResolveCadence(context.Background(), config)
			if err != nil {
				t.Fatalf("defective Run poisoned the intact matching population: %v", err)
			}
			assertIntactCadenceSource(t, contracts, config.Measurement.Dataset.Name)
		})
	}
}

func TestRetainedCadenceUsesProducerDatasetNotAppliedWorkloadDataset(t *testing.T) {
	config, _ := servingCadenceRunFixture(t)
	resolver, _ := retainedServingCadenceFixtureWith(t, config, func(scenario *api.LoadScenario) {
		scenario.Spec.DatasetRefs[config.Measurement.WorkloadName] = api.EvaluationObjectReference{Name: "actual-producer-dataset-b", UID: "actual-producer-dataset-b-uid"}
		if err := scenario.Spec.Validate(); err != nil {
			t.Fatalf("distinct producer dataset must remain a valid scenario: %v", err)
		}
	}, nil)
	_, err := resolver.ResolveCadence(context.Background(), config)
	if !errors.Is(err, profiler.ErrBootstrapServingTraceRequired) {
		t.Fatalf("producer Dataset B was accepted for profiling Dataset A: %v", err)
	}
	config.Measurement.Dataset.Name = "actual-producer-dataset-b"
	config.Measurement.Dataset.UID = "actual-producer-dataset-b-uid"
	if _, err := resolver.ResolveCadence(context.Background(), config); err != nil {
		t.Fatalf("actual producer Dataset B was rejected in favor of applied Workload Dataset A: %v", err)
	}
}

func TestRetainedCadenceSelectedStageIdentityFailsClosed(t *testing.T) {
	for _, defect := range []string{"missing", "uid", "snapshot"} {
		t.Run(defect, func(t *testing.T) {
			config, _ := servingCadenceRunFixture(t)
			resolver, _ := retainedServingCadenceFixtureWith(t, config, nil, func(reader client.Client, _ map[string][]byte) {
				ctx := context.Background()
				var stage api.EvaluationStage
				if err := reader.Get(ctx, types.NamespacedName{Namespace: "system", Name: "stage"}, &stage); err != nil {
					t.Fatal(err)
				}
				if defect == "missing" {
					if err := reader.Delete(ctx, &stage); err != nil {
						t.Fatal(err)
					}
					return
				}
				if defect == "uid" {
					stage.UID = "replacement"
				} else {
					stage.Spec.InputSnapshotRef.Key = "different-input-snapshot.json"
				}
				if err := reader.Update(ctx, &stage); err != nil {
					t.Fatal(err)
				}
			})
			_, err := resolver.ResolveCadence(context.Background(), config)
			if !errors.Is(err, profiler.ErrBootstrapServingTraceRequired) {
				t.Fatalf("defective only source did not fail closed: %v", err)
			}
		})
	}
}

func TestRetainedCadenceNewestMatchingStageIsDeterministic(t *testing.T) {
	for _, newer := range []bool{false, true} {
		t.Run(fmt.Sprintf("later-finish-%t", newer), func(t *testing.T) {
			config, _ := servingCadenceRunFixture(t)
			resolver, data := retainedServingCadenceFixtureWith(t, config, nil, func(reader client.Client, data map[string][]byte) {
				ctx := context.Background()
				var run api.EvaluationRun
				var stage api.EvaluationStage
				if err := reader.Get(ctx, types.NamespacedName{Namespace: "system", Name: "evaluation"}, &run); err != nil {
					t.Fatal(err)
				}
				if err := reader.Get(ctx, types.NamespacedName{Namespace: "system", Name: "stage"}, &stage); err != nil {
					t.Fatal(err)
				}
				stage.Name, stage.UID, stage.ResourceVersion = "z-stage", "z-stage-uid", ""
				if newer {
					finished := metav1.NewTime(stage.Status.FinishedAt.Add(time.Hour))
					stage.Status.FinishedAt = &finished
				}
				ref := *stage.Status.Procedures[0].EvidenceRef
				var manifest map[string]any
				if err := json.Unmarshal(data[ref.Store+"/"+ref.Key], &manifest); err != nil {
					t.Fatal(err)
				}
				manifest["stageUID"] = string(stage.UID)
				body, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				ref.Key, ref.Size = "z-stage/result.json", int64(len(body))
				data[ref.Store+"/"+ref.Key] = body
				stage.Status.Procedures[0].EvidenceRef = &ref
				if err := reader.Create(ctx, &stage); err != nil {
					t.Fatal(err)
				}
				run.Status.ChildRefs = append(run.Status.ChildRefs, api.EvaluationChildReference{StageID: stage.Spec.StageID, EvaluationObjectReference: api.EvaluationObjectReference{Name: stage.Name, UID: stage.UID}})
				if err := reader.Update(ctx, &run); err != nil {
					t.Fatal(err)
				}
			})
			contracts, err := resolver.ResolveCadence(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			for _, contract := range contracts {
				for _, source := range contract.Provenance.Sources {
					if !strings.HasPrefix(source.TrialID, "evaluation-run/z-stage-uid/") {
						t.Fatalf("selected non-newest source: %s", source.TrialID)
					}
				}
			}
			for _, defect := range []string{"missing", "invalid-json", "foreign-identity"} {
				t.Run("selected-artifact-"+defect, func(t *testing.T) {
					key := "evaluation/z-stage/result.json"
					switch defect {
					case "missing":
						delete(data, key)
					case "invalid-json":
						data[key] = []byte("{")
					case "foreign-identity":
						data[key] = []byte(`{"runUID":"foreign-run"}`)
					}
					if _, err := resolver.ResolveCadence(context.Background(), config); err == nil {
						t.Fatal("chosen artifact failure fell back to the older intact stage")
					}
				})
			}
		})
	}
}

func TestRetainedCadenceSelectedEvidenceErrorsRemainStrict(t *testing.T) {
	for _, defect := range []string{"manifest-identity", "collection-incomplete", "phase-window"} {
		t.Run(defect, func(t *testing.T) {
			config, _ := servingCadenceRunFixture(t)
			resolver, data := retainedServingCadenceFixture(t, config)
			key := "evaluation/recorded/stage/result.json"
			if defect == "collection-incomplete" {
				key = "evaluation/recorded/stage/artifacts/measured/raw-source-index.json"
			} else if defect == "phase-window" {
				key = "evaluation/recorded/stage/artifacts/measured/phase.json"
			}
			var body map[string]any
			if err := json.Unmarshal(data[key], &body); err != nil {
				t.Fatal(err)
			}
			switch defect {
			case "manifest-identity":
				body["stageUID"] = "foreign-stage"
			case "collection-incomplete":
				body["complete"] = false
			case "phase-window":
				body["endUTC"] = body["startUTC"]
			}
			payload, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			data[key] = payload
			if _, err := resolver.ResolveCadence(context.Background(), config); err == nil {
				t.Fatal("chosen source evidence corruption was ignored")
			}
		})
	}
}

func TestRetainedCadenceDiscoveryReadErrorsPropagate(t *testing.T) {
	for _, target := range []string{"scenario", "stage", "binding", "snapshot"} {
		for _, failure := range []string{"transport", "auth"} {
			t.Run(target+"/"+failure, func(t *testing.T) {
				config, _ := servingCadenceRunFixture(t)
				var reader client.Reader
				_, data := retainedServingCadenceFixtureWith(t, config, nil, func(actual client.Client, _ map[string][]byte) {
					reader = actual
				})
				want := errors.New("retained source transport failed")
				if failure == "auth" {
					want = apierrors.NewForbidden(schema.GroupResource{Group: api.GroupVersion.Group, Resource: "evaluations"}, "retained", errors.New("denied"))
				}
				faults := cadenceFaultReader{Reader: reader, fail: func(object client.Object) error {
					switch object.(type) {
					case *api.LoadScenario:
						if target == "scenario" {
							return want
						}
					case *api.EvaluationStage:
						if target == "stage" {
							return want
						}
					}
					return nil
				}}
				objects := cadenceObjectReader(func(_ context.Context, object evidencestore.ObjectIdentity) (io.ReadCloser, error) {
					if target == "snapshot" {
						return nil, want
					}
					return io.NopCloser(bytes.NewReader(data[object.Store+"/"+object.Key])), nil
				})
				resolver, err := profilerkube.NewKubernetesCadenceResolver(faults, "system", func(context.Context, *api.EvaluationRun) (evidencestore.Reader, error) {
					if target == "binding" {
						return nil, want
					}
					return objects, nil
				}, cadenceUnavailableArchive{})
				if err != nil {
					t.Fatal(err)
				}
				_, err = resolver.ResolveCadence(context.Background(), config)
				if !errors.Is(err, want) || errors.Is(err, profiler.ErrBootstrapServingTraceRequired) {
					t.Fatalf("discovery swallowed or reclassified %s failure: %v", failure, err)
				}
			})
		}
	}
}

type cadenceFaultReader struct {
	client.Reader
	fail func(client.Object) error
}

func (r cadenceFaultReader) Get(ctx context.Context, key client.ObjectKey, object client.Object, options ...client.GetOption) error {
	if err := r.fail(object); err != nil {
		return err
	}
	return r.Reader.Get(ctx, key, object, options...)
}

type cadenceObjectReader func(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error)

func (r cadenceObjectReader) Open(ctx context.Context, object evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	return r(ctx, object)
}

func retainedServingCadenceFixture(t *testing.T, config profilingrun.RunConfig, parts ...api.LoadScenarioPart) (*profilerkube.KubernetesCadenceResolver, map[string][]byte) {
	t.Helper()
	return retainedServingCadenceFixtureWith(t, config, nil, nil, parts...)
}

func retainedServingCadenceFixtureWith(t *testing.T, config profilingrun.RunConfig, scenarioChange func(*api.LoadScenario), arrange func(client.Client, map[string][]byte), parts ...api.LoadScenarioPart) (*profilerkube.KubernetesCadenceResolver, map[string][]byte) {
	t.Helper()
	start := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	data := map[string][]byte{}
	put := func(key string, value any) api.EvaluationEvidenceReference {
		payload, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		data["evaluation/"+key] = payload
		return api.EvaluationEvidenceReference{Store: "evaluation", Key: key, Size: int64(len(payload))}
	}
	workload, uid := config.Measurement.WorkloadName, config.Measurement.WorkloadUID
	if len(parts) == 0 {
		parts = []api.LoadScenarioPart{{ID: "actual", DurationSeconds: 2, Rates: map[string]api.EvaluationNonNegative{workload: 2}}}
	}
	scheduleParts := make([]map[string]any, 0, len(parts))
	var offsets, actualGaps []int64
	var total time.Duration
	for _, part := range parts {
		scheduleParts = append(scheduleParts, map[string]any{"id": part.ID, "seconds": part.DurationSeconds, "rates": part.Rates})
		if part.Rates[workload] > 0 {
			knownGap := int64(73)
			if len(offsets) != 0 {
				knownGap = 900
			}
			offsets = append(offsets, int64(total+100*time.Millisecond), int64(total+200*time.Millisecond))
			actualGaps = append(actualGaps, 1100000000, knownGap)
		}
		total += time.Duration(part.DurationSeconds * float64(time.Second))
	}
	end, drained := start.Add(total), start.Add(total+time.Second)
	finished := metav1.NewTime(drained)
	scenario := &api.LoadScenario{ObjectMeta: metav1.ObjectMeta{Name: "recorded", Namespace: "system", UID: "scenario"}, Spec: api.LoadScenarioSpec{
		Workloads:           []api.EvaluationWorkloadReference{{Name: workload, UID: types.UID(uid), Generation: 1}},
		Parts:               parts,
		ArrivalDistribution: map[string]string{workload: "constant"}, Seeds: map[string]api.EvaluationSeed{workload: "0"},
		DatasetRefs: map[string]api.EvaluationObjectReference{workload: {Name: config.Measurement.Dataset.Name, UID: types.UID(config.Measurement.Dataset.UID)}},
	}}
	if scenarioChange != nil {
		scenarioChange(scenario)
	}
	schedule := map[string]any{"id": "recorded-row", "seconds": total.Seconds(), "parts": scheduleParts, "traces": map[string]any{workload: map[string]any{"offsetsNS": offsets, "offered": len(offsets)}}}
	snapshot := put("retained/input-snapshot.json", map[string]any{
		"schemaVersion": "evaluation-snapshot.v2alpha1", "runUID": "evaluation-run",
		"scenarios": map[string]any{"scenario": map[string]any{"ref": api.EvaluationObjectReference{Name: scenario.Name, UID: scenario.UID}, "spec": scenario.Spec, "workloads": []any{map[string]any{"name": workload, "uid": uid, "model": map[string]string{"name": config.State.Model, "uid": config.Candidate.Inputs.ModelSpec.UID}, "preprocessor": map[string]string{"name": config.State.Preprocessor.Name, "uid": config.State.Preprocessor.UID}, "dataset": map[string]string{"name": config.Measurement.Dataset.Name, "uid": config.Measurement.Dataset.UID}}}}},
		"bindings":  map[string]any{"stage-id": map[string]any{"schedule": []any{schedule}}},
	})
	phase := put("recorded/stage/artifacts/measured/phase.json", map[string]any{"stage": schedule, "starts": map[string]any{workload: map[string]any{"at": start}}, "startUTC": start, "endUTC": end, "drainedUTC": drained})
	var requests bytes.Buffer
	for call, offset := range offsets {
		batch := uint64(call + 1)
		at := start.Add(time.Duration(offset))
		row := serving.RequestRow{Kind: serving.RequestRowFinal, RequestID: fmt.Sprintf("request-%d", batch), WorkloadUID: uid, DefinitionID: "definition", Node: &serving.NodeReference{UID: config.TargetNode.UID, Name: config.TargetNode.Name}, Instance: "pod-uid/container-id", ProfileID: "recorded-profile", ReceivedAt: at.Add(-time.Millisecond), Outcome: "completed", ExecutionComplete: true}
		for _, phase := range []string{"queued", "running", "finished"} {
			progress := firstfloor.Progress{RequestID: row.RequestID, WorkloadUID: uid, Definition: row.DefinitionID, Phase: phase, At: at, BatchID: batch, BatchSize: 1, ProfileID: row.ProfileID}
			if phase == "queued" {
				progress.At = at.Add(-time.Millisecond)
			}
			if phase == "finished" {
				gap := actualGaps[call]
				progress.At, progress.ExecutionComplete, progress.Outcome = at.Add(time.Microsecond), true, "completed"
				progress.ServiceMS, progress.NativeElapsedMS = .001, 1
				progress.ServiceSample = &firstfloor.ServiceSample{ServiceDurationNanos: 1000, PrecedingIdleNanos: &gap, TrialID: "native-trial"}
				if batch == 1 {
					progress.ServiceSample.PrecedingIdleNanos = nil
				}
			}
			wire, err := json.Marshal(progress)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := servingfirstfloor.ParseProgress(append([]byte("CHILL_PROGRESS "), wire...))
			if err != nil {
				t.Fatal(err)
			}
			row.FoldNative(&decoded)
		}
		if err := json.NewEncoder(&requests).Encode(row); err != nil {
			t.Fatal(err)
		}
	}
	requestKey := "recorded/stage/artifacts/measured/obs/20261105T10/requests.jsonl.gz"
	data["evaluation/"+requestKey] = gzipCadenceBytes(t, requests.Bytes())
	requestRef := api.EvaluationEvidenceReference{Store: "evaluation", Key: requestKey, Size: int64(len(data["evaluation/"+requestKey]))}
	index := put("recorded/stage/artifacts/measured/raw-source-index.json", map[string]any{"complete": true, "errors": []any{}, "requestedHours": []string{"20261105T10"}, "requestPopulation": map[string]any{"startUTC": start, "endUTC": end, "drainedUTC": drained}})
	provisional := put("recorded/stage/artifacts/measured/provisional-inputs/requests.jsonl.gz", map[string]string{"not": "the final raw collector"})
	manifest := put("recorded/stage/result.json", map[string]any{"runUID": "evaluation-run", "stageUID": "stage-uid", "stageID": "stage-id", "artifacts": []api.EvaluationEvidenceReference{phase, index, requestRef, provisional}})
	// Real catalog wire body; observer/meter data is not a source-selection key.
	profile := serving.Profile{ID: "recorded-profile"}
	profile.Condition.NodeUID, profile.Condition.PowerMode, profile.Condition.Artifact.ExecutionPath = config.TargetNode.UID, config.State.PowerMode, config.Candidate.ExecutionPath
	catalog, err := json.Marshal(map[string]any{"kind": "profile", "key": profile.ID, "at": start, "body": profile})
	if err != nil {
		t.Fatal(err)
	}
	data["observations/serving/observations.v1alpha5/20261105T10/catalog.jsonl.gz"] = gzipCadenceBytes(t, catalog)
	owner := true
	run := &api.EvaluationRun{ObjectMeta: metav1.ObjectMeta{Name: "evaluation", Namespace: "system", UID: "evaluation-run"}, Spec: api.EvaluationRunSpec{EvidenceStore: "evaluation", Stages: []api.StageTemplate{{ID: "stage-id", LoadScenarioRef: api.EvaluationObjectReference{Name: scenario.Name, UID: scenario.UID}}}}, Status: api.EvaluationRunStatus{FinishedAt: &finished, FrozenConfig: &api.EvaluationFrozenConfig{}, InputSnapshotRef: &snapshot, Conditions: []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionTrue}}, ChildRefs: []api.EvaluationChildReference{{StageID: "stage-id", EvaluationObjectReference: api.EvaluationObjectReference{Name: "stage", UID: "stage-uid"}}}}}
	stage := &api.EvaluationStage{ObjectMeta: metav1.ObjectMeta{Name: "stage", Namespace: "system", UID: "stage-uid", OwnerReferences: []metav1.OwnerReference{{APIVersion: api.GroupVersion.String(), Kind: "EvaluationRun", Name: run.Name, UID: run.UID, Controller: &owner}}}, Spec: api.EvaluationStageSpec{StageID: "stage-id", Row: "recorded-row", LoadScenarioRef: api.EvaluationObjectReference{Name: scenario.Name, UID: scenario.UID}, InputSnapshotRef: snapshot, Harness: api.EvaluationImage{SourceSHA: "recorded-source"}}, Status: api.EvaluationStageStatus{FinishedAt: &finished, Conditions: []metav1.Condition{{Type: "ExecutionComplete", Status: metav1.ConditionTrue}}, Procedures: []api.EvaluationProcedure{{Step: "stage", EvidenceRef: &manifest}}}}
	scheme := runtime.NewScheme()
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run, stage, scenario).Build()
	if arrange != nil {
		arrange(reader, data)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("list-type") == "2" {
			type object struct {
				Key  string `xml:"Key"`
				Size int    `xml:"Size"`
			}
			result := struct {
				XMLName     xml.Name `xml:"ListBucketResult"`
				Contents    []object `xml:"Contents"`
				IsTruncated bool     `xml:"IsTruncated"`
			}{}
			prefix := strings.TrimPrefix(request.URL.Path, "/") + "/"
			for key, value := range data {
				if strings.HasPrefix(key, prefix+request.URL.Query().Get("prefix")) {
					result.Contents = append(result.Contents, object{strings.TrimPrefix(key, prefix), len(value)})
				}
			}
			w.Header().Set("Content-Type", "application/xml")
			if err := xml.NewEncoder(w).Encode(result); err != nil {
				t.Error(err)
			}
			return
		}
		body, found := data[strings.TrimPrefix(request.URL.Path, "/")]
		if !found {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(404)
			_, _ = w.Write([]byte("<Error><Code>NoSuchKey</Code></Error>"))
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)
	store := func(name string) *evidences3.Store {
		objects, err := evidences3.New(evidences3.Config{LogicalStore: name, Bucket: name, Endpoint: server.URL, Region: "us-east-1", AccessKeyID: "test", SecretAccessKey: "test", TempDir: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		return objects
	}
	evaluation, observations := store("evaluation"), store("observations")
	resolver, err := profilerkube.NewKubernetesCadenceResolver(reader, "system", func(context.Context, *api.EvaluationRun) (evidencestore.Reader, error) { return evaluation, nil }, observations)
	if err != nil {
		t.Fatal(err)
	}
	return resolver, data
}

func gzipCadenceBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	if _, err := compressed.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func servingCadenceRunFixture(t *testing.T) (profilingrun.RunConfig, []byte) {
	t.Helper()
	payload, err := os.ReadFile("../testdata/lattepanda-ort-cpu-bs1.json")
	if err != nil {
		t.Fatal(err)
	}
	var config profilingrun.RunConfig
	if err := json.Unmarshal(payload, &config); err != nil {
		t.Fatal(err)
	}
	config.Measurement.Cadence = nil
	config.Workload = profilingrun.CurrentWorkloadContract()
	config.Actor = config.ActorConfig()
	config.Actor.MemoryRequestBytes, config.Actor.MemoryLimitBytes = 128<<20, 1<<30
	candidate, err := os.ReadFile("../testdata/candidate-report.json")
	if err != nil {
		t.Fatal(err)
	}
	return config, candidate
}

func cadencePublicCampaign(t *testing.T, config profilingrun.RunConfig) *api.BatchEnergyOptimization {
	t.Helper()
	// Deliberately decode an ordinary public object with no cadence body.
	body := fmt.Sprintf(`{"metadata":{"name":"measure","namespace":"system","uid":"campaign"},"spec":{"state":{"candidate":{"modelSpec":%q,"deviceClass":%q,"executionPath":%q,"powerMode":%q,"preprocessor":%q},"targetNode":%q},"activeProtocol":{"workload":%q,"dataset":%q,"durationSeconds":30,"warmupIterations":20,"metricObservation":{"intervalMilliseconds":1000,"timeoutMilliseconds":500,"durationSeconds":31}},"batchSizes":[1],"trialsPerBatch":3,"retryLimit":0}}`, config.State.Model, config.State.DeviceClass, config.Candidate.ExecutionPath, config.State.PowerMode, config.State.Preprocessor.Name, config.TargetNode.Name, config.Measurement.WorkloadName, config.Measurement.Dataset.Name)
	var parent api.BatchEnergyOptimization
	if err := json.Unmarshal([]byte(body), &parent); err != nil {
		t.Fatal(err)
	}
	return &parent
}

type cadencePreparationFacts struct {
	config    profilingrun.RunConfig
	candidate []byte
}

func (p cadencePreparationFacts) ResolveCandidate(context.Context, profiler.CandidateSelection) (profiler.ResolvedCandidate, error) {
	c := p.config
	return profiler.ResolvedCandidate{ReportBytes: p.candidate, Inputs: c.Candidate.Inputs, Verdict: c.Candidate.Verdict, State: c.State, Dataset: c.Measurement.Dataset, DeviceArchitecture: c.TargetNode.Architecture, DeviceNodeSelector: map[string]string{"edge.dacs.io/device-class": c.State.DeviceClass}, DevicePowerModes: []string{c.State.PowerMode}, RuntimeImage: c.Images.Runtime, ArtifactImage: c.Images.Artifact, ArtifactArchitecture: c.TargetNode.Architecture, MetricObserverImage: c.Images.MetricObserver}, nil
}
func (p cadencePreparationFacts) ResolveNode(context.Context, profiler.NodeSelection) (profilingrun.NodeSnapshot, error) {
	return p.config.TargetNode, nil
}
func (p cadencePreparationFacts) ResolveMetricSource(context.Context, string) (profiler.ResolvedMetricSource, error) {
	m := p.config.Metrics
	return profiler.ResolvedMetricSource{Kind: m.SourceKind, Endpoint: m.Endpoint, PowerMetric: m.PowerMetric, MetricLabels: m.MetricLabels, ThermalEndpoint: m.ThermalEndpoint, ThermalMetrics: m.ThermalMetrics}, nil
}
func (p cadencePreparationFacts) ResolveWorkloadObservation(context.Context) (profiler.ResolvedWorkloadObservation, error) {
	return profiler.ResolvedWorkloadObservation{Image: p.config.Images.WorkloadObserver}, nil
}
func (p cadencePreparationFacts) ResolveMeasurementWorkload(context.Context, string, profiler.CandidateSelection) (profilingrun.MeasurementContract, error) {
	return p.config.Measurement, nil
}
func (p cadencePreparationFacts) ResolveActuationPolicy(context.Context, profiler.ActuationSelection) (profiler.FrozenActuationPolicy, error) {
	return profiler.FrozenActuationPolicy{}, errors.New("fixed mode has no actuator")
}
func (p cadencePreparationFacts) Resolve(context.Context, profilingrun.RunConfig) (profiler.FrozenExecutionPolicy, error) {
	return profiler.FrozenExecutionPolicy{Policy: profiler.ExecutionPolicy{ExecutionPolicySpec: profiler.ExecutionPolicySpec{RuntimeMemoryRequestBytes: p.config.Actor.MemoryRequestBytes, RuntimeMemoryLimitBytes: p.config.Actor.MemoryLimitBytes}}}, nil
}

type cadenceUnavailableArchive struct{}

func (cadenceUnavailableArchive) Open(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	return nil, evidencestore.ErrNotFound
}
func (cadenceUnavailableArchive) List(context.Context, string) ([]evidencestore.ObjectIdentity, error) {
	return nil, nil
}

func brokenCadenceSnapshot(
	t *testing.T, data map[string][]byte, original, broken *api.EvaluationRun,
	config profilingrun.RunConfig, defect string,
) api.EvaluationEvidenceReference {
	t.Helper()
	var snapshot map[string]any
	ref := *original.Status.InputSnapshotRef
	if err := json.Unmarshal(data[ref.Store+"/"+ref.Key], &snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot["runUID"] = string(broken.UID)
	if strings.HasPrefix(defect, "unrelated-snapshot-") {
		frozen := snapshot["scenarios"].(map[string]any)["scenario"].(map[string]any)
		frozen["spec"].(map[string]any)["datasetRefs"] = map[string]api.EvaluationObjectReference{
			config.Measurement.WorkloadName: {Name: "other-producer-dataset", UID: "other-producer-dataset-uid"},
		}
	}
	switch defect {
	case "matching-snapshot-run", "unrelated-snapshot-run":
		snapshot["runUID"] = "foreign-run"
	case "matching-snapshot-schema":
		snapshot["schemaVersion"] = "foreign-snapshot"
	case "matching-snapshot-scenario":
		snapshot["scenarios"] = map[string]any{}
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	switch defect {
	case "unrelated-snapshot-type", "matching-snapshot-type":
		payload = []byte(`{"runUID":123}`)
	case "unrelated-snapshot-syntax", "matching-snapshot-syntax":
		payload = []byte(`{"runUID":`)
	}
	ref.Key, ref.Size = "broken/input-snapshot.json", int64(len(payload))
	data[ref.Store+"/"+ref.Key] = payload
	broken.Status.InputSnapshotRef = &ref
	return ref
}

func assertIntactCadenceSource(t *testing.T, contracts map[int]*firstfloor.CadenceContract, dataset string) {
	t.Helper()
	if len(contracts) == 0 {
		t.Fatal("intact Run supplied no cadence")
	}
	for _, contract := range contracts {
		if !strings.Contains(contract.Target.LoadContext, dataset) {
			t.Fatal("intact source lost its input population")
		}
		for _, source := range contract.Provenance.Sources {
			if !strings.HasPrefix(source.TrialID, "evaluation-run/stage-uid/") {
				t.Fatalf("structurally defective Run became the source: %s", source.TrialID)
			}
		}
	}
}

func TestSnapshotReadAndCloseFailuresPrecedeContentClassification(t *testing.T) {
	closeFailure := errors.New("snapshot Close failed")
	for _, tc := range []struct {
		name                    string
		readErr, closeErr, want error
	}{
		{"context read", context.Canceled, nil, context.Canceled},
		{"transport read", io.ErrClosedPipe, nil, io.ErrClosedPipe},
		{"malformed plus Close", nil, closeFailure, closeFailure},
		{"malformed plus missing Close", nil, evidencestore.ErrNotFound, evidencestore.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, _ := servingCadenceRunFixture(t)
			var reader client.Reader
			retainedServingCadenceFixtureWith(t, config, nil, func(actual client.Client, _ map[string][]byte) {
				reader = actual
			})
			objects := cadenceObjectReader(func(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error) {
				return &cadenceSnapshotFailureStream{
					Reader: strings.NewReader(`{"runUID":123}`), readErr: tc.readErr, closeErr: tc.closeErr,
				}, nil
			})
			resolver, err := profilerkube.NewKubernetesCadenceResolver(reader, "system",
				func(context.Context, *api.EvaluationRun) (evidencestore.Reader, error) {
					return objects, nil
				}, cadenceUnavailableArchive{})
			if err != nil {
				t.Fatal(err)
			}
			contracts, err := resolver.ResolveCadence(context.Background(), config)
			if !errors.Is(err, tc.want) || errors.Is(err, profiler.ErrBootstrapServingTraceRequired) || len(contracts) != 0 {
				t.Fatalf("failed evidence I/O was excluded as malformed content: contracts=%v error=%v", contracts, err)
			}
		})
	}
}

type cadenceSnapshotFailureStream struct {
	io.Reader
	readErr, closeErr error
}

func (s *cadenceSnapshotFailureStream) Read(p []byte) (int, error) {
	if s.readErr != nil {
		return 0, s.readErr
	}
	return s.Reader.Read(p)
}

func (s *cadenceSnapshotFailureStream) Close() error { return s.closeErr }
