package httpapi

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
		{"bucket-valid", http.StatusOK}, {"bucket-empty", http.StatusBadRequest},
		{"bucket-invalid", http.StatusBadRequest}, {"bucket-zero", http.StatusBadRequest},
		{"bucket-too-small", http.StatusBadRequest}, {"bucket-too-large", http.StatusBadRequest},
		{"bucket-over-range", http.StatusBadRequest}, {"bucket-over-count", http.StatusBadRequest},
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
			observationLog, err := serving.NewObservationLog(serving.ObservationLogConfig{Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour, Writer: settingsArchive{directory: t.TempDir()}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := observationLog.Close(context.Background()); err != nil {
					t.Error(err)
				}
			})
			from := time.Now().UTC().Add(time.Second)
			to := from.Add(time.Second)
			node := serving.NodeReference{Name: "metered", UID: "physical-node"}
			source := metricobserver.SourceIdentity{Kind: "edge-metrics", Endpoint: "http://meter/metrics", Metric: "wall_watts"}
			power := metricobserver.Result{Source: source, StartedAt: from, EndedAt: to, Samples: []metricobserver.Sample{{ObservedAt: from, Watts: 0}, {ObservedAt: to, Watts: 0}}}
			policy := powerwindow.Policy{MaximumGap: time.Second, MinimumCoverage: 1}
			if err := observationLog.SchedulerRecord(serving.SchedulerObservation{At: to, Node: &node, Meter: "wall", PowerScope: "node-exclusive-wall", Power: &power, PowerCoverage: &policy}); err != nil {
				t.Fatal(err)
			}
			snapshot := serving.SchedulerSnapshot{At: from}
			if err := observationLog.SchedulerRecord(serving.SchedulerObservation{At: from, Snapshot: &snapshot}); err != nil {
				t.Fatal(err)
			}
			calls := 0
			meters := []serving.OperatingPowerMeter{{Node: node, Meter: "wall", Scope: "node-exclusive-wall", Source: source}}
			handler := &Handler{authorizer: &servingkube.Authorizer{Client: client}, config: HandlerConfig{
				MaxQueryEvents: 10,
				OperatingPower: func(ctx context.Context, from, to time.Time, limit int) (serving.OperatingPowerWindow, error) {
					calls++
					return observationLog.OperatingPower(ctx, from, to, meters, limit)
				},
				OperatingPowerSeries: func(ctx context.Context, from, to time.Time, bucket time.Duration, limit int) (serving.OperatingPowerSeries, error) {
					calls++
					return observationLog.OperatingPowerSeries(ctx, from, to, bucket, meters, limit)
				},
			}}
			query := url.Values{"from": {from.Format(time.RFC3339Nano)}, "to": {to.Format(time.RFC3339Nano)}}
			applyPowerQueryScenario(query, scenario.name, from)
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
			if scenario.name == "bucket-valid" {
				assertPowerSeriesResponse(t, response.Body.Bytes(), from, to)
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

func assertPowerSeriesResponse(t *testing.T, raw []byte, from, to time.Time) {
	t.Helper()
	var got serving.OperatingPowerSeries
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !got.From.Equal(from) || !got.To.Equal(to) || got.BucketNanos != time.Second || len(got.Buckets) == 0 || got.Buckets[0].Nodes[0].ObservedSamples == nil || *got.Buckets[0].Nodes[0].ObservedSamples != 1 {
		t.Fatalf("bucket query lost real timestamp/sample contract: %+v", got)
	}
}

func applyPowerQueryScenario(query url.Values, scenario string, from time.Time) {
	switch scenario {
	case "missing-from":
		query.Del("from")
	case "missing-to":
		query.Del("to")
	case "reversed":
		query.Set("to", from.Add(-time.Second).Format(time.RFC3339Nano))
	case "bucket-valid":
		query.Set("bucket", "1s")
	case "bucket-empty":
		query.Set("bucket", "")
	case "bucket-invalid":
		query.Set("bucket", "invalid")
	case "bucket-zero":
		query.Set("bucket", "0s")
	case "bucket-too-small":
		query.Set("bucket", "1ms")
	case "bucket-too-large":
		query.Set("bucket", "2h")
	case "bucket-over-range":
		query.Set("bucket", "1h")
		query.Set("to", from.Add(time.Hour+time.Second).Format(time.RFC3339Nano))
	case "bucket-over-count":
		query.Set("bucket", "1s")
		query.Set("to", from.Add(901*time.Second).Format(time.RFC3339Nano))
	}
}
