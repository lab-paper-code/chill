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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/serving"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"
)

const actorUsername = "operator"

func schedulerSettingsForTest() serving.SchedulerSettings {
	return serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, ComparisonPeriod: time.Second,
		MaxObservationAge: time.Second, MaxReplicas: 1,
		MaxInstances: 1, CapacityWindow: time.Second, CapacityWindows: 1, CapacityPassingWindows: 1, CapacityMinimumRequests: 1, PlanSearchWorkers: 1, MaxCPUs: 1}
}

type settingsArchive struct{ directory string }

func (a settingsArchive) PutImmutable(
	_ context.Context, identity evidencestore.ObjectIdentity, input io.Reader,
) (evidencestore.Receipt, error) {
	output := filepath.Join(a.directory, identity.Key)
	if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
		return evidencestore.Receipt{}, err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	n, err := io.Copy(file, input)
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil || n != identity.Size {
		return evidencestore.Receipt{}, errors.Join(err, errors.New("archive incomplete"))
	}
	return evidencestore.Receipt{
		SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: evidencestore.CreateOutcomeCreated,
	}, nil
}

func settingsControllerForTest(
	t *testing.T, kube kubernetes.Interface, observationLog *serving.ObservationLog, settings serving.SchedulerSettings,
) (*servingkube.SchedulerController, *serving.Runtime) {
	t.Helper()
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected REST request while changing settings: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(apiServer.Close)
	scheme := runtime.NewScheme()
	if err := core.AddToScheme(scheme); err != nil {
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
	observer, err := servingkube.NewObserver(store, kube, execution, &sdk.Client{},
		servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	operations, err := servingkube.NewOperations(store, kube, execution, observer, &sdk.Client{},
		servingkube.OperationConfig{Namespace: "serving", PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := servingkube.NewSchedulerController(store, kube, execution, observer, operations, observationLog,
		servingkube.SchedulerControllerConfig{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	return controller, execution
}

func TestSchedulerSettingsPersistAndApplyFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name, omit                                                                                             string
		resourceAllowed, updateFailure, observationLogFailure, invalidSettings, refreshFailure, missingRefresh bool
		autonomous, stored                                                                                     []string
		want                                                                                                   int
	}{
		{name: "persist and apply", resourceAllowed: true, want: http.StatusOK},
		{name: "resource denial", want: http.StatusForbidden},
		{name: "write failure", resourceAllowed: true, updateFailure: true, want: http.StatusServiceUnavailable},
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
			after.MaxCPUs++
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
			configRaw := `{"settings":` + string(beforeRaw) + `,"reserved":{"node":{"cpus":[0]}}}`
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
			observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
				Directory: t.TempDir(), Store: "evidence",
				Writer: settingsArchive{directory: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := observationLog.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			controller, execution := settingsControllerForTest(t, kube, observationLog, before)
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
				SchedulerConfigKey: "scheduler.json", SchedulerLock: &sync.Mutex{}, MaxRequestBytes: 1 << 20,
			}}
			refreshes := 0
			if !tc.missingRefresh {
				handler.config.RefreshScheduler = func(ctx context.Context) error {
					refreshes++
					if tc.refreshFailure {
						return errors.New("removed profile catalog refresh still fails")
					}
					cm, err := kube.CoreV1().ConfigMaps("serving").Get(ctx, "scheduler", meta.GetOptions{})
					if err != nil {
						return err
					}
					var configuration struct {
						Settings serving.SchedulerSettings `json:"settings"`
					}
					if err := json.Unmarshal([]byte(cm.Data["scheduler.json"]), &configuration); err != nil {
						return err
					}
					return controller.SetConfiguration(servingkube.SchedulerControllerConfig{Settings: configuration.Settings})
				}
			}
			raw, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
			raw = bytes.Replace(raw, []byte(tc.omit), nil, 1)
			request := httptest.NewRequest(http.MethodPut, "/api/v1/scheduler/settings", bytes.NewReader(raw))
			request.Header.Set("Authorization", "Bearer reviewed-token")
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
				wantDesired := before
				if tc.refreshFailure {
					wantDesired = after
					assertSchedulerSettingsRefreshFailed(t, observationLog, controller, refreshes)
				}
				assertSchedulerSettingsUnapplied(t, persisted, controller, before, wantDesired)
				if (tc.observationLogFailure || !tc.resourceAllowed || tc.invalidSettings || tc.omit != "" || tc.missingRefresh) && updates != 0 {
					t.Fatal("denied/unrecorded/invalid/unrefreshable settings performed CM write")
				}
				return
			}
			if !reflect.DeepEqual(controller.Settings(), stored) || updates != 1 || refreshes != 1 || persisted.Data["other"] != "untouched" {
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
	t *testing.T, observationLog *serving.ObservationLog, controller *servingkube.SchedulerController, refreshes int,
) {
	t.Helper()
	if refreshes != 1 || len(controller.Settings().AutonomousWorkloads) != 0 {
		t.Fatal("post-persistence refresh failure enabled the previously accepted catalog")
	}
	records, _, _, err := observationLog.SchedulerObservationPage(
		time.Now().Add(-time.Minute), time.Now().Add(time.Second), 10, 0)
	if err != nil || len(records) != 2 || records[1].Settings.AppliedAt != nil || records[1].Settings.Error == "" {
		t.Fatalf("refresh failure audit claimed application: %+v %v", records, err)
	}
}

func assertSchedulerSettingsAudit(t *testing.T, persisted *core.ConfigMap, response []byte,
	observationLog *serving.ObservationLog, before, after serving.SchedulerSettings,
) {
	t.Helper()
	var saved struct {
		Reserved map[string]serving.SchedulerAllocation `json:"reserved"`
	}
	if err := json.Unmarshal([]byte(persisted.Data["scheduler.json"]), &saved); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.Reserved, map[string]serving.SchedulerAllocation{"node": {CPUs: []int{0}}}) {
		t.Fatal("operator configuration lost")
	}
	var applied serving.SchedulerSettingsChange
	if err := json.Unmarshal(response, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.ActorUsername != actorUsername || applied.ActorUID != "actor-uid" ||
		!reflect.DeepEqual(applied.Before, before) || !reflect.DeepEqual(applied.After, after) || applied.AppliedAt == nil {
		t.Fatalf("missing actual audit result: %+v", applied)
	}
	records, _, _, err := observationLog.SchedulerObservationPage(
		applied.ValidatedAt.Add(-time.Second), time.Now().Add(time.Second), 10, 0)
	if err != nil || len(records) != 2 || records[0].Settings.AppliedAt != nil || records[1].Settings.AppliedAt == nil {
		t.Fatalf("intent/application ledger=%+v error=%v", records, err)
	}
}

func TestSchedulerCompactReadSharesEvidenceWithoutLosingRetainedVariants(t *testing.T) {
	at := time.Date(2026, 10, 15, 18, 22, 30, 0, time.UTC)
	profile := &serving.Profile{ID: "profile", Reference: evidencestore.ObjectIdentity{Store: "raw", Key: "profile", Size: 123},
		Batches: []serving.BatchProfile{{Batch: 1, DurationNanos: []float64{12, 34}, EnergyPerInputJoules: 0.5}}}
	equalProfile := *profile
	differentProfile := *profile
	differentProfile.Batches = []serving.BatchProfile{{Batch: 1, DurationNanos: []float64{56}}}
	instances := []serving.SchedulerInstance{
		{ID: "physical", TargetID: "selected-slot", Node: serving.NodeReference{UID: "node-uid"}, Profile: profile},
		{ID: "equal", Profile: &equalProfile},
		{ID: "different", Profile: &differentProfile},
		{ID: "missing"},
	}
	candidate := serving.SchedulerCandidate{ID: "selected", Instances: instances}
	decision := serving.SchedulerDecision{Keep: candidate, Target: &candidate}
	snapshot := serving.SchedulerSnapshot{Instances: instances, Options: []serving.SchedulerOption{{Instance: instances[0]}}, InProgressTarget: &candidate}
	event := serving.SchedulerObservation{At: at, Decision: &decision, Snapshot: &snapshot}
	evidence := newSchedulerReadEvidence()
	projected := evidence.observations([]serving.SchedulerObservation{event})
	got := projected[0].Decision.Target.Instances
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
	if projected[0].Snapshot.Options[0].Instance.Profile.Ref != got[0].Profile.Ref ||
		projected[0].Snapshot.InProgressTarget.Instances[0].Profile.Ref != got[0].Profile.Ref {
		t.Fatal("snapshot evidence did not use the same response dictionaries")
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
	if profile.Batches[0].DurationNanos[0] != 12 || instances[0].Profile != profile {
		t.Fatal("read projection mutated the raw source")
	}
}

func TestSchedulerSettingsStaticRoutingChangeNeedsReasonAndRecordsBeforeAfter(t *testing.T) {
	settings := schedulerSettingsForTest()
	settingsRaw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]map[string]int{"old-uid": {"physical": 1}}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "1"},
		Data: map[string]string{"scheduler.json": `{"settings":` + string(settingsRaw) +
			`,"staticRouting":{"old-uid":{"physical":1}},"staticRoutingValidatedAt":"2026-10-01T00:00:00Z"}`},
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
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Writer: settingsArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	controller, execution := settingsControllerForTest(t, kube, observationLog, settings)
	handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, runtime: execution, observationLog: observationLog, config: HandlerConfig{
		Scheduler: controller, SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler", SchedulerConfigKey: "scheduler.json",
		SchedulerLock: &sync.Mutex{}, MaxRequestBytes: 1 << 20, RefreshScheduler: func(context.Context) error { return nil },
	}}
	put := func(extra string) *httptest.ResponseRecorder {
		body := strings.TrimSuffix(string(settingsRaw), "}") + extra + "}"
		request := httptest.NewRequest(http.MethodPut, "/api/v1/scheduler/settings", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer reviewed-token")
		response := httptest.NewRecorder()
		handler.scheduler(response, request)
		return response
	}
	for _, rejected := range []struct{ extra, field string }{
		{`,"staticRouting":{}`, "reason"},
		{`,"staticRouting":{},"reason":"  "`, "reason"},
		{`,"staticRouting":{"absent-uid":{"physical":1}},"reason":"baseline"`, "staticRouting"},
	} {
		response := put(rejected.extra)
		var failure serving.Error
		if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil || response.Code != http.StatusBadRequest || failure.Field != rejected.field || updates != 0 {
			t.Fatalf("%s: status=%d body=%s updates=%d", rejected.extra, response.Code, response.Body, updates)
		}
	}
	// GET settings shows the stored declaration the routing dialog starts from.
	request := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/settings", nil)
	request.Header.Set("Authorization", "Bearer reviewed-token")
	got := httptest.NewRecorder()
	handler.scheduler(got, request)
	var shown struct {
		StaticRouting map[string]map[string]int `json:"staticRouting"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &shown); err != nil || got.Code != http.StatusOK || !reflect.DeepEqual(shown.StaticRouting, declared) {
		t.Fatalf("GET settings lost the stored declaration: status=%d body=%s", got.Code, got.Body)
	}
	// The same declaration is no staticRouting change: it needs no reason and records none.
	unchanged := put(`,"staticRouting":{"old-uid":{"physical":1}},"reason":"stray"`)
	var kept serving.SchedulerSettingsChange
	if err := json.Unmarshal(unchanged.Body.Bytes(), &kept); err != nil || unchanged.Code != http.StatusOK || kept.Reason != "" {
		t.Fatalf("unchanged declaration: status=%d body=%s", unchanged.Code, unchanged.Body)
	}
	response := put(`,"staticRouting":{},"reason":"fixed placement baseline"`)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	var change serving.SchedulerSettingsChange
	if err := json.Unmarshal(response.Body.Bytes(), &change); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(change.BeforeStaticRouting, declared) || change.AfterStaticRouting != nil || change.Reason != "fixed placement baseline" {
		t.Fatalf("staticRouting change record: %+v", change)
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
	if stored.StaticRouting != nil || stored.StaticRoutingValidatedAt == nil || !stored.StaticRoutingValidatedAt.Equal(change.ValidatedAt) {
		t.Fatalf("stored staticRouting does not name its settings change: %s", persisted.Data["scheduler.json"])
	}
	records, _, _, err := observationLog.SchedulerObservationPage(change.ValidatedAt, change.ValidatedAt.Add(time.Millisecond), 10, 0)
	if err != nil || len(records) == 0 || records[0].Settings.Reason != change.Reason {
		t.Fatalf("settings change is not found at its validatedAt: %+v %v", records, err)
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
	observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Writer: settingsArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observationLog.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	controller, execution := settingsControllerForTest(t, fake.NewSimpleClientset(), observationLog, schedulerSettingsForTest())
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
