package entrypoint

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

func schedulerSettingsForTest() serving.SchedulerSettings {
	return serving.SchedulerSettings{ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second, MaxObservationAge: time.Second, Replays: 1, MaxCandidates: 1, MaxReplicas: 1, MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 1}
}

type settingsArchive struct{ directory string }

func (a settingsArchive) PutImmutable(_ context.Context, identity evidencestore.ObjectIdentity, input io.Reader) (evidencestore.Receipt, error) {
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
	return evidencestore.Receipt{SchemaVersion: evidencestore.ReceiptSchemaVersion, Object: identity, Outcome: evidencestore.CreateOutcomeCreated}, nil
}

func settingsControllerForTest(t *testing.T, kube kubernetes.Interface, journal *serving.Journal, settings serving.SchedulerSettings) *servingkube.SchedulerController {
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
	execution, err := serving.NewRuntime(context.Background(), serving.RuntimeConfig{QueueLimit: 8, Journal: journal})
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
	observer, err := servingkube.NewObserver(store, kube, execution, &sdk.Client{}, servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	operations, err := servingkube.NewOperations(store, kube, execution, observer, &sdk.Client{}, servingkube.OperationConfig{Namespace: "serving", PollInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := servingkube.NewSchedulerController(store, kube, execution, observer, operations, journal, servingkube.SchedulerControllerConfig{Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	return controller
}

func TestSchedulerSettingsPersistAndApplyFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name                                                            string
		resourceAllowed, updateFailure, journalFailure, invalidSettings bool
		want                                                            int
	}{
		{name: "persist and apply", resourceAllowed: true, want: http.StatusOK}, {name: "resource denial", want: http.StatusForbidden}, {name: "write failure", resourceAllowed: true, updateFailure: true, want: http.StatusServiceUnavailable}, {name: "journal failure prevents write", resourceAllowed: true, journalFailure: true, want: http.StatusServiceUnavailable}, {name: "validation prevents write", resourceAllowed: true, invalidSettings: true, want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := schedulerSettingsForTest()
			after := before
			after.Seed = 99
			if tc.invalidSettings {
				after.ObservationWindow = 0
			}
			beforeRaw, err := json.Marshal(before)
			if err != nil {
				t.Fatal(err)
			}
			configRaw := `{"settings":` + string(beforeRaw) + `,"cpuInventory":{"node":[0]},"gpuInventory":{"node":["GPU-real"]},"reserved":{"node":{"cpus":[0]}}}`
			kube := fake.NewSimpleClientset(&core.ConfigMap{ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "1"}, Data: map[string]string{"scheduler.json": configRaw, "other": "untouched"}})
			kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "operator", UID: "actor-uid", Groups: []string{"scheduler-writers"}}}}, nil
			})
			kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				sar := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				allowed := sar.Spec.NonResourceAttributes != nil || tc.resourceAllowed
				return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: allowed}}, nil
			})
			updates := 0
			kube.PrependReactor("update", "configmaps", func(ktesting.Action) (bool, runtime.Object, error) {
				updates++
				if tc.updateFailure {
					return true, nil, errors.New("storage unavailable")
				}
				return false, nil, nil
			})
			journal, err := serving.NewJournal(serving.JournalConfig{
				Directory: t.TempDir(), Store: "evidence", Window: time.Hour,
				Writer: settingsArchive{directory: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := journal.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			controller := settingsControllerForTest(t, kube, journal, before)
			if tc.journalFailure {
				if err = journal.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, journal: journal, config: HandlerConfig{Scheduler: controller, SchedulerNamespace: "serving", SchedulerConfigMap: "scheduler", SchedulerConfigKey: "scheduler.json", SchedulerLock: &sync.Mutex{}, MaxRequestBytes: 1 << 20}}
			raw, err := json.Marshal(after)
			if err != nil {
				t.Fatal(err)
			}
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
				if controller.Settings() != before || persisted.Data["scheduler.json"] != configRaw {
					t.Fatal("failed settings change altered running or persisted policy")
				}
				if (tc.journalFailure || !tc.resourceAllowed || tc.invalidSettings) && updates != 0 {
					t.Fatal("denied/unrecorded/invalid settings performed CM write")
				}
				return
			}
			if controller.Settings() != after || updates != 1 || persisted.Data["other"] != "untouched" {
				t.Fatal("settings not durably applied or unrelated CM data altered")
			}
			assertSchedulerSettingsAudit(t, persisted, response.Body.Bytes(), journal, before, after)
		})
	}
}

func assertSchedulerSettingsAudit(t *testing.T, persisted *core.ConfigMap, response []byte,
	journal *serving.Journal, before, after serving.SchedulerSettings,
) {
	t.Helper()
	var saved map[string]json.RawMessage
	if err := json.Unmarshal([]byte(persisted.Data["scheduler.json"]), &saved); err != nil {
		t.Fatal(err)
	}
	if string(saved["reserved"]) != `{"node":{"cpus":[0]}}` ||
		string(saved["gpuInventory"]) != `{"node":["GPU-real"]}` ||
		string(saved["cpuInventory"]) != `{"node":[0]}` {
		t.Fatal("operator configuration lost")
	}
	var applied serving.SchedulerSettingsChange
	if err := json.Unmarshal(response, &applied); err != nil {
		t.Fatal(err)
	}
	if applied.ActorUsername != "operator" || applied.ActorUID != "actor-uid" ||
		applied.Before != before || applied.After != after || applied.AppliedAt == nil {
		t.Fatalf("missing actual audit result: %+v", applied)
	}
	records, _, err := journal.SchedulerObservationPage(
		applied.ValidatedAt.Add(-time.Second), time.Now().Add(time.Second), 10, 0)
	if err != nil || len(records) != 2 || records[0].Settings.AppliedAt != nil || records[1].Settings.AppliedAt == nil {
		t.Fatalf("intent/application ledger=%+v error=%v", records, err)
	}
}
