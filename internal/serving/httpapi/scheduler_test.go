package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	servingops "github.com/lab-paper-code/chill/internal/serving/operations"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/serving/application"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	"github.com/lab-paper-code/chill/test/fixtures"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

const actorUsername = "operator"

// cleanObservationDirectory models an explicitly clean predecessor, not crash recovery.
func cleanObservationDirectory(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "async-run.json"),
		[]byte(`{"started":"2026-11-05T00:00:00Z","clean":true,"gaps":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	return directory
}

func schedulerSettingsForTest() serving.SchedulerSettings {
	return serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		DefaultOperationCost: map[string]serving.OperationCostValue{
			"prepare": {Seconds: 852, Joules: 36371}, "remove": {Seconds: 13, Joules: 1014}, "power": {Seconds: 166, Joules: 2229},
		},
		DefaultIdlePower: &serving.IdlePowerDefault{NodeWatts: 54, PerInstanceWatts: 8},
		MaxReplicas: 1, TransitionReserveFraction: .25,
		CapacityGridSteps: 20, CapacityBisectionWidth: .005, CapacityTailSamples: 100,
		PlanSearchStrategy: "v1", PlanSearchEvaluations: 500000, PlanExactFinalists: 16, PlanPolishActive: 4,
		MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1}
}

type settingsArchive struct{ directory string }

func (a settingsArchive) StreamImmutable(
	_ context.Context, location evidencestore.ObjectLocation, produce func(io.Writer) error,
) (evidencestore.Receipt, error) {
	output := filepath.Join(a.directory, location.Key)
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	err = produce(file)
	info, statErr := file.Stat()
	err = errors.Join(err, statErr, file.Sync(), file.Close())
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	identity := evidencestore.ObjectIdentity{Store: location.Store, Key: location.Key, Size: info.Size()}
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: evidencestore.CreateOutcomeCreated,
	}, nil
}

func settingsServiceForTest(
	t *testing.T, kube kubernetes.Interface, observationLog *observation.ObservationLog, settings serving.SchedulerSettings,
	refreshFailure, completionAppendFailure bool,
) (*application.SchedulerService, *serving.Runtime, *atomic.Int32) {
	t.Helper()
	refreshes := &atomic.Int32{}
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api":
			_ = json.NewEncoder(w).Encode(meta.APIVersions{Versions: []string{"v1"}})
		case "/apis":
			version := meta.GroupVersionForDiscovery{GroupVersion: api.GroupVersion.String(), Version: api.GroupVersion.Version}
			_ = json.NewEncoder(w).Encode(meta.APIGroupList{Groups: []meta.APIGroup{{
				Name: api.GroupVersion.Group, Versions: []meta.GroupVersionForDiscovery{version}, PreferredVersion: version,
			}}})
		case "/apis/" + api.GroupVersion.String():
			_ = json.NewEncoder(w).Encode(meta.APIResourceList{GroupVersion: api.GroupVersion.String(),
				APIResources: []meta.APIResource{
					{Name: "batchenergyoptimizations", Kind: "BatchEnergyOptimization", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
					{Name: "powermodetransitioncosts", Kind: "PowerModeTransitionCost", Namespaced: true, Verbs: meta.Verbs{"get", "list"}},
				},
			})
		case "/apis/" + api.GroupVersion.String() + "/namespaces/serving/batchenergyoptimizations":
			refreshes.Add(1)
			if refreshFailure {
				http.Error(w, "catalog source unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(api.BatchEnergyOptimizationList{
				TypeMeta: meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "BatchEnergyOptimizationList"},
				Items:    []api.BatchEnergyOptimization{},
			})
		case "/apis/" + api.GroupVersion.String() + "/namespaces/serving/powermodetransitioncosts":
			_ = json.NewEncoder(w).Encode(api.PowerModeTransitionCostList{
				TypeMeta: meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "PowerModeTransitionCostList"},
				Items:    []api.PowerModeTransitionCost{},
			})
		default:
			t.Errorf("unexpected REST request while changing settings: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(apiServer.Close)
	scheme := runtime.NewScheme()
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := api.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := servingkube.NewStore(&rest.Config{Host: apiServer.URL}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	execution, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{QueueLimit: 8, ObservationLog: observationLog})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := execution.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	cluster := servingkube.NewClusterState(store, kube, "serving")
	observer, err := servingkube.NewObserver(cluster, store, kube, execution, &sdk.Client{}, servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	operations, err := servingops.NewOperations(cluster, store, kube, execution, observer, &sdk.Client{}, servingops.OperationConfig{Namespace: "serving", PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := servingkube.NewSchedulerController(cluster, store, kube, execution, observer, operations, observationLog, servingkube.SchedulerControllerConfig{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	objectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if completionAppendFailure && r.URL.Query().Get("prefix") == servingkube.DispatchOccupancyPrefix {
			if err := observationLog.Close(r.Context()); err != nil {
				t.Error(err)
			}
		}
		_, _ = io.WriteString(w, "<ListBucketResult></ListBucketResult>")
	}))
	t.Cleanup(objectServer.Close)
	objects, err := stores3.New(stores3.Config{LogicalStore: "evidence", Bucket: "bucket", Endpoint: objectServer.URL,
		Region: "region", AccessKeyID: "test", SecretAccessKey: "test", HTTPClient: objectServer.Client()})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := servingkube.NewCatalog(store, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	costs, err := servingkube.NewCostEvidence(store, objects, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewSchedulerService(context.Background(), application.Dependencies{
		Store: store, Kube: kube, SettingsKube: kube, Execution: execution, Observer: observer, Objects: objects,
		ObservationLog: observationLog, Operations: operations, Catalog: catalog, Costs: costs,
		Options: application.Options{ServingNamespace: "serving", SchedulerConfigMap: "scheduler", SchedulerConfigKey: "scheduler.json",
			MaxRequestBytes: 1 << 20, MaxEvidenceBytes: 1 << 20, StoreName: "evidence"},
	})
	service.Controller = controller
	t.Cleanup(service.Close)
	return service, execution, refreshes
}

func TestSchedulerSettingsPersistAndApplyFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, omit                                                                                             string
		resourceAllowed, updateFailure, observationLogFailure, invalidSettings, refreshFailure, missingRefresh bool
		completionAppendFailure                                                                                bool
		autonomous, stored                                                                                     []string
		want                                                                                                   int
	}{
		{name: "persist and apply", resourceAllowed: true, want: http.StatusOK},
		{name: "resource denial", want: http.StatusForbidden},
		{name: "write failure", resourceAllowed: true, updateFailure: true, want: http.StatusServiceUnavailable},
		{name: "completion append failure does not roll back", resourceAllowed: true, completionAppendFailure: true, want: http.StatusServiceUnavailable},
		{
			name: "journal failure prevents write", resourceAllowed: true,
			observationLogFailure: true, want: http.StatusServiceUnavailable,
		},
		{name: "validation prevents write", resourceAllowed: true, invalidSettings: true, want: http.StatusBadRequest},
		{name: "omitted completion margin prevents write", resourceAllowed: true, omit: `"completionMarginMS":0,`, want: http.StatusBadRequest},
		{
			name: "refresh failure does not enable stale catalog", resourceAllowed: true, refreshFailure: true,
			autonomous: []string{"workload-uid"}, stored: []string{"workload-uid"}, want: http.StatusServiceUnavailable,
		},
		{
			name: "missing owner refresh prevents persistence", resourceAllowed: true,
			missingRefresh: true, want: http.StatusServiceUnavailable,
		},
		{
			name: "absent autonomous Workload removed", resourceAllowed: true,
			autonomous: []string{"workload-uid", "deleted-uid"}, stored: []string{"workload-uid"}, want: http.StatusOK,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := schedulerSettingsForTest()
			after := before
			after.MaxInstances++
			after.CapacityGridSteps, after.CapacityBisectionWidth, after.CapacityTailSamples = 7, .02, 12
			// PUT stores only autonomous UIDs of current Workloads.
			after.AutonomousWorkloads = tc.autonomous
			stored := after
			stored.AutonomousWorkloads = tc.stored
			if tc.invalidSettings {
				after.ObservationWindow = 0
			}
			beforeRaw, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			configRaw := `{"settings":` + string(beforeRaw) + `,"reserved":{"node":{"memoryBytes":1024}}}`
			kube := fake.NewSimpleClientset(&core.ConfigMap{
				ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "1"},
				Data:       map[string]string{"scheduler.json": configRaw, "other": "untouched"},
			})
			kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
					Authenticated: true,
					User: authenticationv1.UserInfo{
						Username: actorUsername, UID: "actor-uid", Groups: []string{"scheduler-writers"},
					},
				}}, nil
			})
			kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				sar := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				allowed := sar.Spec.NonResourceAttributes != nil || tc.resourceAllowed
				return true, &authorizationv1.SubjectAccessReview{
					Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed},
				}, nil
			})
			updates := 0
			kube.PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
				updates++
				if tc.updateFailure {
					return true, nil, errors.New("storage unavailable")
				}
				return false, nil, nil
			})
			observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := observationLog.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			service, execution, refreshes := settingsServiceForTest(t, kube, observationLog, before, tc.refreshFailure, tc.completionAppendFailure)
			controller := service.Controller
			if err := execution.Publish(&serving.Definition{Name: "workload", UID: "workload-uid"}); err != nil {
				t.Fatal(err)
			}
			if tc.observationLogFailure {
				if err = observationLog.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, runtime: execution, observationLog: observationLog, config: HandlerConfig{
				Scheduler: controller, SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler",
				SchedulerConfigKey: "scheduler.json", SchedulerLock: &service.Lock, MaxRequestBytes: 1 << 20,
			}}
			if !tc.missingRefresh {
				handler.config.ChangeSchedulerSettings = service.ChangeSchedulerSettings
			}
			raw, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.Replace(raw, []byte(tc.omit), nil, 1)
			request := httptest.NewRequest(http.MethodPut, "/api/v1/scheduler/settings", bytes.NewReader(raw))
			request.Header.Set("Authorization", "Bearer reviewed-token")
			request.Header.Set("If-Match", `"1"`)
			response := httptest.NewRecorder()
			handler.scheduler(response, request)
			if response.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", response.Code, tc.want, response.Body)
			}
			persisted, err := kube.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if response.Code != http.StatusOK {
				if tc.completionAppendFailure {
					assertSchedulerSettingsUnapplied(t, persisted, controller, after, after)
					if updates != 1 || refreshes.Load() != 1 {
						t.Fatal("completion failure changed apply sequence")
					}
					return
				}
				wantDesired := before
				if tc.refreshFailure {
					wantDesired = after
					assertSchedulerSettingsRefreshFailed(t, observationLog, controller, int(refreshes.Load()))
				}
				assertSchedulerSettingsUnapplied(t, persisted, controller, before, wantDesired)
				if (tc.observationLogFailure || !tc.resourceAllowed || tc.invalidSettings || tc.omit != "" || tc.missingRefresh) && updates != 0 {
					t.Fatal("denied/unrecorded/invalid/unrefreshable settings performed CM write")
				}
				return
			}
			if !reflect.DeepEqual(controller.Settings(), stored) || updates != 1 || refreshes.Load() != 1 || persisted.Data["other"] != "untouched" {
				t.Fatal("settings not durably applied or unrelated CM data altered")
			}
			assertSchedulerSettingsAudit(t, persisted, response.Body.Bytes(), observationLog, before, stored)
		})
	}
}

func assertSchedulerSettingsUnapplied(
	t *testing.T, persisted *core.ConfigMap, controller *servingkube.SchedulerController,
	before, wantDesired serving.SchedulerSettings,
) {
	t.Helper()
	var desired struct {
		Settings serving.SchedulerSettings `json:"settings"`
	}
	if err := json.Unmarshal([]byte(persisted.Data["scheduler.json"]), &desired); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(controller.Settings(), before) || !reflect.DeepEqual(desired.Settings, wantDesired) {
		t.Fatal("failed settings change lost desired-versus-accepted policy separation")
	}
}

func assertSchedulerSettingsRefreshFailed(
	t *testing.T, observationLog *observation.ObservationLog, controller *servingkube.SchedulerController, refreshes int,
) {
	t.Helper()
	if refreshes != 1 || len(controller.Settings().AutonomousWorkloads) != 0 {
		t.Fatal("post-persistence refresh failure enabled the previously accepted catalog")
	}
	records, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), time.Now().Add(-time.Minute), time.Now().Add(time.Second), 10, 0, observation.SchedulerObservationSelection{})
	if err != nil || len(records) != 2 || records[1].Settings.AppliedAt != nil || records[1].Settings.Error == "" {
		t.Fatalf("refresh failure audit claimed application: %+v %v", records, err)
	}
}

func assertSchedulerSettingsAudit(t *testing.T, persisted *core.ConfigMap, response []byte,
	observationLog *observation.ObservationLog, before, after serving.SchedulerSettings,
) {
	t.Helper()
	var saved struct {
		Reserved map[string]serving.SchedulerAllocation `json:"reserved"`
	}
	if err := json.Unmarshal([]byte(persisted.Data["scheduler.json"]), &saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Reserved, map[string]serving.SchedulerAllocation{"node": {MemoryBytes: 1024}}) {
		t.Fatal("operator configuration lost")
	}
	var applied observation.SchedulerSettingsChange
	if err := json.Unmarshal(response, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.ActorUsername != actorUsername || applied.ActorUID != "actor-uid" ||
		!reflect.DeepEqual(applied.Before, before) || !reflect.DeepEqual(applied.After, after) || applied.AppliedAt == nil {
		t.Fatalf("missing actual audit result: %+v", applied)
	}
	records, _, _, _, err := observationLog.SchedulerObservationPage(context.Background(), applied.ValidatedAt.Add(-time.Second), time.Now().Add(time.Second), 10, 0, observation.SchedulerObservationSelection{})
	if err != nil || len(records) != 2 || records[0].Settings.AppliedAt != nil || records[1].Settings.AppliedAt == nil {
		t.Fatalf("intent/application ledger=%+v error=%v", records, err)
	}
}

func TestSchedulerCompactReadSharesEvidenceWithoutLosingRetainedVariants(t *testing.T) {
	at := time.Date(2026, 10, 15, 18, 22, 30, 0, time.UTC)
	profile := &serving.Profile{ID: "profile", Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "profile", Size: 123},
		Batches: []serving.BatchProfile{{Batch: 1, ServiceSamples: []firstfloor.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 12), fixtures.ServiceSample("physical-trial", 1, 34)}, EnergyPerInputJoules: 0, EnergyBelowResolution: true}}}
	equalProfile := *profile
	differentProfile := *profile
	differentProfile.Batches = []serving.BatchProfile{{Batch: 1, ServiceSamples: []firstfloor.ServiceSample{fixtures.ServiceSample("physical-trial", 0, 56), fixtures.ServiceSample("physical-trial", 1, 56)}}}
	instances := []serving.SchedulerInstance{
		{ID: "physical", TargetID: "selected-slot", Node: serving.NodeReference{UID: "node-uid"}, Profile: profile, Reasons: []string{"PolicyAckPending"}},
		{ID: "equal", Profile: &equalProfile},
		{ID: "different", Profile: &differentProfile},
		{ID: "missing"},
	}
	candidate := serving.SchedulerCandidate{ID: "selected", Instances: instances}
	decision := serving.SchedulerDecision{Keep: candidate, Target: &candidate}
	snapshot := serving.SchedulerSnapshot{Instances: instances, Options: []serving.SchedulerOption{{Instance: instances[0]}}, Reservations: []serving.SchedulerReservation{{Owner: candidate.ID, Target: &candidate}}}
	event := observation.SchedulerObservation{At: at, Decision: &decision, Snapshot: &snapshot}
	evidence := newSchedulerReadEvidence()
	projected := evidence.observations([]observation.SchedulerObservation{event})
	got := projected[0].Decision.Target.Instances
	if !reflect.DeepEqual(got[0].Reasons, instances[0].Reasons) ||
		!reflect.DeepEqual(projected[0].Snapshot.Instances[0].Reasons, instances[0].Reasons) {
		t.Fatal("compact observation/snapshot hid policy ACK waiting")
	}
	if len(evidence.Profiles) != 2 || got[0].Profile.Ref != got[1].Profile.Ref || got[0].Profile.Ref == got[2].Profile.Ref {
		t.Fatal("equal evidence was not shared, or differing same-identity evidence was discarded")
	}
	for i := range instances {
		if instances[i].Profile == nil {
			if got[i].Profile != nil {
				t.Fatal("missing evidence became fabricated evidence")
			}
			continue
		}
		if !reflect.DeepEqual(evidence.Profiles[got[i].Profile.Ref], instances[i].Profile) {
			t.Fatal("reference changed a measured quantity or immutable evidence identity")
		}
	}
	if len(projected[0].Snapshot.Options) != 0 ||
		projected[0].Snapshot.Reservations[0].Target.Instances[0].Profile.Ref != got[0].Profile.Ref {
		t.Fatal("snapshot evidence did not use the same response dictionaries")
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil || !bytes.Contains(evidenceJSON, []byte(`"energyBelowResolution":true`)) {
		t.Fatalf("compact observation API lost below-resolution marker: %s, %v", evidenceJSON, err)
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Profile map[string]json.RawMessage `json:"profile"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if profileFields := wire.Profile; len(profileFields) != 1 || profileFields["$ref"] == nil {
		t.Fatal("compact wire response still includes the repeated inline profile")
	}
	if profile.Batches[0].ServiceSamples[0].ServiceDurationNanos != 12 || instances[0].Profile != profile {
		t.Fatal("read projection mutated the raw source")
	}
}

func TestSchedulerSettingsProjectionsAreReadOnly(t *testing.T) {
	settings := schedulerSettingsForTest()
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]map[string]int{"old-uid": {"physical": 1}}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "1"},
		Data: map[string]string{"scheduler.json": `{"settings":` + string(settingsRaw) +
			`,"staticRouting":{"old-uid":{"physical":1}},"staticRoutingValidatedAt":"2026-10-01T00:00:00Z"` +
			`,"powerObservation":{"sampling":{"Interval":1000000000,"Duration":1000000000,"RequestTimeout":100000000},"coverage":{"MaximumGap":2000000000,"MaximumBoundaryDistance":3000000000,"MinimumCoverage":0.99}}}`},
	})
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: actorUsername, UID: "actor-uid"},
		}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	updates := 0
	kube.PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
		updates++
		return false, nil, nil
	})
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	service, execution, _ := settingsServiceForTest(t, kube, observationLog, settings, false, false)
	controller := service.Controller
	meters := []servingops.MeterDeclaration{{ID: "accepted-meter", Node: serving.NodeReference{Name: "accepted-node", UID: "accepted-uid"}, Scope: "node-exclusive-wall"}}
	handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, runtime: execution, observationLog: observationLog, config: HandlerConfig{
		Scheduler: controller, SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler", SchedulerConfigKey: "scheduler.json",
		SchedulerLock: &service.Lock, MaxRequestBytes: 1 << 20, ChangeSchedulerSettings: service.ChangeSchedulerSettings,
		SchedulerMeters: func() []servingops.MeterDeclaration { return meters },
		RequestTimeout:  73125 * time.Millisecond,
	}}
	put := func(extra string) *httptest.ResponseRecorder {
		body := strings.TrimSuffix(string(settingsRaw), "}") + extra + "}"
		request := httptest.NewRequest(http.MethodPut, "/api/v1/scheduler/settings", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer reviewed-token")
		request.Header.Set("If-Match", `"1"`)
		response := httptest.NewRecorder()
		handler.scheduler(response, request)
		return response
	}
	for _, rejected := range []struct{ extra, field string }{
		{`,"staticRouting":{}`, "staticRouting"},
		{`,"staticRouting":null`, "staticRouting"},
		{`,"staticRouting":{"old-uid":{"physical":1}}`, "staticRouting"},
		{`,"staticRoutingValidatedAt":null`, "staticRoutingValidatedAt"},
		{`,"staticRoutingValidatedAt":"2026-10-01T00:00:00Z"`, "staticRoutingValidatedAt"},
		{`,"meters":[]`, "body"},
		{`,"meters":null`, "body"},
		{`,"requestTimeout":73125000000`, "body"},
		{`,"requestTimeout":null`, "body"},
		{`,"powerObservation":{}`, "body"},
	} {
		response := put(rejected.extra)
		var failure serving.Error
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != http.StatusBadRequest || failure.Field != rejected.field || updates != 0 {
			t.Fatalf("%s: status=%d body=%s updates=%d", rejected.extra, response.Code, response.Body, updates)
		}
	}
	// GET settings still exposes the stored read-only declaration.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/settings", nil)
	request.Header.Set("Authorization", "Bearer reviewed-token")
	got := httptest.NewRecorder()
	handler.scheduler(got, request)
	var shown struct {
		StaticRouting    map[string]map[string]int     `json:"staticRouting"`
		PowerObservation map[string]map[string]float64 `json:"powerObservation"`
		Meters           []servingops.MeterDeclaration `json:"meters"`
		RequestTimeout   time.Duration                 `json:"requestTimeout"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &shown); err != nil || got.Code != http.StatusOK || !reflect.DeepEqual(shown.StaticRouting, declared) {
		t.Fatalf("GET settings lost the stored declaration: status=%d body=%s", got.Code, got.Body)
	}
	if shown.PowerObservation["coverage"]["MinimumCoverage"] != 0.99 {
		t.Fatalf("GET settings lost the stored operating-power policy: %s", got.Body)
	}
	if !reflect.DeepEqual(shown.Meters, meters) {
		t.Fatalf("GET settings did not use accepted meter projection: %+v", shown.Meters)
	}
	if shown.RequestTimeout != handler.config.RequestTimeout || !strings.Contains(got.Body.String(), `"requestTimeout":73125000000`) {
		t.Fatalf("GET settings did not expose runtime timeout in nanoseconds: %s", got.Body)
	}
	handler.config.SchedulerMeters = nil
	empty := httptest.NewRecorder()
	handler.scheduler(empty, request)
	if !strings.Contains(empty.Body.String(), `"meters":[]`) {
		t.Fatalf("absent declarations must be an empty array: %s", empty.Body)
	}
	response := put("")
	if response.Code != http.StatusOK || updates != 1 {
		t.Fatalf("settings-only update: status=%d body=%s updates=%d", response.Code, response.Body, updates)
	}
	persisted, err := kube.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		StaticRouting            map[string]map[string]int `json:"staticRouting"`
		StaticRoutingValidatedAt *time.Time                `json:"staticRoutingValidatedAt"`
	}
	if err := json.Unmarshal([]byte(persisted.Data["scheduler.json"]), &stored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored.StaticRouting, declared) || stored.StaticRoutingValidatedAt == nil ||
		stored.StaticRoutingValidatedAt.Format(time.RFC3339) != "2026-10-01T00:00:00Z" {
		t.Fatalf("settings update changed routing intent: %s", persisted.Data["scheduler.json"])
	}
}

func TestSchedulerCompactQueryRejectsAmbiguousViews(t *testing.T) {
	for _, query := range []string{"view=compact&view=unsupported", "view=&view=compact", "view=compact&view=compact"} {
		if _, err := schedulerCompactQuery(httptest.NewRequest(http.MethodGet, "/api/v1/scheduler?"+query, nil)); err == nil {
			t.Fatalf("ambiguous view silently selected a representation: %s", query)
		}
	}
}

func TestPoolsShowMeasurementNodeAndWorkloadActivePool(t *testing.T) {
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	service, execution, _ := settingsServiceForTest(t, fake.NewSimpleClientset(), observationLog, schedulerSettingsForTest(), false, false)
	controller := service.Controller
	tainted := serving.NodeReference{Name: "tainted", UID: "tainted-uid"}
	serves := serving.NodeReference{Name: "serves", UID: "serves-uid"}
	execution.SetMeasurementNodes([]serving.NodeReference{tainted})
	if err := execution.InstallInstances([]serving.ReadyInstance{
		{State: serving.InstanceState{ID: "owned", Node: serves}, OwnerWorkloadUID: "workload-uid"},
		{State: serving.InstanceState{ID: "measured", Node: tainted}},
	}); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{runtime: execution, observer: &servingkube.Observer{}, config: HandlerConfig{Scheduler: controller}}
	view, err := handler.poolView("")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var pools serving.PoolsState
	if err := json.Unmarshal(raw, &pools); err != nil {
		t.Fatal(err)
	}
	if len(pools.Measurement) != 1 || pools.Measurement[0].Node != tainted || len(pools.Measurement[0].Instances) != 1 ||
		len(pools.Workloads) != 1 || pools.Workloads[0].Workload.UID != "workload-uid" ||
		!reflect.DeepEqual(pools.Workloads[0].Nodes, []serving.NodeReference{serves}) {
		t.Fatalf("pools=%s", raw)
	}
}

func TestSchedulerSettingsETagPreconditions(t *testing.T) {
	settings := schedulerSettingsForTest()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Name: "scheduler", Namespace: "serving", ResourceVersion: "1"},
		Data:       map[string]string{"scheduler.json": `{"settings":` + string(raw) + `}`},
	})
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
			Authenticated: true, User: authenticationv1.UserInfo{Username: actorUsername, UID: "actor-uid"},
		}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	conflict := false
	updates := 0
	kube.PrependReactor("update", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		updates++
		resource := core.SchemeGroupVersion.WithResource("configmaps")
		current, err := kube.Tracker().Get(resource, "serving", "scheduler")
		if err != nil {
			return true, nil, err
		}
		updated := action.(ktesting.UpdateAction).GetObject().(*core.ConfigMap).DeepCopy()
		if conflict || updated.ResourceVersion != current.(*core.ConfigMap).ResourceVersion {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "scheduler", errors.New("concurrent writer"))
		}
		version, err := strconv.ParseUint(updated.ResourceVersion, 10, 64)
		if err != nil {
			return true, nil, err
		}
		updated.ResourceVersion = strconv.FormatUint(version+1, 10)
		if err := kube.Tracker().Update(resource, updated, "serving"); err != nil {
			return true, nil, err
		}
		return true, updated, nil
	})
	observationLog, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: cleanObservationDirectory(t), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	service, execution, _ := settingsServiceForTest(t, kube, observationLog, settings, false, false)
	controller := service.Controller
	handler := &Handler{
		authorizer: &servingkube.Authorizer{Client: kube}, runtime: execution, observationLog: observationLog,
		config: HandlerConfig{
			Scheduler: controller, SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler",
			SchedulerConfigKey: "scheduler.json", SchedulerLock: &service.Lock, MaxRequestBytes: 1 << 20,
			ChangeSchedulerSettings: service.ChangeSchedulerSettings,
		},
	}
	request := func(method, tag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/scheduler/settings", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer reviewed-token")
		if tag != "" {
			req.Header.Set("If-Match", tag)
		}
		response := httptest.NewRecorder()
		handler.scheduler(response, req)
		return response
	}
	get := request(http.MethodGet, "")
	if get.Code != http.StatusOK || get.Header().Get("ETag") != `"1"` {
		t.Fatalf("GET status=%d etag=%q body=%s", get.Code, get.Header().Get("ETag"), get.Body)
	}
	for _, test := range []struct {
		tag    string
		status int
		code   string
	}{
		{"", http.StatusPreconditionRequired, "SettingsPreconditionRequired"},
		{`"stale"`, http.StatusConflict, "SettingsConflict"},
		{`W/"1"`, http.StatusConflict, "SettingsConflict"},
	} {
		response := request(http.MethodPut, test.tag)
		var failure serving.Error
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if response.Code != test.status || failure.Code != test.code || updates != 0 {
			t.Fatalf("tag=%q status=%d body=%s updates=%d", test.tag, response.Code, response.Body, updates)
		}
	}
	put := request(http.MethodPut, get.Header().Get("ETag"))
	if put.Code != http.StatusOK || put.Header().Get("ETag") != `"2"` || updates != 1 {
		t.Fatalf("PUT status=%d etag=%q body=%s updates=%d", put.Code, put.Header().Get("ETag"), put.Body, updates)
	}
	stale := request(http.MethodPut, get.Header().Get("ETag"))
	if stale.Code != http.StatusConflict || updates != 1 {
		t.Fatalf("replayed version status=%d body=%s", stale.Code, stale.Body)
	}
	latestETag := assertSettingsReadAfterExternalUpdate(t, kube, controller, request)
	conflict = true
	raced := request(http.MethodPut, latestETag)
	var failure serving.Error
	if err := json.Unmarshal(raced.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if raced.Code != http.StatusConflict || failure.Code != "SettingsConflict" {
		t.Fatalf("kube conflict status=%d body=%s", raced.Code, raced.Body)
	}
	// Another accepted mutation may finish before the first response is written.
	conflict = false
	assertSettingsResponseFrozen(t, handler, service, request, latestETag, settings)
}

func assertSettingsResponseFrozen(t *testing.T, handler *Handler, service *application.SchedulerService,
	request func(string, string) *httptest.ResponseRecorder, latestETag string, settings serving.SchedulerSettings,
) {
	t.Helper()
	controller := service.Controller
	var responseVersion, laterVersion string
	handler.config.ChangeSchedulerSettings = func(ctx context.Context, input application.SettingsInput,
		version string, valid bool, username, uid string,
	) (observation.SchedulerSettingsChange, string, error) {
		change, version, err := service.ChangeSchedulerSettings(ctx, input, version, valid, username, uid)
		if err != nil {
			return change, version, err
		}
		if !service.Lock.TryLock() {
			t.Fatal("settings lock retained after application result")
		}
		service.Lock.Unlock()
		responseVersion = version
		input.MaxInstances = settings.MaxInstances + 7
		_, laterVersion, err = service.ChangeSchedulerSettings(ctx, input, version, true, username, uid)
		return change, version, err
	}
	frozen := request(http.MethodPut, latestETag)
	var change observation.SchedulerSettingsChange
	if err := json.Unmarshal(frozen.Body.Bytes(), &change); err != nil {
		t.Fatal(err)
	}
	if frozen.Code != http.StatusOK || frozen.Header().Get("ETag") != strconv.Quote(responseVersion) ||
		responseVersion == laterVersion || change.After.MaxInstances != settings.MaxInstances ||
		controller.Settings().MaxInstances != settings.MaxInstances+7 {
		t.Fatalf("response reread later settings/version: status=%d etag=%q body=%s", frozen.Code, frozen.Header().Get("ETag"), frozen.Body)
	}
}

func assertSettingsReadAfterExternalUpdate(
	t *testing.T, kube kubernetes.Interface, controller *servingkube.SchedulerController,
	request func(string, string) *httptest.ResponseRecorder,
) string {
	t.Helper()
	current := controller.Settings()
	external, err := kube.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	desired := current
	desired.MaxInstances = 9
	desiredRaw, err := json.Marshal(desired)
	if err != nil {
		t.Fatal(err)
	}
	external.Data["scheduler.json"] = `{"settings":` + string(desiredRaw) + `}`
	external, err = kube.CoreV1().ConfigMaps("serving").Update(context.Background(), external, meta.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if controller.Settings().MaxInstances != current.MaxInstances {
		t.Fatal("external ConfigMap update unexpectedly refreshed runtime")
	}
	latest := request(http.MethodGet, "")
	var shown serving.SchedulerSettings
	if err := json.Unmarshal(latest.Body.Bytes(), &shown); err != nil {
		t.Fatal(err)
	}
	if latest.Code != http.StatusOK || shown.MaxInstances != desired.MaxInstances ||
		latest.Header().Get("ETag") != strconv.Quote(external.ResourceVersion) {
		t.Fatalf("GET paired stale settings with current version: status=%d etag=%q body=%s",
			latest.Code, latest.Header().Get("ETag"), latest.Body)
	}
	return latest.Header().Get("ETag")
}
