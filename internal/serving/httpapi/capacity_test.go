package httpapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestSchedulerCapacityProjectionMatchesDashboardFixture(t *testing.T) {
	tests := make([]serving.SchedulerFeedbackRate, 0, 3)
	for _, id := range []string{"instance-failed", "instance-untested", "instance-pass"} {
		tests = append(tests, serving.SchedulerFeedbackRate{
			InstanceID: id,
			Input: serving.CapacityInput{
				WorkloadUID: "service-uid", SLO: serving.SLO{Percentile: 99, TargetLatency: 100 * time.Millisecond},
				Node: serving.NodeReference{Name: "node-a", UID: "node-uid"}, PowerMode: "15W", ProfileID: "profile-id",
				Allocation: serving.SchedulerAllocation{GPUUUID: "gpu-uuid", MemoryBytes: 9007199254740993}, MaxBatch: 4,
				CompletionMarginMS: 2.5, Window: time.Second, Windows: 5, PassingWindows: 4, MinimumRequests: 100,
				GridSteps: 10, BisectionWidth: .01, TailSamples: 20,
			},
			PathFeedback:     serving.PathFeedback{WorkloadUID: "service-uid", Percentile: 99, Source: "configured"},
			FeedbackCapacity: serving.FeedbackCapacity{Tests: []serving.FeedbackRateTest{}},
		})
	}
	tests[0].Tests = []serving.FeedbackRateTest{{InputKey: "failed input / +&", Rate: 40, Met: false, Passed: 0, Requests: 100}}
	tests[0].PathFeedback = serving.PathFeedback{WorkloadUID: "service-uid", Percentile: 99, TotalNanos: 40000000, PostNanos: 30000000, SampleCount: 1, Source: "empirical"}
	tests[2].PathFeedback = serving.PathFeedback{WorkloadUID: "service-uid", Percentile: 99, TotalNanos: 10000000, SampleCount: 2, Source: "empirical"}
	tests[2].KappaHat = 12.5
	tests[2].Tests = []serving.FeedbackRateTest{
		{InputKey: "pass input", Rate: 12.5, Met: true, Passed: 4, Requests: 100},
		{InputKey: "pass input", Rate: 20, Met: false, Passed: 1, Requests: 100},
	}
	response := httptest.NewRecorder()
	sendSchedulerCapacity(response, tests)
	if response.Code != http.StatusOK {
		t.Fatalf("capacity status: %d", response.Code)
	}
	fixture, err := os.ReadFile("../../../chill-dashboard/lib/fixtures/scheduler-feedback-capacity.json")
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	for _, value := range []struct {
		data   []byte
		target *any
	}{{response.Body.Bytes(), &got}, {fixture, &want}} {
		decoder := json.NewDecoder(bytes.NewReader(value.data))
		decoder.UseNumber()
		if err := decoder.Decode(value.target); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("HTTP projection differs from frontend contract fixture: %s", response.Body)
	}
}

func TestSchedulerCapacityEmptyEvidenceIsSuccessfulArray(t *testing.T) {
	response := httptest.NewRecorder()
	sendSchedulerCapacity(response, nil)
	var got map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || string(got["tests"]) != "[]" || len(got) != 1 {
		t.Fatalf("empty current evidence: %d %s", response.Code, response.Body)
	}
}

func TestSchedulerCapacityKeepsAuthorizationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		allowed     bool
		status      int
	}{
		{name: "missing token", status: http.StatusUnauthorized},
		{name: "forbidden", token: "token", status: http.StatusForbidden},
		{name: "authorized scheduler unavailable", token: "token", allowed: true, status: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kube := fake.NewSimpleClientset()
			kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{
					Authenticated: true, User: authenticationv1.UserInfo{Username: "operator", UID: "operator-uid"},
				}}, nil
			})
			kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				sar := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				if sar.Spec.NonResourceAttributes == nil || sar.Spec.NonResourceAttributes.Path != "/chill/api/v1/scheduler/capacity" || sar.Spec.NonResourceAttributes.Verb != measurementGetVerb {
					t.Fatalf("changed capacity authorization: %+v", sar.Spec)
				}
				return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: tc.allowed}}, nil
			})
			handler := &Handler{authorizer: &servingkube.Authorizer{Client: kube}}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/scheduler/capacity", nil)
			if tc.token != "" {
				request.Header.Set("Authorization", "Bearer "+tc.token)
			}
			response := httptest.NewRecorder()
			handler.schedulerCapacity(response, request)
			if response.Code != tc.status {
				t.Fatalf("authorization status: %d %s", response.Code, response.Body)
			}
		})
	}
}
