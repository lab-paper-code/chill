package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	kubetesting "k8s.io/client-go/testing"
)

func policySettingsInput(t *testing.T, settings serving.SchedulerSettings) SettingsInput {
	t.Helper()
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	var input SettingsInput
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func policySettingsFixture(t *testing.T) *preparationCatalogFixture {
	t.Helper()
	fixture := newPreparationCatalogFixture(t)
	service := fixture.service
	service.settingsKube = service.kube
	kube := service.kube.(*kubefake.Clientset)
	// The fake tracker does not implement Kubernetes optimistic versions.
	kube.PrependReactor("update", "configmaps", func(action kubetesting.Action) (bool, runtime.Object, error) {
		cm := action.(kubetesting.UpdateAction).GetObject().(*core.ConfigMap)
		stored, err := kube.Tracker().Get(core.SchemeGroupVersion.WithResource("configmaps"), cm.Namespace, cm.Name)
		if err != nil {
			return true, nil, err
		}
		if cm.ResourceVersion != stored.(*core.ConfigMap).ResourceVersion {
			return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, cm.Name, errors.New("concurrent settings writer"))
		}
		version, err := strconv.Atoi(cm.ResourceVersion)
		if err != nil {
			return true, nil, err
		}
		cm.ResourceVersion = strconv.Itoa(version + 1)
		return false, nil, nil
	})
	return fixture
}

func policyBundleServer(t *testing.T, service *SchedulerService, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	objects, err := stores3.New(stores3.Config{LogicalStore: "evidence", Bucket: "bucket", Endpoint: server.URL,
		Region: "region", AccessKeyID: "access", SecretAccessKey: "secret", TempDir: t.TempDir(), HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	service.objects = objects
	service.opts.StoreEndpoint = server.URL
}

func addPolicyCatalogSibling(t *testing.T, fixture *preparationCatalogFixture) string {
	t.Helper()
	resources, objects := preparationCatalogEvidence(t, "-new")
	path := "/apis/" + edge.GroupVersion.String() + "/namespaces/serving/batchenergyoptimizations"
	before := fixture.resources[path].(edge.BatchEnergyOptimizationList)
	after := resources[path].(edge.BatchEnergyOptimizationList)
	after.Items = append(before.Items, after.Items...)
	resources[path] = after
	for key, value := range resources {
		fixture.resources[key] = value
	}
	for key, value := range objects {
		fixture.evidence[key] = value
	}
	return "serving/catalog-campaign-new/campaign-uid-new"
}

func persistPolicyFixture(t *testing.T, service *SchedulerService, config SchedulerConfiguration) string {
	t.Helper()
	cm, err := service.kube.CoreV1().ConfigMaps("serving").Get(context.Background(), "scheduler", meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	cm.Data["scheduler.json"] = string(raw)
	cm, err = service.kube.CoreV1().ConfigMaps("serving").Update(context.Background(), cm, meta.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return cm.ResourceVersion
}

func TestSettingsApplyWhileNewProfileUploadHeld(t *testing.T) {
	fixture := policySettingsFixture(t)
	service := fixture.service
	installPolicyControlSources(t, service)
	entered, released := make(chan struct{}), make(chan struct{})
	var enterOnce, releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	policyBundleServer(t, service, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.Contains(r.URL.Path, "campaign-uid-new") {
			enterOnce.Do(func() { close(entered) })
			select {
			case <-released:
			case <-r.Context().Done():
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	t.Cleanup(release)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	accepted := service.Profiles.Load()
	config := *service.configuration.Load()
	oldSpec := accepted.Preparations[fixture.profile.ID]
	newID := addPolicyCatalogSibling(t, fixture)
	// This desired configuration has never been accepted. PUT must not adopt
	// its private fields or confuse its queue policy with the accepted spec.
	desired := config
	desired.Settings.ServingMemoryBytes = 16 << 20
	desired.Reserved = map[string]serving.SchedulerAllocation{"unaccepted": {MemoryBytes: 999}}
	desired.StaticRouting = map[string]map[string]int{"unaccepted": {"physical": 1}}
	version := persistPolicyFixture(t, service, desired)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- service.Refresh(ctx) }()
	t.Cleanup(func() { cancel(); release(); <-done })
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("background refresh did not reach new-profile upload")
	}
	var powerStopped atomic.Bool
	service.powerCancel = func() { powerStopped.Store(true) }
	settings := config.Settings
	settings.PlanSearchStrategy = "v2"
	settings.ServingMemoryBytes = 64 << 20
	settings.CompletionMarginMS = 123
	settings.DefaultDispatchOccupancyNanos = 7654321
	putCtx, putCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer putCancel()
	change, newVersion, err := service.ChangeSchedulerSettings(putCtx, policySettingsInput(t, settings), version, true, "operator", "uid")
	if err != nil || change.AppliedAt == nil || newVersion == version {
		t.Fatalf("held source prevented synchronous policy acceptance: %+v version=%s err=%v", change, newVersion, err)
	}
	select {
	case <-released:
		t.Fatal("PUT released the independent source operation")
	default:
	}
	storedConfig := assertPolicySettingsConsumers(t, service, settings)
	published := service.Profiles.Load()
	if !reflect.DeepEqual(published.CatalogView, accepted.CatalogView) || len(published.Preparations) != 1 ||
		published.ConfigVersion != accepted.ConfigVersion || service.resourceVersion != accepted.ConfigVersion ||
		published.Preparations[fixture.profile.ID].Bundle != oldSpec.Bundle ||
		published.Preparations[fixture.profile.ID].Config.MaxQueueBytes != 32<<20 {
		t.Fatal("policy acceptance replaced source inputs or retained a stale queue cap")
	}
	if powerStopped.Load() || !reflect.DeepEqual(service.configuration.Load().Reserved, config.Reserved) ||
		!reflect.DeepEqual(service.configuration.Load().StaticRouting, config.StaticRouting) ||
		!reflect.DeepEqual(storedConfig.Reserved, desired.Reserved) {
		t.Fatal("policy acceptance changed private fields, meters or stored declarations")
	}
	release()
	assertStalePolicyRefreshRejected(t, done)
	if service.Profiles.Load() != published || !reflect.DeepEqual(service.Controller.Settings(), settings) {
		t.Fatal("stale refresh restored old settings or accepted incomplete source state")
	}
	service.powerCancel = nil
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh := service.Profiles.Load()
	if fresh.ConfigVersion != newVersion || fresh.Preparations[newID].Bundle.Size == 0 ||
		fresh.Preparations[newID].Config.MaxQueueBytes != 32<<20 || !reflect.DeepEqual(service.Controller.Settings(), settings) {
		t.Fatal("fresh refresh did not accept new policy and complete catalog together")
	}
}

func assertStalePolicyRefreshRejected(t *testing.T, done chan error) {
	t.Helper()
	select {
	case err := <-done:
		done <- err // retain completion for cleanup
		if err == nil {
			t.Fatal("older in-flight refresh published over the PUT")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("released refresh did not complete")
	}
}

func assertPolicySettingsConsumers(t *testing.T, service *SchedulerService, settings serving.SchedulerSettings) SchedulerConfiguration {
	t.Helper()
	stored, _, err := SchedulerConfigMap(context.Background(), service.kube, "serving", "scheduler", "scheduler.json")
	if err != nil {
		t.Fatal(err)
	}
	storedConfig, err := DecodeSchedulerConfiguration(stored.Data["scheduler.json"], 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	actual, ok := service.AcceptedSettings()
	if !ok || !reflect.DeepEqual(actual, settings) || !reflect.DeepEqual(service.Controller.Settings(), settings) ||
		!reflect.DeepEqual(storedConfig.Settings, settings) {
		t.Fatal("stored, controller and atomic settings diverged")
	}
	for _, value := range service.occupancy.selected() {
		if value.PerRequest != time.Duration(settings.DefaultDispatchOccupancyNanos) {
			t.Fatalf("occupancy retained old policy fallback: %+v", value)
		}
	}
	assertPolicyControlUsesSettings(t, service, settings)
	return storedConfig
}

func TestPreparationReusesAcceptedBundleButChecksCurrentBoundaries(t *testing.T) {
	fixture := policySettingsFixture(t)
	service := fixture.service
	var acceptedUploaded atomic.Bool
	var qualifiedUploaded atomic.Bool
	policyBundleServer(t, service, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if strings.Contains(r.URL.Path, "reuse-changed-policy") {
			qualifiedUploaded.Store(true)
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "campaign-uid-new") {
			http.Error(w, "new profile unavailable", http.StatusBadRequest)
			return
		}
		if acceptedUploaded.Swap(true) {
			t.Error("accepted immutable bundle uploaded again")
		}
		w.WriteHeader(http.StatusOK)
	})
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	accepted := service.Profiles.Load()
	config := *service.configuration.Load()
	config.Settings.ServingMemoryBytes = 64 << 20
	persistPolicyFixture(t, service, config)
	if err := service.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if spec := service.Profiles.Load().Preparations[fixture.profile.ID]; spec.Bundle != accepted.Preparations[fixture.profile.ID].Bundle || spec.Config.MaxQueueBytes != 32<<20 {
		t.Fatal("settings RV change lost bundle reuse or queue derivation")
	}
	newID := addPolicyCatalogSibling(t, fixture)
	accepted = service.Profiles.Load()
	for range 2 {
		if err := service.Refresh(context.Background()); err == nil {
			t.Fatal("failed first upload accepted a partial catalog")
		}
		if service.Profiles.Load() != accepted {
			t.Fatal("failed first upload discarded the accepted catalog")
		}
	}
	// A newly qualified identity is never allowed to borrow the old bundle.
	if _, exists := accepted.Preparations[newID]; exists {
		t.Fatal("new profile became prepared without a successful upload")
	}
	fixture.fail("node", true)
	if _, err := service.preparation(context.Background(), config, fixture.profile); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("bundle reuse bypassed fresh Node read", err)
	}
	fixture.fail("node", false)
	node, err := service.kube.CoreV1().Nodes().Get(context.Background(), fixture.profile.NodeName, meta.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	node.UID = types.UID("replacement-node")
	if _, err := service.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.preparation(context.Background(), config, fixture.profile); err == nil {
		t.Fatal("bundle reuse accepted a replacement Node UID")
	}
	node.UID = types.UID(fixture.profile.Condition.NodeUID)
	if _, err := service.kube.CoreV1().Nodes().Update(context.Background(), node, meta.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	fixture.fail("declaration", true)
	if spec, err := service.preparation(context.Background(), config, fixture.profile); err != nil || spec.Config.RuntimeImage != fixture.profile.Condition.RuntimeImage {
		t.Fatal("bundle reuse depended on current runtime declaration", err)
	}
	fixture.fail("declaration", false)
	current := &core.ConfigMap{}
	if err := fixture.declarations.Get(context.Background(), types.NamespacedName{Namespace: "serving", Name: "execution-facts"}, current); err != nil {
		t.Fatal(err)
	}
	current.Data["execution-facts.yaml"] = strings.ReplaceAll(current.Data["execution-facts.yaml"], "registry/runtime:current", "registry/runtime:replacement")
	if err := fixture.declarations.Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	spec, err := service.preparation(context.Background(), config, fixture.profile)
	if err != nil || spec.Config.RuntimeImage != fixture.profile.Condition.RuntimeImage || spec.Bundle != accepted.Preparations[fixture.profile.ID].Bundle {
		t.Fatal("bundle reuse lost measured runtime image", err)
	}
	qualified := fixture.profile
	qualified.ID += "/reuse-changed-policy"
	spec, err = service.preparation(context.Background(), config, qualified)
	if err != nil || !qualifiedUploaded.Load() || spec.Bundle.Key == accepted.Preparations[fixture.profile.ID].Bundle.Key {
		t.Fatal("new policy-qualified profile borrowed an accepted bundle", err)
	}
}

func TestSettingsExpiryAndConflictDoNotApplyLatePolicy(t *testing.T) {
	for _, boundary := range []string{"expired before lock", "expired after read", "stale ETag", "update conflict", "expired after persistence", "unusable queue"} {
		t.Run(boundary, func(t *testing.T) {
			fixture := policySettingsFixture(t)
			service := fixture.service
			if err := service.Refresh(context.Background()); err != nil {
				t.Fatal(err)
			}
			accepted := service.Profiles.Load()
			before, _ := service.AcceptedSettings()
			after := before
			after.PlanSearchStrategy = "v2"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			version := service.resourceVersion
			kube := service.kube.(*kubefake.Clientset)
			switch boundary {
			case "expired before lock":
				service.Lock.Lock()
				cancel()
				service.Lock.Unlock()
			case "expired after read":
				kube.PrependReactor("get", "configmaps", func(kubetesting.Action) (bool, runtime.Object, error) {
					cancel()
					return false, nil, nil
				})
			case "stale ETag":
				version = "stale"
			case "update conflict":
				kube.PrependReactor("update", "configmaps", func(kubetesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewConflict(schema.GroupResource{Resource: "configmaps"}, "scheduler", errors.New("concurrent writer"))
				})
			case "expired after persistence":
				kube.PrependReactor("update", "configmaps", func(kubetesting.Action) (bool, runtime.Object, error) {
					cancel()
					return false, nil, nil
				})
			case "unusable queue":
				after.ServingMemoryBytes = 1
			}
			change, _, err := service.ChangeSchedulerSettings(ctx, policySettingsInput(t, after), version, true, "operator", "uid")
			if err == nil || change.AppliedAt != nil || !reflect.DeepEqual(service.Controller.Settings(), before) || service.Profiles.Load() != accepted {
				t.Fatal("expired/conflicting policy changed accepted state", err)
			}
			cm, _, err := SchedulerConfigMap(context.Background(), kube, "serving", "scheduler", "scheduler.json")
			if err != nil {
				t.Fatal(err)
			}
			stored, err := DecodeSchedulerConfiguration(cm.Data["scheduler.json"], 1<<20)
			if err != nil {
				t.Fatal(err)
			}
			want := before
			if boundary == "expired after persistence" {
				want = after
				rows, _, _, _, err := service.observationLog.SchedulerObservationPage(context.Background(), time.Now().Add(-time.Minute), time.Now().Add(time.Second), 10, 0, observation.SchedulerObservationSelection{})
				if err != nil || len(rows) != 2 || rows[1].Settings.AppliedAt != nil || rows[1].Settings.Error == "" {
					t.Fatal("stored-but-unapplied outcome lost audit evidence", err)
				}
			}
			if !reflect.DeepEqual(stored.Settings, want) {
				t.Fatal("rejected policy performed an unexpected ConfigMap write")
			}
		})
	}
}
