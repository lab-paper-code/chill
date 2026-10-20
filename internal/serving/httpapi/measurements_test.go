package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

const (
	measurementTestNamespace = "allowed"
	measurementTestName      = "task"
)

func TestMeasurementCallerResourceDenialNeverInvokesService(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		for _, denyPath := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/resource-denial", true: "/path-denial"}[denyPath], func(t *testing.T) {
				kube := fake.NewSimpleClientset()
				kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{Authenticated: true, User: authentication.UserInfo{Username: "caller", UID: "caller-uid", Groups: []string{"operators"}, Extra: map[string]authentication.ExtraValue{"tenant": {"reviewed"}}}}}, nil
				})
				resourceCalls := 0
				kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
					review := action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview)
					if review.Spec.User != "caller" || review.Spec.UID != "caller-uid" || review.Spec.Groups[0] != "operators" || review.Spec.Extra["tenant"][0] != "reviewed" {
						t.Fatal("owner service account replaced caller")
					}
					if resource := review.Spec.ResourceAttributes; resource != nil {
						resourceCalls++
						name := ""
						if method == http.MethodDelete {
							name = measurementTestName
						}
						if resource.Namespace != measurementTestNamespace || resource.Group != api.GroupVersion.Group || resource.Version != api.GroupVersion.Version || resource.Resource != "batchenergyoptimizations" || resource.Name != name || resource.Verb != map[string]string{http.MethodGet: "list", http.MethodPost: "create", http.MethodDelete: "delete"}[method] {
							t.Fatalf("incorrect native resource SAR: %+v", resource)
						}
						return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: false}}, nil
					}
					if review.Spec.NonResourceAttributes.Path != "/chill/api/v1/namespaces/allowed/batch-energy-optimizations" {
						t.Fatalf("incorrect owner path SAR: %+v", review.Spec)
					}
					return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: !denyPath}}, nil
				})
				serviceCalls := 0
				handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{
					MaxRequestBytes: 1 << 20,
					ListMeasurements: func(context.Context, string) (api.BatchEnergyOptimizationList, error) {
						serviceCalls++
						return api.BatchEnergyOptimizationList{}, nil
					},
					SubmitMeasurement: func(context.Context, api.BatchEnergyOptimization, bool) (MeasurementMutationResult, error) {
						serviceCalls++
						return MeasurementMutationResult{}, nil
					},
					DeleteMeasurement: func(context.Context, string, string, string, string) (MeasurementMutationResult, error) {
						serviceCalls++
						return MeasurementMutationResult{}, nil
					},
				}}
				request := httptest.NewRequest(method, "/api/v1/namespaces/allowed/batch-energy-optimizations", strings.NewReader(`{}`))
				request.SetPathValue("namespace", measurementTestNamespace)
				if method == http.MethodDelete {
					request.SetPathValue("name", measurementTestName)
				}
				request.Header.Set("Authorization", "Bearer actual-caller-token")
				response := httptest.NewRecorder()
				handler.measurements(response, request)
				if response.Code != http.StatusForbidden || serviceCalls != 0 || (resourceCalls == 0) != denyPath {
					t.Fatalf("code=%d owner calls=%d resource SAR=%d", response.Code, serviceCalls, resourceCalls)
				}
			})
		}
	}
}

func TestMeasurementInvalidIdentityAndDryRunCannotInvokeService(t *testing.T) {
	for _, tc := range []struct{ method, body, query string }{
		{http.MethodPost, `{"apiVersion":"edge.dacs.io/v1alpha1","kind":"BatchEnergyOptimization","metadata":{"name":"task","namespace":"outside"}}`, ""},
		{http.MethodPost, `{"apiVersion":"edge.dacs.io/v1alpha1","kind":"BatchEnergyOptimization","metadata":{"name":"task"}}`, "?dryRun=server"},
		{http.MethodDelete, ``, "?uid=native-uid"},
	} {
		kube := fake.NewSimpleClientset()
		kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{Authenticated: true, User: authentication.UserInfo{Username: "caller"}}}, nil
		})
		kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
			return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: true}}, nil
		})
		calls := 0
		h := &Handler{authorizer: &servingkube.Authorizer{Client: kube}, config: HandlerConfig{MaxRequestBytes: 1 << 20, SubmitMeasurement: func(context.Context, api.BatchEnergyOptimization, bool) (MeasurementMutationResult, error) {
			calls++
			return MeasurementMutationResult{}, nil
		}, DeleteMeasurement: func(context.Context, string, string, string, string) (MeasurementMutationResult, error) {
			calls++
			return MeasurementMutationResult{}, nil
		}}}
		r := httptest.NewRequest(tc.method, "/api/v1/namespaces/allowed/batch-energy-optimizations"+tc.query, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer caller-token")
		r.SetPathValue("namespace", measurementTestNamespace)
		r.SetPathValue("name", measurementTestName)
		w := httptest.NewRecorder()
		h.measurements(w, r)
		if w.Code != http.StatusBadRequest || calls != 0 {
			t.Fatalf("invalid boundary reached owner: code=%d calls=%d", w.Code, calls)
		}
	}
}

func TestMeasurementObservationLogPreservesReviewedActorAndActualMutationPhase(t *testing.T) {
	cases := []struct {
		name, method, suffix, phase, failure string
		status                               int
		noObservationLog, partial            bool
	}{
		{name: "accepted", method: http.MethodPost, phase: "Accepted", status: http.StatusAccepted},
		{name: "dry run", method: http.MethodPost, suffix: "?dryRun=All", phase: "NOT_SUBMITTED", status: http.StatusOK},
		{name: "preflight rejected", method: http.MethodPost, phase: measurementAuditFailed,
			failure: "MeasurementPreflightRejected", status: http.StatusBadRequest},
		{name: "partial evidence failure", method: http.MethodDelete, suffix: "/task?uid=native-existing",
			phase: measurementAuditFailed, failure: "MeasurementEvidenceDeletionIncomplete",
			status: http.StatusServiceUnavailable, partial: true},
		{name: "no journal", method: http.MethodPost, noObservationLog: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			started := time.Now().UTC().Add(-time.Second)
			observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{
				Directory: t.TempDir(), Store: "evidence",
				Writer: settingsArchive{directory: t.TempDir()},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := observationLog.Close(ctx); err != nil {
					t.Error(err)
				}
			})
			kube := fake.NewSimpleClientset()
			kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{
					Authenticated: true, User: authentication.UserInfo{Username: "reviewed-operator", UID: "reviewed-uid"},
				}}, nil
			})
			kube.PrependReactor("create", "subjectaccessreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authorization.SubjectAccessReview{
					Status: authorization.SubjectAccessReviewStatus{Allowed: true},
				}, nil
			})
			h := &Handler{
				observationLog: observationLog, authorizer: &servingkube.Authorizer{Client: kube},
				config: HandlerConfig{MaxRequestBytes: 1 << 20},
			}
			calls := 0
			assertStarted := func() {
				calls++
				events, _, _, err := observationLog.SchedulerObservationPage(started, time.Now().UTC().Add(time.Second), 10, 0)
				if err != nil || len(events) != 1 || events[0].Action == nil ||
					events[0].Action.Phase != "validated" {
					t.Fatal("mutation began without its durable validated actor/target record")
				}
			}
			h.config.SubmitMeasurement = func(
				_ context.Context, task api.BatchEnergyOptimization, _ bool,
			) (MeasurementMutationResult, error) {
				assertStarted()
				if tc.failure != "" {
					return MeasurementMutationResult{}, &serving.Error{Code: tc.failure, Message: "actual unresolved input"}
				}
				task.UID = "native-created"
				task.ResourceVersion = "8"
				return MeasurementMutationResult{Request: tc.phase, Task: &task}, nil
			}
			h.config.DeleteMeasurement = func(
				context.Context, string, string, string, string,
			) (MeasurementMutationResult, error) {
				assertStarted()
				task := &api.BatchEnergyOptimization{}
				task.Namespace = measurementTestNamespace
				task.Name = measurementTestName
				task.UID = "native-existing"
				task.ResourceVersion = "7"
				result := MeasurementMutationResult{
					Task: task, DeletedEvidence: []MeasurementEvidenceLocation{{Store: "evidence", Key: "private/raw"}},
				}
				return result, &serving.Error{Code: tc.failure, Message: "some owned evidence removed; native retained"}
			}
			body := `{"apiVersion":"edge.dacs.io/v1alpha1","kind":"BatchEnergyOptimization","metadata":{"name":"task"}}`
			if tc.noObservationLog {
				h.observationLog = nil
			}
			path := "/api/v1/namespaces/allowed/batch-energy-optimizations" + tc.suffix
			r := httptest.NewRequest(tc.method, path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer private-caller-token")
			r.SetPathValue("namespace", measurementTestNamespace)
			r.SetPathValue("name", measurementTestName)
			if tc.method == http.MethodDelete {
				r.Header.Set("If-Match", `"7"`)
			}
			w := httptest.NewRecorder()
			h.measurements(w, r)
			if tc.noObservationLog {
				if w.Code != http.StatusServiceUnavailable || calls != 0 {
					t.Fatal("unrecorded mutation reached owner")
				}
				return
			}
			events, _, _, err := observationLog.SchedulerObservationPage(started, time.Now().UTC().Add(time.Second), 10, 0)
			if err != nil || w.Code != tc.status || calls != 1 || len(events) != 2 {
				t.Fatalf("audit/response mismatch code=%d events=%+v err=%v", w.Code, events, err)
			}
			assertMeasurementAuditActor(t, events, tc.method)
			assertMeasurementAuditResult(t, events[1].Action, tc.phase, tc.failure, tc.partial)
		})
	}
}

func assertMeasurementAuditActor(t *testing.T, events []serving.SchedulerObservation, method string) {
	t.Helper()
	for _, event := range events {
		if event.Action == nil {
			t.Fatal("journal omitted mutation action")
		}
		var actor struct{ ActorUsername, ActorUID, Namespace, Name, UID string }
		if json.Unmarshal(event.Action.Expected, &actor) != nil ||
			actor.ActorUsername != "reviewed-operator" || actor.ActorUID != "reviewed-uid" ||
			actor.Namespace != measurementTestNamespace || actor.Name != measurementTestName {
			t.Fatal("journal lost reviewed actor or exact native target")
		}
		if method == http.MethodDelete && actor.UID != "native-existing" {
			t.Fatal("journal lost delete UID fence")
		}
		if strings.Contains(string(event.Action.Expected), "private-caller-token") ||
			strings.Contains(string(event.Action.Actual), "store") {
			t.Fatal("audit leaked bearer or evidence location")
		}
	}
}

func assertMeasurementAuditResult(
	t *testing.T, action *serving.SchedulerActionObservation, phase, failure string, partial bool,
) {
	t.Helper()
	if action == nil || action.Phase != phase || action.CompletedAt == nil || action.Error != failure {
		t.Fatal("journal lost actual mutation phase/failure")
	}
	if partial {
		var progress struct {
			CleanedObjects  int
			EvidenceDeleted bool
		}
		if json.Unmarshal(action.Actual, &progress) != nil ||
			progress.CleanedObjects != 1 || progress.EvidenceDeleted {
			t.Fatal("partial cleanup progress was not retained honestly")
		}
	}
	if phase == "Accepted" {
		var actual struct{ UID, ResourceVersion string }
		if json.Unmarshal(action.Actual, &actual) != nil ||
			actual.UID != "native-created" || actual.ResourceVersion != "8" {
			t.Fatal("audit omitted admitted native identity")
		}
	}
}
