package kube

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

func TestTickFeedbackUpdatesNewCallsWithoutRewritingAcceptedNativeContext(t *testing.T) {
	c, _ := tickReadySubsetController(t, nil)
	c.config.Settings.AutonomousWorkloads = nil
	c.config.Settings.CompletionMarginMS = 7
	c.runtime.SetCompletionMargin(7)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := c.Tick(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := c.State().Snapshot
	workload := snapshot.Workloads[0]
	var ready serving.SchedulerInstance
	for _, instance := range snapshot.Instances {
		if instance.Ready {
			ready = instance
			break
		}
	}
	if ready.Profile == nil || ready.Capacity <= 0 {
		t.Fatalf("fixture has no usable native capacity: %+v", ready)
	}
	seen, release := interceptNativeFeedbackCalls(t, c, ctx, ready)
	var calls []*serving.Call
	var receipts []serving.Receipt
	want := map[string]float64{}
	accept := func(margin float64) {
		t.Helper()
		receipt, err := c.runtime.Receive(workload.Name, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		call, err := c.runtime.Accept(&receipt, managerSample(t))
		if err != nil {
			t.Fatal(err)
		}
		calls, receipts = append(calls, call), append(receipts, receipt)
		want[receipt.ID] = margin
	}
	actual := map[string]native.Request{}
	for range ready.Capacity {
		accept(first.PathFeedback[0].PostNanos / float64(time.Millisecond))
		select {
		case request := <-seen:
			actual[request.ID] = request
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	accept(7)
	old := receipts[len(receipts)-1].ID
	for _, request := range c.runtime.SchedulerInputs(time.Now).Outstanding {
		if request.ID == old && request.Phase != "pending" {
			t.Fatal("old accepted request did not remain in the ingress queue")
		}
	}
	recordSchedulerPathSample(t, c, workload)
	config := c.config
	config.Settings.CompletionMarginMS = 19
	config.OnDecision = func(decision serving.SchedulerDecision, snapshot serving.SchedulerSnapshot) error {
		if len(decision.PathFeedback) != 1 || decision.PathFeedback[0].Source != "empirical" ||
			decision.PathFeedback[0] != snapshot.PathFeedback[0] || decision.PathFeedback[0].PostNanos != float64(30*time.Millisecond) {
			t.Fatalf("callback did not receive actual captured model: %+v", decision.PathFeedback)
		}
		// The normal settings writer remains independent of Tick's derived model.
		c.runtime.SetCompletionMargin(19)
		accept(decision.PathFeedback[0].PostNanos / float64(time.Millisecond))
		return nil
	}
	if err := c.SetConfiguration(config); err != nil {
		t.Fatal(err)
	}
	second, err := c.Tick(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second.Settings.CompletionMarginMS != 19 || c.Settings().CompletionMarginMS != 19 {
		t.Fatal("derived publication overwrote configured settings")
	}
	if len(calls) != ready.Capacity+2 {
		t.Fatal("committed decision callback did not accept a new request")
	}
	release()
	for index, call := range calls {
		if _, err := call.Wait(ctx); err != nil {
			t.Fatal(err)
		}
		if err := c.runtime.Response(receipts[index], "completed", ""); err != nil {
			t.Fatal(err)
		}
	}
	for len(actual) < len(calls) {
		select {
		case request := <-seen:
			actual[request.ID] = request
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if err := c.observationLog.Drain(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := c.observationLog.Query(ctx, workload.UID, time.Time{}, time.Time{}, nil, nil, 100, "")
	if err != nil {
		t.Fatal(err)
	}
	verifyFrozenNativeFeedback(t, rows.Requests, actual, want, len(calls))
}

func verifyFrozenNativeFeedback(t *testing.T, rows []serving.RequestRow, actual map[string]native.Request, want map[string]float64, count int) {
	t.Helper()
	finals := 0
	for _, row := range rows {
		margin, selected := want[row.RequestID]
		if !selected {
			continue
		}
		finals++
		request := actual[row.RequestID]
		sender, ok := row.ServerTiming.Elapsed(serving.ServerSenderElapsedFrozen)
		if row.Kind != serving.RequestRowFinal || row.CompletionMarginMS == nil || *row.CompletionMarginMS != margin ||
			request.Context.CompletionMarginMS != margin || !ok || request.Context.SenderElapsedMS != float64(sender)/float64(time.Millisecond) {
			t.Fatalf("decision, accepted final row and native arguments diverged: row=%+v native=%+v want=%v", row, request.Context, margin)
		}
	}
	if finals != count {
		t.Fatalf("got %d final rows for %d accepted calls", finals, count)
	}
}

func interceptNativeFeedbackCalls(t *testing.T, c *SchedulerController, ctx context.Context, ready serving.SchedulerInstance) (<-chan native.Request, context.CancelFunc) {
	t.Helper()
	releaseContext, release := context.WithCancel(ctx)
	seen := make(chan native.Request, ready.Capacity+2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request native.Request
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		seen <- request
		<-releaseContext.Done()
		progress := native.Progress{RequestID: request.ID, WorkloadUID: request.Context.WorkloadUID,
			Definition: request.Context.Definition, Phase: "finished", At: time.Now(), BatchID: 1,
			BatchSize: 1, ProfileID: ready.Profile.ID, ExecutionComplete: true, Outcome: "completed", NativeElapsedMS: 1}
		if err := json.NewEncoder(w).Encode(native.Response{ModelName: ready.Profile.Condition.Actor.Model,
			ID: request.ID, ExecutionComplete: true, Progress: progress,
			Outputs: []native.Field{{Name: "output", Data: [][]byte{[]byte("result")}}}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(func() {
		release()
		server.Close()
	})
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	previous := c.observer.native.HTTPClient.Transport.(*http.Transport).Proxy
	c.observer.native.HTTPClient.Transport = &http.Transport{Proxy: func(r *http.Request) (*url.URL, error) {
		if r.Method == http.MethodPost {
			return proxy, nil
		}
		return previous(r)
	}}
	return seen, release
}
