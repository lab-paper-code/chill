package kube

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	edge "github.com/lab-paper-code/chill/api/v1alpha1"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
	"github.com/lab-paper-code/chill/internal/serving"
	"k8s.io/apimachinery/pkg/api/resource"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const reuseTargetGPU = "GPU-target"

func reuseProfilesFixture(t *testing.T) (serving.Profile, serving.Profile) {
	t.Helper()
	catalog, key, _, _ := catalogEvidenceFixture(t, 4096)
	view, err := catalog.Refresh(context.Background(), []string{key.Namespace})
	if err != nil || len(view.Profiles) != 1 {
		t.Fatalf("verified source unavailable: %+v %v", view, err)
	}
	source := view.Profiles[0]
	if source.Batches[0].MeanWallWatts <= 0 {
		t.Fatal("active wall power was not restored from measured energy/window")
	}
	source.NodeName, source.Condition.NodeUID = "genesis2", "genesis2-uid"
	source.Condition.MetricSource.NodeName, source.MetricSource.NodeName = source.NodeName, source.NodeName
	source.Condition.Backend, source.RuntimeMetadata.Runtime = "ort-cuda", "ort-cuda"
	source.RuntimeMetadata.GPUUUID = "GPU-source"
	source.Condition.RuntimeAccess = &profilederivation.CurveRuntimeAccessContract{GPUUUID: "GPU-source", Resources: map[string]string{"nvidia.com/gpu": "1"}}
	source.RuntimeDelivery = json.RawMessage(`{"gpuUUID":"GPU-source"}`)
	target := cloneCatalogProfile(source)
	target.ID = "chill-system/confirmation/confirmation-uid"
	target.NodeName, target.Condition.NodeUID = "genesis3", "genesis3-uid"
	target.Condition.Name = "confirmation-plan"
	target.Condition.MetricSource.NodeName, target.MetricSource.NodeName = target.NodeName, target.NodeName
	target.Condition.MetricSource.Endpoint = "http://target-meter"
	target.RuntimeMetadata.GPUUUID, target.Condition.RuntimeAccess.GPUUUID = reuseTargetGPU, reuseTargetGPU
	target.RuntimeDelivery = json.RawMessage(`{"gpuUUID":"GPU-target"}`)
	target.MeasuredAt = target.MeasuredAt.Add(time.Minute)
	return source, target
}

func reuseView(t *testing.T, source, target serving.Profile, maximum float64) CatalogView {
	t.Helper()
	request, err := json.Marshal(profileReuseRequest{SourceProfileID: source.ID, MaximumRelativeDifference: maximum})
	if err != nil {
		t.Fatal(err)
	}
	view, err := applyProfileReuse(CatalogView{Profiles: []serving.Profile{source, target}}, map[string]string{target.ID: string(request)})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func TestProfileReuseMaterializesTargetIdentityAndRetainsConfirmation(t *testing.T) {
	source, target := reuseProfilesFixture(t)
	for index := range target.Batches {
		for sample := range target.Batches[index].DurationNanos {
			target.Batches[index].DurationNanos[sample] *= 1.04
		}
		target.Batches[index].MeanWallWatts *= 1.04
		target.Batches[index].MemoryBytes *= 2
	}
	target.IdleBaselineWatts *= 1.04
	view := reuseView(t, source, target, .05)
	if len(view.Profiles) != 2 || len(view.ReuseChecks) != 1 || view.ReuseChecks[0].Outcome != measurementAccepted || len(view.Insufficiencies) != 0 {
		t.Fatalf("equivalent confirmation rejected: %+v", view)
	}
	profile := view.Profiles[1]
	if !strings.HasPrefix(profile.ID, target.ID+"/reuse-") || profile.Condition.NodeUID != target.Condition.NodeUID ||
		profile.NodeName != target.NodeName || profile.RuntimeMetadata.GPUUUID != reuseTargetGPU ||
		profile.Condition.RuntimeAccess.GPUUUID != reuseTargetGPU || string(profile.RuntimeDelivery) != string(target.RuntimeDelivery) ||
		!reflect.DeepEqual(profile.MetricSource, target.MetricSource) || profile.IdleBaselineWatts != target.IdleBaselineWatts ||
		!reflect.DeepEqual(profile.Batches[0].DurationNanos, source.Batches[0].DurationNanos) ||
		profile.Batches[0].EnergyPerInputJoules != source.Batches[0].EnergyPerInputJoules ||
		profile.Batches[0].MemoryBytes != target.Batches[0].MemoryBytes || profile.Reuse == nil ||
		profile.Reuse.ConfirmationTrialsPerBatch != 3 || len(profile.Evidence) <= len(target.Evidence) {
		t.Fatalf("source facts leaked into target physical contract: %+v", profile)
	}
	profile.Batches[0].DurationNanos[0] = -1
	profile.RuntimeMetadata.GPUUUID = "mutated"
	profile.Reuse.Batches[0].ServiceRelativeDifference = 9
	if source.Batches[0].DurationNanos[0] <= 0 || target.RuntimeMetadata.GPUUUID != reuseTargetGPU || view.ReuseChecks[0].Batches[0].ServiceRelativeDifference > .05 {
		t.Fatal("mutable reused view aliases original or confirmation check")
	}
}

func TestProfileReuseRejectsUnconfirmedConditionsAndDifferences(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*serving.Profile, *serving.Profile)
	}{
		{"device-family", func(_, p *serving.Profile) { p.Condition.DeviceClass += "-different" }},
		{"runtime-image", func(_, p *serving.Profile) { p.Condition.RuntimeImage += "changed" }},
		{"cpu-affinity", func(_, p *serving.Profile) { p.Condition.CPU.AffinityCPUs = []int{99} }},
		{"power-mode", func(_, p *serving.Profile) { p.Condition.PowerMode = "different" }},
		{"runner-version", func(_, p *serving.Profile) { p.RuntimeMetadata.RuntimeVersion += "changed" }},
		{"short-window", func(_, p *serving.Profile) { p.Condition.ActiveProtocol.DurationSeconds = 29 }},
		{"too-few-trials", func(_, p *serving.Profile) { p.TrialsPerBatch = 2 }},
		{"missing-batch", func(s, p *serving.Profile) {
			for _, profile := range []*serving.Profile{s, p} {
				profile.Condition.PlannedBatchSizes = []int{1, 2}
				profile.Condition.Firstfloor.MaxBatchSize = 2
				profile.RuntimeMetadata.InitializedBatches = []int{1, 2}
			}
			s.Batches = append(s.Batches, serving.BatchProfile{Batch: 2, DurationNanos: []float64{100}, MeanWallWatts: 10})
		}},
		{"service-difference", func(_, p *serving.Profile) {
			for index := range p.Batches[0].DurationNanos {
				p.Batches[0].DurationNanos[index] *= 1.06
			}
		}},
		{"active-power-difference", func(_, p *serving.Profile) { p.Batches[0].MeanWallWatts *= 1.06 }},
		{"idle-power-difference", func(_, p *serving.Profile) { p.IdleBaselineWatts *= 1.06 }},
		{"missing-power", func(_, p *serving.Profile) { p.Batches[0].MeanWallWatts = 0 }},
		{"zero-source-idle", func(s, _ *serving.Profile) { s.IdleBaselineWatts = 0 }},
		{"same-node", func(s, p *serving.Profile) { p.Condition.NodeUID = s.Condition.NodeUID }},
		{"different-namespace", func(_, p *serving.Profile) { p.ID = "other/confirmation/uid" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			source, target := reuseProfilesFixture(t)
			test.mutate(&source, &target)
			view := reuseView(t, source, target, .05)
			if len(view.Profiles) != 1 || view.Profiles[0].ID != source.ID || len(view.ReuseChecks) != 1 ||
				view.ReuseChecks[0].Outcome != profileReuseRejected || len(view.Insufficiencies) != 1 ||
				!strings.Contains(view.Insufficiencies[0].Reason, "independent target measurement required") {
				t.Fatalf("failed confirmation became independent/reused row: %+v", view)
			}
		})
	}
}

func TestProfileReuseToleranceIsConfiguredAndInclusive(t *testing.T) {
	for _, test := range []struct {
		source, target, maximum float64
		accepted                bool
	}{
		{100, 105, .05, true}, {100, 95, .05, true}, {100, 105.01, .05, false},
		{100, 104, .03, false}, {100, 104, .04, true},
	} {
		difference, err := reuseRelativeDifference(test.source, test.target)
		if err != nil || (difference <= test.maximum) != test.accepted {
			t.Fatalf("configured relative boundary changed: %+v, %g %v", test, difference, err)
		}
	}
	source, target := reuseProfilesFixture(t)
	source.IdleBaselineWatts, target.IdleBaselineWatts = 100, 105
	if view := reuseView(t, source, target, .05); view.ReuseChecks[0].Outcome != measurementAccepted {
		t.Fatalf("inclusive configured boundary rejected: %+v", view.ReuseChecks)
	}
	if view := reuseView(t, source, target, .03); view.ReuseChecks[0].Outcome != profileReuseRejected {
		t.Fatalf("tighter configured threshold ignored: %+v", view.ReuseChecks)
	}
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := reuseRelativeDifference(value, 1); err == nil {
			t.Fatalf("invalid source value accepted: %g", value)
		}
	}
	for _, raw := range []string{`{}`, `null`, `{"sourceProfileID":"p","maximumRelativeDifference":0}`,
		`{"sourceProfileID":"p","maximumRelativeDifference":1}`, `{"sourceProfileID":"p","maximumRelativeDifference":0.05,"typo":true}`,
		`{"sourceProfileID":"p","maximumRelativeDifference":0.05} {}`} {
		if _, err := decodeProfileReuseRequest(raw); err == nil {
			t.Fatalf("invalid annotation accepted: %s", raw)
		}
	}
}

func TestProfileReuseRequiresCurrentDirectSource(t *testing.T) {
	source, target := reuseProfilesFixture(t)
	request := `{"sourceProfileID":"` + source.ID + `","maximumRelativeDifference":0.05}`
	newer := cloneCatalogProfile(source)
	newer.ID += "-newer"
	newer.MeasuredAt = source.MeasuredAt.Add(time.Hour)
	for _, profiles := range [][]serving.Profile{{target}, {source, newer, target}} {
		view, err := applyProfileReuse(CatalogView{Profiles: profiles}, map[string]string{target.ID: request})
		if err != nil || len(view.ReuseChecks) != 1 || view.ReuseChecks[0].Outcome != profileReuseRejected {
			t.Fatalf("missing/superseded source accepted: %+v %v", view, err)
		}
	}
	view, err := applyProfileReuse(CatalogView{Profiles: []serving.Profile{source, target}}, map[string]string{source.ID: request, target.ID: request})
	if err != nil || len(view.Profiles) != 0 || len(view.ReuseChecks) != 2 {
		t.Fatalf("self/transitive confirmation became source: %+v %v", view, err)
	}
}

func TestCatalogRefreshReadsReuseAnnotationWithoutIndependentFallback(t *testing.T) {
	catalog, key, _, _ := catalogEvidenceFixture(t, 4096)
	campaign := &edge.BatchEnergyOptimization{}
	if err := catalog.store.client.Get(context.Background(), key, campaign); err != nil {
		t.Fatal(err)
	}
	campaign.Annotations = map[string]string{profileReuseAnnotation: `{"sourceProfileID":"missing/source/uid","maximumRelativeDifference":0.05}`}
	if err := catalog.store.client.Update(context.Background(), campaign); err != nil {
		t.Fatal(err)
	}
	view, err := catalog.Refresh(context.Background(), []string{key.Namespace})
	if err != nil || len(view.Profiles) != 0 || len(view.ReuseChecks) != 1 || view.ReuseChecks[0].Outcome != profileReuseRejected {
		t.Fatalf("annotation bypassed catalog reuse boundary: %+v %v", view, err)
	}
}

func TestProfileReuseConnectsTargetPlannerOptionAndGPUInventory(t *testing.T) {
	ctx := context.Background()
	manager, _ := newManagerHarness(t)
	createManagerCatalog(t, manager)
	if _, err := manager.CreateWorkload(ctx, "workload", managerWorkloadSpec()); err != nil {
		t.Fatal(err)
	}
	definition := manager.runtime.SchedulerInputs(time.Now).Workloads[0]
	source, target := reuseProfilesFixture(t)
	model := managerProfile(definition)
	for _, profile := range []*serving.Profile{&source, &target} {
		profile.Condition.LogicalModel = model.Condition.LogicalModel
		profile.Condition.Artifact, profile.Condition.Preprocessor = model.Condition.Artifact, model.Condition.Preprocessor
		profile.Condition.ActiveProtocol.Dataset = model.Condition.ActiveProtocol.Dataset
		profile.Condition.RuntimeFamily = model.Condition.RuntimeFamily
		profile.Condition.Backend, profile.RuntimeMetadata.Runtime = model.Condition.Backend, model.Condition.Backend
		profile.Condition.Firstfloor = model.Condition.Firstfloor
	}
	view := reuseView(t, source, target, .05)
	if len(view.Profiles) != 2 {
		t.Fatalf("target row missing: %+v", view)
	}
	profile := view.Profiles[1]
	controller := schedulerEmptyPoolController(t)
	controller.runtime = manager.runtime
	controller.observer.SetProfiles(view.Profiles)
	_, node, _, _ := observerSourceFixture()
	node.Name, node.UID = target.NodeName, types.UID(target.Condition.NodeUID)
	node.Status.Allocatable["nvidia.com/gpu"] = resource.MustParse("1")
	if _, err := controller.kube.CoreV1().Nodes().Create(ctx, node, meta.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	config := controller.config
	config.Preparations = map[string]PrepareSpec{profile.ID: {Instance: serving.SchedulerInstance{
		ID: "target-option", Node: serving.NodeReference{Name: profile.NodeName, UID: profile.Condition.NodeUID}, Profile: &profile,
		MaxBatch: 1, Capacity: 32,
	}}}
	snapshot, err := controller.capture(ctx, time.Now().UTC(), config)
	if err != nil || len(snapshot.Options) != 1 || snapshot.Options[0].Instance.Profile.ID != profile.ID ||
		snapshot.Options[0].Instance.Node.UID != target.Condition.NodeUID || len(snapshot.Options[0].CompatibleUIDs) != 1 {
		t.Fatalf("accepted target row did not reach planner: %+v %v", snapshot.Options, err)
	}
	if gpus := measuredGPUs(view.Profiles, target.Condition.NodeUID); !reflect.DeepEqual(gpus, []string{reuseTargetGPU}) {
		t.Fatalf("source GPU polluted target inventory: %v", gpus)
	}
}

func TestProfileReuseIdentityPinsSourceAndPolicy(t *testing.T) {
	source, target := reuseProfilesFixture(t)
	first := reuseView(t, source, target, .05).Profiles[1]
	tighter := reuseView(t, source, target, .04).Profiles[1]
	source.ID += "-another-source"
	other := reuseView(t, source, target, .05).Profiles[1]
	repeated := reuseView(t, source, target, .05).Profiles[1]
	if first.ID == tighter.ID || first.ID == other.ID || other.ID != repeated.ID {
		t.Fatal("mutable source/policy annotation collides with an immutable profile bundle")
	}
}

func installReuseMeasurementProfile(t *testing.T, harness *opcostHarness) string {
	t.Helper()
	profile := harness.controller.observer.Profiles()[0]
	confirmationID := profile.ID
	harness.controller.mu.Lock()
	spec := harness.controller.config.Preparations[confirmationID]
	profile.ID += "/reuse-confirmed"
	profile.Reuse = &serving.ProfileReuseCheck{
		SourceProfileID: "chill-system/original/original-uid", ConfirmationProfileID: confirmationID,
		Outcome: measurementAccepted,
	}
	spec.Instance.Profile = &profile
	spec.Config.Policy.ProfileID = profile.ID
	harness.controller.config.Preparations = map[string]PrepareSpec{profile.ID: spec}
	harness.controller.mu.Unlock()
	harness.controller.observer.SetProfiles([]serving.Profile{profile})
	return profile.ID
}

func TestProfileReuseOperationCostResolvesConfirmation(t *testing.T) {
	harness := newOpcostHarness(t)
	id := installReuseMeasurementProfile(t, harness)
	measurement := opcostRoot("reuse-cost", time.Now(), lane()...)
	harness.create(measurement)
	harness.admit()
	status := harness.status(measurement)
	if status.Phase != edge.MeasurementPhaseComplete || len(harness.measured) != 5 ||
		harness.measured[1].Operation.Prepare.Instance.Profile.ID != id {
		t.Fatalf("confirmation BEO did not resolve its prepared reused row: %+v", status)
	}
}

func TestProfileReuseOccupancyResolvesConfirmationBeforeBatchJudgment(t *testing.T) {
	harness := occupancyHarness(t)
	installReuseMeasurementProfile(t, harness)
	measurement := occupancyRoot("reuse-occupancy", time.Now(), 3)
	harness.create(measurement)
	harness.admit()
	// Reaching the measured-batch judgment proves occupancy used the shared
	// resolver instead of waiting forever for the absent direct profile ID.
	if got := harness.admission(measurement); got != "Refused: BatchUnmeasured 3" {
		t.Fatalf("occupancy did not resolve the confirmation profile: %q", got)
	}
}
