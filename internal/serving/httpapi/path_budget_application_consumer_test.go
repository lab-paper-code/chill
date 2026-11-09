package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/application"
	servingnative "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	servingops "github.com/lab-paper-code/chill/internal/serving/operations"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	apps "k8s.io/api/apps/v1"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

// The Kubernetes discovery/list endpoint and native execution are in-process
// doubles. HTTP ingress, Tick/Continue, the production persistence callback,
// capacity calculation, native client protocol and durable request rows are real.
func pathBudgetApplicationController(t *testing.T, f *pathSLOHTTPFixture, settings serving.SchedulerSettings, callback func(serving.SchedulerDecision, serving.SchedulerSnapshot) error) *servingkube.SchedulerController {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var body any
		switch r.URL.Path {
		case "/api":
			body = meta.APIVersions{Versions: []string{"v1"}}
		case "/apis":
			groups := []meta.APIGroup{}
			for _, version := range []meta.GroupVersionForDiscovery{{GroupVersion: edge.GroupVersion.String(), Version: edge.GroupVersion.Version}, {GroupVersion: "apps/v1", Version: "v1"}} {
				name, _, _ := strings.Cut(version.GroupVersion, "/")
				groups = append(groups, meta.APIGroup{Name: name, Versions: []meta.GroupVersionForDiscovery{version}, PreferredVersion: version})
			}
			body = meta.APIGroupList{Groups: groups}
		case "/apis/" + edge.GroupVersion.String():
			body = meta.APIResourceList{GroupVersion: edge.GroupVersion.String(), APIResources: []meta.APIResource{{Name: "deviceclasses", Kind: "DeviceClass", Verbs: meta.Verbs{"list"}}}}
		case "/apis/apps/v1":
			body = meta.APIResourceList{GroupVersion: "apps/v1", APIResources: []meta.APIResource{{Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: meta.Verbs{"list"}}, {Name: "replicasets", Kind: "ReplicaSet", Namespaced: true, Verbs: meta.Verbs{"list"}}}}
		case "/apis/" + edge.GroupVersion.String() + "/deviceclasses":
			body = edge.DeviceClassList{TypeMeta: meta.TypeMeta{APIVersion: edge.GroupVersion.String(), Kind: "DeviceClassList"}, Items: []edge.DeviceClass{}}
		case "/apis/apps/v1/namespaces/serving/deployments":
			body = apps.DeploymentList{TypeMeta: meta.TypeMeta{APIVersion: "apps/v1", Kind: "DeploymentList"}, Items: []apps.Deployment{}}
		case "/apis/apps/v1/namespaces/serving/replicasets":
			body = apps.ReplicaSetList{TypeMeta: meta.TypeMeta{APIVersion: "apps/v1", Kind: "ReplicaSetList"}, Items: []apps.ReplicaSet{}}
		default:
			t.Errorf("unexpected Kubernetes request: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if err := json.NewEncoder(w).Encode(body); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(api.Close)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{core.AddToScheme, apps.AddToScheme, edge.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	store, err := servingkube.NewStore(&rest.Config{Host: api.URL}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node"}})
	cluster := servingkube.NewClusterState(store, kube, "serving")
	client := &servingnative.Client{}
	observer, err := servingkube.NewObserver(cluster, store, kube, f.runtime, client, servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	observer.SetProfiles([]serving.Profile{*f.snapshot.Instances[0].Profile})
	operations, err := servingops.NewOperations(cluster, store, kube, f.runtime, observer, client, servingops.OperationConfig{Namespace: "serving", PollInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	service := application.NewSchedulerService(context.Background(), application.Dependencies{ObservationLog: f.log})
	if callback == nil {
		callback = service.RecordDecision
	}
	controller, err := servingkube.NewSchedulerController(cluster, store, kube, f.runtime, observer, operations, f.log, servingkube.SchedulerControllerConfig{
		Settings: settings, OnDecision: callback,
		Preparations: map[string]servingstate.PrepareSpec{"profile": {Instance: f.snapshot.Instances[0]}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(controller.Close)
	return controller
}

func seedPathBudgetHTTP(t *testing.T, ctx context.Context, f *pathSLOHTTPFixture) {
	t.Helper()
	f.unblock()
	for range 24 {
		if reply := f.invoke(ctx); reply.err != nil {
			t.Fatal(reply.err)
		}
		<-f.entered
	}
	if err := f.log.Drain(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPathBudgetApplicationContinueDoesNotPublishUnappliedObservation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newPathSLOHTTPFixture(t, ctx, 32)
	settings := f.settings
	settings.CompletionMarginMS, settings.AutonomousWorkloads = 29, nil
	controller := pathBudgetApplicationController(t, f, settings, nil)
	seedPathBudgetHTTP(t, ctx, f)
	if _, err := controller.Tick(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := controller.Continue(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Empty Kubernetes inventory cannot supply a real executor. Reinstall the
	// named native HTTP double explicitly; no readiness/ownership claim is made.
	f.install()
	reply := f.invoke(ctx)
	if reply.err != nil {
		t.Fatal(reply.err)
	}
	native := <-f.entered
	row := timingRow(t, f.log, reply.id)
	state := controller.State().Snapshot
	published := serving.ResolvePathBudget(settings, state.PathBudgets, f.snapshot.Workloads[0])
	if native.Context.CompletionMarginMS != 29 || row.CompletionMarginMS == nil || *row.CompletionMarginMS != 29 {
		t.Fatalf("no-active request lost configured POST: native=%+v row=%+v", native.Context, row.CompletionMarginMS)
	}
	if published.PostMS != 29 || published.Source != serving.PathBudgetConfigured {
		t.Fatalf("Continue published unapplied observation as active: public=%+v nativePOST=%g durablePOST=%g", published, native.Context.CompletionMarginMS, *row.CompletionMarginMS)
	}
	if len(state.PathBudgetObservations) == 0 || state.PathBudgetObservations[0].Source != serving.PathBudgetObserved {
		t.Fatalf("Continue lost separately identified observation: %+v", state.PathBudgetObservations)
	}
}

func TestPathBudgetApplicationCompletedTickReachesNativeAndDurableFinal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	f := newPathSLOHTTPFixture(t, ctx, 32)
	settings := f.settings
	settings.CompletionMarginMS, settings.Controls = 29, serving.SchedulerControls{}
	controller := pathBudgetApplicationController(t, f, settings, nil)
	seedPathBudgetHTTP(t, ctx, f)
	resume := make(chan struct{})
	resumeOld := sync.OnceFunc(func() { close(resume) })
	defer resumeOld()
	f.holdNext <- resume
	oldReply := make(chan pathSLOHTTPReply, 1)
	go func() { oldReply <- f.invoke(ctx) }()
	select {
	case request := <-f.entered:
		if request.Context.CompletionMarginMS != 29 {
			t.Fatalf("old acceptance lost configured POST: %+v", request.Context)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	var budget serving.PathBudget
	for {
		if _, err := controller.Tick(ctx, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		budget = serving.ResolvePathBudget(settings, controller.State().Snapshot.PathBudgets, f.snapshot.Workloads[0])
		if budget.Source == serving.PathBudgetObserved {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	resumeOld()
	old := <-oldReply
	if old.err != nil {
		t.Fatal(old.err)
	}
	oldRow := timingRow(t, f.log, old.id)
	if oldRow.CompletionMarginMS == nil || *oldRow.CompletionMarginMS != 29 {
		t.Fatalf("completed Tick rewrote old acceptance: %+v", oldRow.CompletionMarginMS)
	}
	// The empty Kubernetes inventory invalidated the named native double.
	// Reinstall it after the old call drains: the runtime correctly refuses
	// profile replacement while that synthetic executor is still outstanding.
	f.install()
	reply := f.invoke(ctx)
	if reply.err != nil {
		t.Fatal(reply.err)
	}
	request := <-f.entered
	row := timingRow(t, f.log, reply.id)
	if budget.DefinitionID != f.snapshot.Workloads[0].Identity() || request.Context.CompletionMarginMS != budget.PostMS || row.CompletionMarginMS == nil || *row.CompletionMarginMS != budget.PostMS {
		t.Fatalf("completed Tick budget did not reach native/durable consumer: budget=%+v native=%+v row=%+v", budget, request.Context, row.CompletionMarginMS)
	}
	requirePathSLOPersistedPaths(t, []serving.RequestRow{row}, []native.Request{request}, budget, budget)
}

func TestPathBudgetApplicationRecordFailureKeepsCommittedNumericalState(t *testing.T) {
	for _, mode := range []string{"failure", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			tickCtx, cancelTick := context.WithCancel(ctx)
			defer cancelTick()
			f := newPathSLOHTTPFixture(t, ctx, 32)
			settings := f.settings
			settings.CompletionMarginMS, settings.Controls = 29, serving.SchedulerControls{}
			service := application.NewSchedulerService(ctx, application.Dependencies{ObservationLog: f.log})
			committed := false
			controller := pathBudgetApplicationController(t, f, settings, func(decision serving.SchedulerDecision, snapshot serving.SchedulerSnapshot) error {
				budget := serving.ResolvePathBudget(settings, decision.PathBudgets, f.snapshot.Workloads[0])
				if budget.Source != serving.PathBudgetObserved {
					return service.RecordDecision(decision, snapshot)
				}
				committed = true
				if mode == "failure" {
					return errors.New("decision persistence failed")
				}
				if err := service.RecordDecision(decision, snapshot); err != nil {
					return err
				}
				cancelTick()
				return nil
			})
			seedPathBudgetHTTP(t, ctx, f)
			for {
				_, err := controller.Tick(tickCtx, time.Now().UTC())
				if committed {
					if err == nil {
						t.Fatal("failed/canceled decision reported physical-action authorization")
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case <-time.After(time.Millisecond):
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			state := controller.State()
			budget := serving.ResolvePathBudget(settings, state.Snapshot.PathBudgets, f.snapshot.Workloads[0])
			if budget.Source != serving.PathBudgetObserved || len(state.Actions) != 0 {
				t.Fatalf("numerical commit lost or failed persistence launched physical work: %+v", state)
			}
			f.install()
			reply := f.invoke(ctx)
			if reply.err != nil {
				t.Fatal(reply.err)
			}
			request := <-f.entered
			row := timingRow(t, f.log, reply.id)
			if request.Context.CompletionMarginMS != budget.PostMS || row.CompletionMarginMS == nil || *row.CompletionMarginMS != budget.PostMS {
				t.Fatalf("public budget claims unapplied POST after %s: budget=%+v native=%+v durable=%+v", mode, budget, request.Context, row.CompletionMarginMS)
			}
			// Cancellation before the commit boundary cannot replace this pair.
			before := state.Snapshot.At
			canceled, stop := context.WithCancel(ctx)
			stop()
			if _, err := controller.Tick(canceled, time.Now().UTC()); !errors.Is(err, context.Canceled) || !controller.State().Snapshot.At.Equal(before) {
				t.Fatalf("pre-commit cancellation changed applied state: %v", err)
			}
		})
	}
}
