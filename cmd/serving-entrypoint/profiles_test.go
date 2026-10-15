package main

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	native "github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/modelcompat"
	derivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
	sdk "github.com/lab-paper-code/chill/internal/serving/firstfloor"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func measuredAdoptionProfile() serving.Profile {
	return serving.Profile{
		ID: "measurements/complete/uid", NodeName: "node",
		Condition: derivation.CurvePlanSpec{
			NodeUID: "node-uid", Backend: "ort-cuda", RuntimeImage: "registry/cuda@sha256:measured",
			Artifact:   derivation.CurveArtifactContract{Path: "/artifact/model.onnx"},
			Firstfloor: &modelcompat.FirstfloorSettings{MaxBatchSize: 4, MaxQueueSize: 8},
		},
		RuntimeMetadata: &native.RuntimeMetadata{
			Runtime: "ort-cuda", ModelPath: "/model/model.onnx",
			Inputs:  []native.InputSpec{{Name: "input", Datatype: "FP32", Shape: []int{1}}},
			GPUUUID: "GPU-measured", CPUAffinity: []int{3}, IntraOpThreads: 1, InterOpThreads: 1,
			WarmupIterations: 5, InitializedBatches: []int{1, 2, 4},
		},
		Batches: []serving.BatchProfile{
			{Batch: 1, MemoryBytes: 2048, DurationNanos: []float64{2e6, 1e6}},
			{Batch: 2, MemoryBytes: 4096, DurationNanos: []float64{3e6}},
		},
	}
}
func TestMeasuredAdoptionRequiresExplicitUnreservedPhysicalInventory(t *testing.T) {
	owner := &schedulerOwner{}
	profile := measuredAdoptionProfile()
	config := schedulerConfiguration{CPUInventory: map[string][]int{"node": {0, 1, 2, 3}}}
	before := append([]int(nil), config.CPUInventory["node"]...)
	if err := owner.checkProfileInventory(config, profile); err == nil || !strings.Contains(err.Error(), "GPU-measured") {
		t.Fatalf("missing GPU inventory: %v", err)
	}
	if config.GPUInventory != nil || !reflect.DeepEqual(before, config.CPUInventory["node"]) {
		t.Fatal("inventory was silently widened")
	}
	config.GPUInventory = map[string][]string{"node": {"GPU-measured"}}
	if err := owner.checkProfileInventory(config, profile); err != nil {
		t.Fatal(err)
	}
	config.Reserved = map[string]serving.SchedulerAllocation{"node": {CPUs: []int{3}}}
	if err := owner.checkProfileInventory(config, profile); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved CPU admitted: %v", err)
	}
	config.Reserved["node"] = serving.SchedulerAllocation{GPUUUID: "GPU-measured"}
	if err := owner.checkProfileInventory(config, profile); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("reserved GPU admitted: %v", err)
	}
}
func TestMeasuredPreparationKeepsSparseInitializationAndNoReadinessClaims(t *testing.T) {
	owner := &schedulerOwner{
		kube: fake.NewSimpleClientset(&core.Node{ObjectMeta: meta.ObjectMeta{
			Name: "node", UID: "node-uid", Labels: map[string]string{servingkube.ServingPoolLabel: "pool"},
		}}),
		opts: options{
			storeName: "store", storeEndpoint: "http://store", storeBucket: "bucket", storeRegion: "region",
			storeCredentialsSecret: "actual-secret", requestTimeout: time.Second,
			maxEvidenceBytes: 64 << 20, maxRequestBytes: 8 << 20,
		},
	}
	profile := measuredAdoptionProfile()
	spec, err := owner.measuredPreparation(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Instance.Ready || spec.Instance.CompletionObserved || spec.Instance.Profile != nil ||
		spec.Bundle.Size != 0 || spec.Config.Policy.Batches != nil {
		t.Fatal("selection fabricated readiness/evidence/publication")
	}
	if !reflect.DeepEqual(spec.Config.BatchSizes, []int{1, 2, 4}) || spec.Config.Policy.MaxBatchSize != 2 ||
		spec.MemoryBytes != 4096 || spec.Store.CredentialsSecretName != "actual-secret" {
		t.Fatalf("lost measured startup/delivery facts: %+v", spec)
	}
	if !reflect.DeepEqual(spec.Instance.Allocation.CPUs, []int{3}) ||
		spec.Instance.Allocation.GPUUUID != "GPU-measured" ||
		!strings.Contains(strings.Join(spec.Config.RunnerCommand, " "), "--runtime ort-cuda") {
		t.Fatal("measured GPU/runtime allocation changed")
	}
	bundle := measuredProfileBundle(profile)
	if !reflect.DeepEqual(bundle.Batches[0].ServiceSamplesMS, []float64{1, 2}) ||
		profile.Batches[0].DurationNanos[0] != 2e6 {
		t.Fatal("shared bundle producer changed raw distribution")
	}
	profile.Batches[1].MemoryBytes = 0
	if _, err = owner.measuredPreparation(context.Background(), profile); err == nil {
		t.Fatal("missing measured memory accepted")
	}
}
func TestProfileSelectionReportsPendingRejectedAndSuperseded(t *testing.T) {
	selection := entrypoint.ProfileCampaign{Namespace: "measurements", Name: "complete"}
	for _, code := range []string{"MeasurementPending", "MeasurementRejected", "InvalidEvidence"} {
		view := servingkube.CatalogView{Insufficiencies: []servingkube.CatalogIssue{{
			Source: types.NamespacedName{Namespace: selection.Namespace, Name: selection.Name},
			Code:   code, Reason: "actual measurement failure",
		}}}
		result := profileSelection(selection, view)
		if result.Code != code || result.Reason != "actual measurement failure" || result.Persisted || result.Loaded {
			t.Fatalf("misreported rejected input: %+v", result)
		}
		if (result.Status == "Pending") != (code == "MeasurementPending") {
			t.Fatalf("wrong status: %+v", result)
		}
	}
	result := profileSelection(selection, servingkube.CatalogView{
		Profiles: []serving.Profile{{ID: "measurements/complete-new/uid"}},
	})
	if result.ProfileID != "" || result.Code != "ProfileUnavailable" {
		t.Fatal("campaign name prefix aliased another measurement")
	}
}

func TestGeneratedPreparationRejectsUnrepresentableMeasuredRuntimeAccess(t *testing.T) {
	for _, access := range []derivation.CurveRuntimeAccessContract{
		{Privileged: true, SupplementalGroups: []int64{44}},
		{SupplementalGroups: []int64{44}},
		{Resources: map[string]string{"other.device/accelerator": "1"}},
	} {
		profile := measuredAdoptionProfile()
		profile.Condition.RuntimeAccess = &access
		// No clients: reject before any external lookup or configuration write.
		spec, err := (&schedulerOwner{}).measuredPreparation(context.Background(), profile)
		if err == nil || !strings.Contains(err.Error(), "cannot reproduce measured runtimeAccess") {
			t.Fatalf("measured delivery access silently dropped: spec=%+v err=%v", spec, err)
		}
		if spec.Name != "" {
			t.Fatal("unrepresentable delivery produced a preparation")
		}
	}
}

func TestFailedRefreshPreservesAcceptedProfilesAndRouting(t *testing.T) {
	const acceptedVersion = "accepted-config"
	ctx := context.Background()
	journal, err := serving.NewJournal(serving.JournalConfig{
		Directory: t.TempDir(), Store: "evidence", Window: time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	execution, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 1, Journal: journal})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		if err := execution.Shutdown(shutdownCtx); err != nil {
			t.Error(err)
		}
	})
	scheme := runtime.NewScheme()
	if err := core.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	store, err := servingkube.NewStore(&rest.Config{Host: "http://127.0.0.1:1"}, scheme)
	if err != nil {
		t.Fatal(err)
	}
	settings := serving.SchedulerSettings{
		ObservationWindow: time.Second, ControlInterval: time.Second, Horizon: time.Second,
		MaxObservationAge: time.Second, Replays: 1, MaxCandidates: 1, MaxReplicas: 1,
		MaxInstances: 1, MaxReplayRequests: 1, MaxCPUs: 2,
	}
	rejected := schedulerConfiguration{
		Settings: settings, CostReferences: []evidencestore.ObjectIdentity{{}},
	}
	raw, err := json.Marshal(rejected)
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewSimpleClientset(&core.ConfigMap{
		ObjectMeta: meta.ObjectMeta{Namespace: "serving", Name: "scheduler", ResourceVersion: "new"},
		Data:       map[string]string{"scheduler.json": string(raw)},
	})
	observer, err := servingkube.NewObserver(store, kube, execution, &sdk.Client{},
		servingkube.ObserverConfig{Namespace: "serving"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := observer.Close(); err != nil {
			t.Error(err)
		}
	})
	objects := &stores3.Store{}
	catalog, err := servingkube.NewCatalog(store, objects, 1024, 0.95)
	if err != nil {
		t.Fatal(err)
	}
	profile := measuredAdoptionProfile()
	profile.Condition.LogicalModel = "model"
	observer.SetProfiles([]serving.Profile{profile})
	definition := &serving.Definition{
		Name: "workload", UID: "workload-uid", Generation: 1,
		SLO: serving.SLO{Percentile: 99, TargetLatency: time.Second},
		Model: serving.Model{Name: "model", UID: "model-uid", Generation: 1, Paths: []serving.ModelPath{{
			Path: profile.Condition.Artifact.Path, Backend: profile.Condition.Backend,
			Firstfloor: profile.Condition.Firstfloor,
		}}},
	}
	if err := execution.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := execution.InstallInstances([]serving.ReadyInstance{{
		State: serving.InstanceState{
			ID: "physical", Pool: "pool", Node: serving.NodeReference{Name: "node", UID: "uid"},
			Profile: profile.ID, Ready: true,
		},
		Profile: &profile, Weight: 1, Capacity: 1,
		Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
			t.Error("refresh must not dispatch inference")
			return serving.BackendResult{}, nil
		},
	}}); err != nil {
		t.Fatal(err)
	}
	execution.EnableSchedulerRouting()
	before, err := execution.State(definition.Name)
	if err != nil || !before.Admission || before.Reason != "Ready" {
		t.Fatalf("baseline route: %+v %v", before, err)
	}
	accepted := &schedulerConfiguration{
		Settings: settings, StaticRouting: map[string]map[string]int{definition.UID: {"physical": 1}},
	}
	owner := &schedulerOwner{
		kube: kube, observer: observer, objects: objects, catalog: catalog, resourceVersion: acceptedVersion,
		profiles: profile.ID, staticAppliedRV: acceptedVersion,
		opts: options{servingNamespace: "serving", schedulerConfigMap: "scheduler",
			schedulerConfigKey: "scheduler.json", maxRequestBytes: 1 << 20, maxEvidenceBytes: 1024},
	}
	owner.configuration.Store(accepted)
	if err := owner.refresh(ctx); err == nil {
		t.Fatal("invalid cost reference was accepted")
	}
	after, err := execution.State(definition.Name)
	if err != nil || !after.Admission || after.Reason != before.Reason {
		t.Fatalf("failed refresh changed actual runtime admission/routing: before=%+v after=%+v err=%v",
			before, after, err)
	}
	if !reflect.DeepEqual(observer.Profiles(), []serving.Profile{profile}) ||
		owner.configuration.Load() != accepted ||
		owner.resourceVersion != acceptedVersion || owner.staticAppliedRV != acceptedVersion {
		t.Fatal("failed refresh published partial profile/configuration state")
	}
}
