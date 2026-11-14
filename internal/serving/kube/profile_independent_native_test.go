package kube

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	native "github.com/lab-paper-code/chill/internal/firstfloor"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"github.com/lab-paper-code/chill/test/fixtures"
	core "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// What the profile session measurement depends on, with the real native
// runtime: the production actor and HTTP handler driving the real IPC runner
// (runtime/firstfloor/runner.py) on ONNX Runtime CPU with a generated Identity
// model. Python 3 with numpy, onnx and onnxruntime is required; missing
// dependencies fail the test. Kubernetes is the operations path's fakes.
//
// Covered: a profile-less fixed-batch measurement instance prepared through
// the operations path reaches native ready, and saturation requests carrying
// the session's level workload identifier execute in batches of B, every one
// after a known idle gap (warm-up covered B). The cmd/chill-server real-native
// test covers folding response batches and emitting the load driver's rows.
// Deriving and publishing a profile and serving from it are not covered here.
func TestSessionMeasurementRealNative(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	command := fixtures.RealFirstfloorCommand(t)
	root, directory := command[2], command[3]
	const batch = 2
	fixture, node, profile, class := observerSourceFixture()
	spec := measurementInstanceSpec(node, profile)
	spec.Config.BatchSizes, spec.Config.MaxBatchSize = []int{batch}, batch
	spec.Config.Policy.FixedBatchSize, spec.Instance.MaxBatch, spec.Instance.FixedBatchSize = batch, batch, batch
	endpoint, client := realNativeServer(t, ctx, spec.Config, root, directory)
	manager, kube, measure := measurementOperations(t, ctx, fixture, node, class, spec, client)

	prepared := measure(servingstate.Operation{ID: "session.1.prepare", Kind: servingstate.OperationPrepare, Node: spec.Instance.Node, Prepare: &spec})
	pod := &core.Pod{}
	if err := manager.store.client.Get(ctx, types.NamespacedName{Namespace: fixture.Namespace, Name: spec.Name}, pod); err != nil {
		t.Fatal(err)
	}
	state := assertMeasurementInstance(t, ctx, kube, pod, prepared, batch)
	if state.Metadata.Runtime.Synthetic || !slices.Equal(state.Metadata.Runtime.InitializedBatches, []int{batch}) {
		t.Fatalf("native runtime did not initialize exactly B=%d: %+v", batch, state.Metadata.Runtime)
	}

	// Saturation keeps one batch executing and one full batch waiting.
	workload := "session-uid/" + strconv.Itoa(batch) + "/saturation"
	const requests = 24
	progress := make([]native.Progress, requests)
	errs := make([]error, requests)
	var wg sync.WaitGroup
	for worker := range 2 * batch {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := worker; i < requests; i += 2 * batch {
				progress[i], errs[i] = realNativeInfer(ctx, endpoint, spec.Config.Model, workload, i)
			}
		}()
	}
	wg.Wait()
	executions := map[uint64]native.Progress{}
	for i, p := range progress {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if p.WorkloadUID != workload || p.BatchSize != batch || p.BatchID == 0 || p.ServiceSample == nil || p.ServiceSample.ServiceDurationNanos <= 0 {
			t.Fatalf("request %d did not complete in a batch of %d of %s: %+v", i, batch, workload, p)
		}
		executions[p.BatchID] = p
	}
	// The first measured execution is not a cold start: warm-up ran B, so
	// every execution, the first included, follows a known idle gap.
	for id, p := range executions {
		if p.ServiceSample.PrecedingIdleNanos == nil || *p.ServiceSample.PrecedingIdleNanos < 0 {
			t.Fatalf("execution %d has no preceding idle gap: %+v", id, p.ServiceSample)
		}
	}
	if len(executions) != requests/batch {
		t.Fatalf("%d requests ran in %d executions, want %d of B=%d", requests, len(executions), requests/batch, batch)
	}

	id := servingstate.PhysicalID(pod.UID, servingstate.FirstfloorStatus(pod).ContainerID)
	removed := measure(servingstate.Operation{ID: "session.2.remove", Kind: servingstate.OperationRemove, Node: spec.Instance.Node, Instance: id})
	if _, drained := removed.Result.DrainNative[id]; !drained || len(removed.Result.Actual.Pods) != 0 {
		t.Fatalf("remove left the measurement instance: %+v", removed.Result)
	}
}

// realNativeServer runs the production native actor and HTTP handler for
// config with the real IPC runner on the generated model, and returns its
// endpoint and a client that routes every address (the instance Service) to it.
func realNativeServer(t *testing.T, ctx context.Context, config native.Config, root, directory string) (string, *http.Client) {
	t.Helper()
	actor := config
	actor.Backend, actor.WarmupIterations = "ort-cpu", 2
	actor.Inputs, actor.Outputs = []string{"image"}, []string{"label"}
	actor.MaxQueueRequests, actor.MaxQueueBytes, actor.MaxRequestBytes = 4*len(config.BatchSizes)*config.MaxBatchSize, 1<<20, 1<<20
	actor.RunnerTimeoutMS = 60_000
	actor.BuildRunnerCommand()
	relocate := map[string]string{"/opt/chill/runtime/firstfloor/runner.py": filepath.Join(root, "runtime/firstfloor/runner.py"),
		"/model": filepath.Join(directory, "model"), "/preprocessor": filepath.Join(directory, "preprocessor")}
	for i, argument := range actor.RunnerCommand {
		if path, ok := relocate[argument]; ok {
			actor.RunnerCommand[i] = path
		}
	}
	if err := native.ValidateConfig(&actor); err != nil {
		t.Fatal(err)
	}
	actorContext, cancel := context.WithCancel(ctx)
	runner, err := native.StartRunner(actorContext, &actor)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	scheduler := native.NewScheduler(actorContext, &actor, runner)
	// Stopping the actor stops and reaps the runner process.
	t.Cleanup(func() {
		cancel()
		<-scheduler.Done()
	})
	server := httptest.NewServer(native.NewHandler(scheduler))
	t.Cleanup(server.Close)
	proxy, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return server.URL, &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxy)}}
}

// realNativeInfer sends one session-load-shaped request and returns the
// native progress of its completed execution.
func realNativeInfer(ctx context.Context, endpoint, model, workload string, index int) (native.Progress, error) {
	id := fmt.Sprintf("%s/%d", workload, index)
	now := time.Now().UTC()
	body, err := json.Marshal(native.Request{ID: id, Context: native.RequestContext{RequestID: id, WorkloadUID: workload,
		Definition: "session", AdmissionPolicy: native.AdmissionDelay, ReceivedAt: now, SLO: native.SLO{Percentile: 95, TargetMS: 60_000}},
		Inputs: []native.Field{{Name: "image", Data: [][]byte{[]byte("1")}}}})
	if err != nil {
		return native.Progress{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/v2/models/"+url.PathEscape(model)+"/infer", bytes.NewReader(body))
	if err != nil {
		return native.Progress{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return native.Progress{}, err
	}
	defer func() { _ = response.Body.Close() }()
	var decoded native.Response
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil || response.StatusCode != http.StatusOK {
		return native.Progress{}, fmt.Errorf("native inference %s: HTTP %d %v", id, response.StatusCode, err)
	}
	return decoded.Progress, nil
}
