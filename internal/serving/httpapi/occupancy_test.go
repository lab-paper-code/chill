package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const occupancyTestBody = `{"node":"node","batchEnergyOptimization":"beo","workload":"workload","batch":2}`
const occupancyActorTest = "actor-uid"

func occupancyHandler(t *testing.T, deny string) (*Handler, *[]authorization.ResourceAttributes) {
	t.Helper()
	log, err := serving.NewObservationLog(
		serving.ObservationLogConfig{
			Directory: t.TempDir(),
			Store:     "evidence",
			Retention: time.Hour,
			Writer:    settingsArchive{directory: t.TempDir()},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	kube := fake.NewSimpleClientset()
	kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, &authentication.TokenReview{
			Status: authentication.TokenReviewStatus{
				Authenticated: true,
				User:          authentication.UserInfo{Username: "operator", UID: occupancyActorTest},
			},
		}, nil
	})
	reviews := new([]authorization.ResourceAttributes)
	kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
		review := action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview)
		if review.Spec.User != "operator" || review.Spec.UID != occupancyActorTest {
			t.Fatal("caller replaced")
		}
		allowed := deny != "path"
		if attrs := review.Spec.ResourceAttributes; attrs != nil {
			*reviews = append(*reviews, *attrs)
			allowed = attrs.Verb != deny
		}
		return true, &authorization.SubjectAccessReview{
			Status: authorization.SubjectAccessReviewStatus{Allowed: allowed},
		}, nil
	})
	return &Handler{
		observationLog: log,
		authorizer:     &servingkube.Authorizer{Client: kube},
		config:         HandlerConfig{MaxRequestBytes: 1024},
	}, reviews
}

func occupancyRequest(method, suffix, body string) *http.Request {
	r := httptest.NewRequest(
		method,
		"/api/v1/namespaces/allowed/dispatch-occupancy-measurements"+suffix,
		strings.NewReader(body),
	)
	r.Header.Set("Authorization", "Bearer caller-token")
	r.SetPathValue("namespace", measurementTestNamespace)
	if method == http.MethodDelete {
		r.SetPathValue("name", "task")
		r.Header.Set("If-Match", `"7"`)
	}
	return r
}

func TestOccupancyHTTPIntentOnlyAndBounded(t *testing.T) {
	bodies := []string{`{}`, occupancyTestBody + ` {}`, strings.Repeat(" ", 1025) + occupancyTestBody,
		`{"node":"node","batchEnergyOptimization":"beo","workload":"workload","batch":0}`}
	for _, field := range []string{"warmupSeconds", "durationSeconds", "workers", "minimumBatches", "requestLifetimeSeconds", "supplyMode", "maximumIdleFraction", "plannedElapsedSeconds", "conditions", "model", "executionPath", "powerMode"} {
		bodies = append(bodies, strings.TrimSuffix(occupancyTestBody, "}")+`,"`+field+`":null}`)
	}
	for _, body := range bodies {
		h, _ := occupancyHandler(t, "")
		calls := 0
		h.config.BuildOccupancy = func(context.Context, string, OccupancyIntent) (*api.DispatchOccupancyMeasurement, error) {
			calls++
			return nil, nil
		}
		h.config.SubmitOccupancy = func(context.Context, api.DispatchOccupancyMeasurement, bool) (OccupancyMutationResult, error) {
			calls++
			return OccupancyMutationResult{}, nil
		}
		w := httptest.NewRecorder()
		h.measurements(w, occupancyRequest(http.MethodPost, "", body))
		if w.Code != http.StatusBadRequest || calls != 0 {
			t.Fatalf("body reached builder: %s status=%d calls=%d", body, w.Code, calls)
		}
	}
	for _, suffix := range []string{"?dryRun=no", "?dryRun=All&dryRun=All"} {
		h, _ := occupancyHandler(t, "")
		w := httptest.NewRecorder()
		h.measurements(w, occupancyRequest(http.MethodPost, suffix, occupancyTestBody))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("invalid dryRun accepted: %d", w.Code)
		}
	}
}

func TestOccupancyHTTPAuditBeforeEffectLiveAfterCommit(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		for _, dry := range []bool{false, true} {
			if method == http.MethodDelete && dry {
				continue
			}
			h, reviews := occupancyHandler(t, "")
			start := time.Now().Add(-time.Second)
			observations := func() []serving.SchedulerObservation {
				page, _, _, _, err := h.observationLog.SchedulerObservationPage(
					context.Background(),
					start,
					time.Now().Add(time.Second),
					10,
					0,
					serving.SchedulerObservationSelection{},
				)
				if err != nil {
					t.Fatal(err)
				}
				return page
			}
			liveCalls, effects := 0, 0
			task := api.DispatchOccupancyMeasurement{
				TypeMeta:   meta.TypeMeta{APIVersion: api.GroupVersion.String(), Kind: "DispatchOccupancyMeasurement"},
				ObjectMeta: meta.ObjectMeta{Namespace: measurementTestNamespace, Name: "task"},
				Spec: api.DispatchOccupancyMeasurementSpec{
					Node:       "node",
					Batch:      2,
					Workers:    17,
					Conditions: api.DispatchOccupancyConditions{InputsJSON: `{"configuration":{"uid":"config"}}`},
				},
			}
			h.config.BuildOccupancy = func(_ context.Context, ns string, intent OccupancyIntent) (*api.DispatchOccupancyMeasurement, error) {
				if ns != measurementTestNamespace || intent.Node != "node" || intent.Batch != 2 {
					t.Fatal("lost intent")
				}
				return &task, nil
			}
			h.config.OnMutationAction = func(action serving.SchedulerActionObservation, _ time.Time) {
				liveCalls++
				page := observations()
				if len(page) != liveCalls || page[len(page)-1].Action.Phase != action.Phase {
					t.Fatal("live before durable commit")
				}
				if liveCalls == 2 && effects != 1 {
					t.Fatal("completion before effect")
				}
			}
			effect := func() {
				effects++
				if len(observations()) != 1 || liveCalls != 1 {
					t.Fatal("effect before audit intent")
				}
			}
			h.config.SubmitOccupancy = func(_ context.Context, in api.DispatchOccupancyMeasurement, isDry bool) (OccupancyMutationResult, error) {
				effect()
				if isDry != dry || !reflect.DeepEqual(in.Spec, task.Spec) {
					t.Fatal("frozen task changed")
				}
				state := "Accepted"
				if dry {
					state = "NOT_SUBMITTED"
				}
				return OccupancyMutationResult{Task: &in, Request: state}, nil
			}
			h.config.DeleteOccupancy = func(_ context.Context, ns, name, uid, version string) (OccupancyMutationResult, error) {
				effect()
				if ns != measurementTestNamespace || name != "task" || uid != "dom-uid" || version != "7" {
					t.Fatal("delete fence lost")
				}
				return OccupancyMutationResult{Task: &task, Request: "DeletionRequested", EvidenceDeleted: true}, nil
			}
			suffix := ""
			if dry {
				suffix = "?dryRun=All"
			}
			if method == http.MethodDelete {
				suffix = "/task?uid=dom-uid"
			}
			w := httptest.NewRecorder()
			h.measurements(w, occupancyRequest(method, suffix, occupancyTestBody))
			status := http.StatusAccepted
			if dry {
				status = http.StatusOK
			}
			if w.Code != status || liveCalls != 2 || effects != 1 {
				t.Fatalf("status=%d body=%s live=%d effect=%d", w.Code, w.Body.String(), liveCalls, effects)
			}
			assertOccupancyAudit(t, *reviews, observations(), method, task.Spec)
		}
	}
}

func assertOccupancyAudit(t *testing.T, reviews []authorization.ResourceAttributes,
	page []serving.SchedulerObservation, method string, spec api.DispatchOccupancyMeasurementSpec,
) {
	t.Helper()
	if len(reviews) != 1 || reviews[0].Resource != "dispatchoccupancymeasurements" ||
		reviews[0].Namespace != measurementTestNamespace {
		t.Fatal("incorrect resource SAR", reviews)
	}
	expectedVerb, expectedName := "create", ""
	if method == http.MethodDelete {
		expectedVerb, expectedName = "delete", "task"
	}
	if reviews[0].Verb != expectedVerb || reviews[0].Name != expectedName {
		t.Fatal("incorrect named SAR", reviews)
	}
	var expected struct {
		ActorUsername, ActorUID string
		RequestedSpec           api.DispatchOccupancyMeasurementSpec
	}
	if err := json.Unmarshal(page[0].Action.Expected, &expected); err != nil {
		t.Fatal(err)
	}
	if expected.ActorUsername != "operator" || expected.ActorUID != occupancyActorTest {
		t.Fatal("audit lost verified actor")
	}
	if method == http.MethodPost && !reflect.DeepEqual(expected.RequestedSpec, spec) {
		t.Fatal("audit lost frozen protocol")
	}
}

func TestOccupancyHTTPDenialAndServiceErrors(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		verb := "create"
		suffix := ""
		if method == http.MethodDelete {
			verb = "delete"
			suffix = "/task?uid=dom-uid"
		}
		for _, deny := range []string{"path", verb} {
			h, _ := occupancyHandler(t, deny)
			w := httptest.NewRecorder()
			h.measurements(w, occupancyRequest(method, suffix, occupancyTestBody))
			if w.Code != http.StatusForbidden {
				t.Fatalf("denied %s %s got %d", method, deny, w.Code)
			}
		}
	}
	for _, tc := range []struct {
		code   string
		status int
	}{{"OccupancyPolicyUnavailable", 503}, {"OccupancyInfeasible", 400}} {
		h, _ := occupancyHandler(t, "")
		h.config.BuildOccupancy = func(context.Context, string, OccupancyIntent) (*api.DispatchOccupancyMeasurement, error) {
			return nil, &serving.Error{Code: tc.code, Message: "reason"}
		}
		h.config.SubmitOccupancy = func(context.Context, api.DispatchOccupancyMeasurement, bool) (OccupancyMutationResult, error) {
			t.Fatal("unavailable build submitted")
			return OccupancyMutationResult{}, nil
		}
		w := httptest.NewRecorder()
		h.measurements(w, occupancyRequest(http.MethodPost, "", occupancyTestBody))
		if w.Code != tc.status {
			t.Fatalf("%s status=%d", tc.code, w.Code)
		}
	}
	for _, suffix := range []string{"/task", "/task?uid=x&uid=y", "/task?uid=%20"} {
		h, _ := occupancyHandler(t, "")
		w := httptest.NewRecorder()
		h.measurements(w, occupancyRequest(http.MethodDelete, suffix, ""))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bad delete accepted %d", w.Code)
		}
	}
	h, _ := occupancyHandler(t, "")
	r := occupancyRequest(http.MethodDelete, "/task?uid=dom-uid", "")
	r.Header.Del("If-Match")
	w := httptest.NewRecorder()
	h.measurements(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatal("unfenced deletion accepted")
	}
}
