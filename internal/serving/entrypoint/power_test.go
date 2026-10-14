package entrypoint

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/powerwindow"
	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	authenticationv1 "k8s.io/api/authentication/v1"
	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestOperatingPowerRequiresReviewedReadAndExplicitInterval(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		status int
	}{
		{"missing-token", http.StatusUnauthorized}, {"denied", http.StatusForbidden},
		{"missing-from", http.StatusBadRequest}, {"missing-to", http.StatusBadRequest},
		{"reversed", http.StatusBadRequest}, {"measured-zero", http.StatusOK},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			client.PrependReactor("create", "tokenreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authenticationv1.TokenReview)
				if review.Spec.Token != "caller-token" {
					t.Fatalf("caller credential not reviewed")
				}
				return true, &authenticationv1.TokenReview{Status: authenticationv1.TokenReviewStatus{Authenticated: true, User: authenticationv1.UserInfo{Username: "reader", UID: "reader-uid", Groups: []string{"power-readers"}}}}, nil
			})
			client.PrependReactor("create", "subjectaccessreviews", func(action ktesting.Action) (bool, runtime.Object, error) {
				review := action.(ktesting.CreateAction).GetObject().(*authorizationv1.SubjectAccessReview)
				if review.Spec.User != "reader" || review.Spec.UID != "reader-uid" || len(review.Spec.Groups) != 1 || review.Spec.Groups[0] != "power-readers" || review.Spec.NonResourceAttributes == nil || review.Spec.NonResourceAttributes.Path != "/chill/api/v1/observations/power" || review.Spec.NonResourceAttributes.Verb != "get" {
					t.Fatalf("incorrect power read authorization: %+v", review.Spec)
				}
				return true, &authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: scenario.name != "denied"}}, nil
			})
			journal, err := serving.NewJournal(serving.JournalConfig{Directory: t.TempDir(), Store: "evidence", Window: time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := journal.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			from := time.Now().UTC().Add(time.Second)
			to := from.Add(time.Second)
			node := serving.NodeReference{Name: "metered", UID: "physical-node"}
			source := metricobserver.SourceIdentity{Kind: "edge-metrics", Endpoint: "http://meter/metrics", Metric: "wall_watts"}
			power := metricobserver.Result{Source: source, StartedAt: from, EndedAt: to, Samples: []metricobserver.Sample{{ObservedAt: from, Watts: 0}, {ObservedAt: to, Watts: 0}}}
			policy := powerwindow.Policy{MaximumGap: time.Second, MinimumCoverage: 1}
			if err := journal.SchedulerRecord(serving.SchedulerObservation{At: to, Node: &node, Meter: "wall", PowerScope: "node-exclusive-wall", Power: &power, PowerCoverage: &policy}); err != nil {
				t.Fatal(err)
			}
			snapshot := serving.SchedulerSnapshot{At: from}
			if err := journal.SchedulerRecord(serving.SchedulerObservation{At: from, Snapshot: &snapshot}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			handler := &Handler{authorizer: &servingkube.Authorizer{Client: client}, config: HandlerConfig{MaxQueryEvents: 10, OperatingPower: func(from, to time.Time, limit int) (serving.OperatingPowerWindow, error) {
				calls++
				return journal.OperatingPower(from, to, []serving.OperatingPowerMeter{{Node: node, Meter: "wall", Scope: "node-exclusive-wall", Source: source}}, limit)
			}}}
			query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
			if scenario.name == "missing-from" {
				query.Del("from")
			}
			if scenario.name == "missing-to" {
				query.Del("to")
			}
			if scenario.name == "reversed" {
				query.Set("to", from.Add(-time.Second).Format(time.RFC3339Nano))
			}
			request := httptest.NewRequest(http.MethodGet, "/api/v1/observations/power?"+query.Encode(), nil)
			if scenario.name != "missing-token" {
				request.Header.Set("Authorization", "Bearer caller-token")
			}
			response := httptest.NewRecorder()
			handler.operatingPower(response, request)
			if response.Code != scenario.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, scenario.status, response.Body)
			}
			if scenario.status != http.StatusOK {
				if calls != 0 {
					t.Fatal("denied or invalid read reached observation owner")
				}
				return
			}
			var got serving.OperatingPowerWindow
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !got.From.Equal(from) || !got.To.Equal(to) || !got.Complete || got.WholePoolJoules == nil || *got.WholePoolJoules != 0 || got.ActivePoolJoules == nil || *got.ActivePoolJoules != 0 {
				t.Fatalf("measured zero changed to missing or different interval: %+v", got)
			}
		})
	}
}
