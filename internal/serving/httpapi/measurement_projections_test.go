package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authentication "k8s.io/api/authentication/v1"
	authorization "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestMeasurementProjectionWhitelistAndOriginalReferences(t *testing.T) {
	metadata := meta.ObjectMeta{Namespace: "allowed", Name: "measurement", UID: "original-uid", ResourceVersion: "42", Annotations: map[string]string{"secret": "hidden"}}
	for _, phase := range []api.MeasurementPhase{api.MeasurementPhaseMeasuring, api.MeasurementPhaseFailed, api.MeasurementPhaseInsufficient} {
		occupancy := api.DispatchOccupancyMeasurement{ObjectMeta: metadata, Spec: api.DispatchOccupancyMeasurementSpec{Node: "node", Workload: "workload", BatchEnergyOptimization: "option", Batch: 8, Workers: 16, DurationSeconds: 60, Conditions: api.DispatchOccupancyConditions{InputsJSON: "secret-input"}}, Status: api.DispatchOccupancyMeasurementStatus{Phase: phase, Reason: "original-reason"}}
		p := ProjectDispatchOccupancyMeasurement(occupancy)
		if len(p.References.Evidence) != 0 || p.References.CR.UID != "original-uid" {
			t.Fatalf("lost original identity: %+v", p)
		}
		raw, err := json.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"inputsJSON", "secret-input", "annotations", "perRequestNanos", "supplyMode", "\"result\"", "\"spec\""} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("projection leaked %s: %s", forbidden, raw)
			}
		}
		if !strings.Contains(string(raw), string(phase)) || !strings.Contains(string(raw), "original-reason") {
			t.Fatalf("lost status: %s", raw)
		}
		occupancy.Status.Result = &api.ProfilingRunEvidenceReference{Store: "original-store", Key: "original-key", Size: 123}
		p = ProjectDispatchOccupancyMeasurement(occupancy)
		if p.References.Evidence[0] != (MeasurementEvidenceLocation{Store: "original-store", Key: "original-key"}) {
			t.Fatal("result reference changed")
		}
		raw, _ = json.Marshal(p)
		if strings.Contains(string(raw), "size") {
			t.Fatalf("result exposed non-whitelisted metadata: %s", raw)
		}
	}
	idle := int32(7)
	operation := api.OperationCostMeasurement{ObjectMeta: metadata, Spec: api.OperationCostMeasurementSpec{Node: "node", Steps: []api.OperationCostStep{{Name: "idle", Kind: api.OperationCostStepIdle, IdleDurationSeconds: &idle}}}, Status: api.OperationCostMeasurementStatus{Phase: api.MeasurementPhaseFailed, Steps: []api.OperationCostStepStatus{{Name: "idle", Key: "saved-key", Verdict: "Rejected"}, {Name: "pending"}}}}
	duration, zero := int64(123456789), 0.0
	p := ProjectOperationCostMeasurement(operation, "configured-store", []servingkube.OperationStepProjection{
		{OperationID: "original-uid.0.idle", DurationNanos: &duration, WallJoules: &zero, Reason: "recorded-reason", FromPowerMode: "MAXN", ToPowerMode: "MODE_15W", Model: "frozen-model"},
		{},
	})
	if len(p.References.Evidence) != 1 || p.References.Evidence[0].Key != "saved-key" || p.References.Evidence[0].Store != "configured-store" {
		t.Fatalf("invented or lost evidence: %+v", p)
	}
	conditions := p.Conditions.(operationConditions)
	if *conditions.Steps[0].IdleDurationSeconds != idle {
		t.Fatal("condition changed")
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"operationID":"original-uid.0.idle"`, `"durationNanos":123456789`, `"wallJoules":0`, `"reason":"recorded-reason"`, `"durationNanos":null`, `"wallJoules":null`, `"phase":"Failed"`, `"verdict":"Rejected"`, `"fromPowerMode":"MAXN"`, `"toPowerMode":"MODE_15W"`, `"model":"frozen-model"`} {
		if !strings.Contains(string(raw), required) {
			t.Fatalf("lost operation status %s: %s", required, raw)
		}
	}
	for _, forbidden := range []string{"stateObservations", "sampling", "prepare", "annotations", "secret"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("operation projection leaked %s: %s", forbidden, raw)
		}
	}
	status := p.Status.(operationStatus)
	if status.Steps[0].FromPowerMode != "MAXN" || status.Steps[0].ToPowerMode != "MODE_15W" || status.Steps[0].Model != "frozen-model" {
		t.Fatalf("lost safe recorded metadata: %+v", status.Steps[0])
	}
	if status.Steps[1].FromPowerMode != "" || status.Steps[1].ToPowerMode != "" || status.Steps[1].Model != "" {
		t.Fatalf("invented missing metadata: %+v", status.Steps[1])
	}
}

func TestMeasurementProjectionListGetCallerAuthorization(t *testing.T) {
	for _, resource := range []string{"operationcostmeasurements", "dispatchoccupancymeasurements"} {
		for _, name := range []string{"", "measurement"} {
			for _, allowed := range []bool{false, true} {
				t.Run(resource+"/"+name+map[bool]string{false: "/denied", true: "/allowed"}[allowed], func(t *testing.T) {
					kube := fake.NewSimpleClientset()
					kube.PrependReactor("create", "tokenreviews", func(ktesting.Action) (bool, runtime.Object, error) {
						return true, &authentication.TokenReview{Status: authentication.TokenReviewStatus{Authenticated: true, User: authentication.UserInfo{Username: "caller"}}}, nil
					})
					resourceCalls := 0
					kube.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
						review := action.(ktesting.CreateAction).GetObject().(*authorization.SubjectAccessReview)
						permit := true
						if r := review.Spec.ResourceAttributes; r != nil {
							resourceCalls++
							verb := "list"
							if name != "" {
								verb = "get"
							}
							if review.Spec.User != "caller" || r.Namespace != "allowed" || r.Name != name || r.Resource != resource || r.Verb != verb {
								t.Fatalf("wrong caller SAR: %+v", review.Spec)
							}
							permit = allowed
						}
						return true, &authorization.SubjectAccessReview{Status: authorization.SubjectAccessReviewStatus{Allowed: permit}}, nil
					})
					h := &Handler{authorizer: &servingkube.Authorizer{Client: kube}}
					r := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/allowed/measurements", nil)
					r.Header.Set("Authorization", "Bearer caller-token")
					r.SetPathValue("namespace", "allowed")
					r.SetPathValue("name", name)
					calls := 0
					list := func(context.Context, string) (MeasurementProjectionList, error) {
						calls++
						return MeasurementProjectionList{Items: []MeasurementProjection{}}, nil
					}
					get := func(context.Context, string, string) (MeasurementProjection, error) {
						calls++
						return MeasurementProjection{}, nil
					}
					w := httptest.NewRecorder()
					h.measurementProjectionRead(w, r, resource, list, get)
					expected := http.StatusForbidden
					expectedCalls := 0
					if allowed {
						expected = http.StatusOK
						expectedCalls = 1
					}
					if w.Code != expected || calls != expectedCalls || resourceCalls != 1 {
						t.Fatalf("code=%d calls=%d SARs=%d", w.Code, calls, resourceCalls)
					}
					if allowed && name == "" && strings.TrimSpace(w.Body.String()) != `{"items":[]}` {
						t.Fatalf("empty list: %s", w.Body.String())
					}
				})
			}
		}
	}
}
