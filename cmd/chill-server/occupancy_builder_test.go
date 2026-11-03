package main

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	occupancyModelTest            = "model"
	occupancyForeignSelectionTest = "foreign-selection"
)

type occupancyFixture struct {
	intent       httpapi.OccupancyIntent
	policy       dispatchOccupancyPolicy
	cm           *core.ConfigMap
	profile      *serving.Profile
	node         *core.Node
	workload     *api.Workload
	preprocessor *api.Preprocessor
	preparation  servingkube.PrepareSpec
	timeout      time.Duration
}

func syntheticOccupancyFixture() occupancyFixture {
	f := occupancyFixture{
		intent: httpapi.OccupancyIntent{Node: "node", BatchEnergyOptimization: "beo", Workload: "workload", Batch: 2},
		policy: syntheticOccupancyPolicy(),
		cm: &core.ConfigMap{
			ObjectMeta: meta.ObjectMeta{
				Namespace:       "serving",
				Name:            "scheduler",
				UID:             "config-uid",
				ResourceVersion: "9",
			},
		},
		profile: &serving.Profile{
			ID:       "allowed/beo/beo-uid",
			NodeName: "node",
			Batches:  []serving.BatchProfile{{Batch: 2, DurationNanos: []float64{10e6, 20e6, 30e6}}},
		},
		node: &core.Node{ObjectMeta: meta.ObjectMeta{Name: "node", UID: "node-uid"}},
		workload: &api.Workload{
			ObjectMeta: meta.ObjectMeta{Name: "workload", UID: "workload-uid", Generation: 3},
			Spec: api.WorkloadSpec{
				ModelRef: api.CatalogReference{
					Name: occupancyModelTest,
				}, PreprocessorRef: api.CatalogReference{Name: "preprocessor"}, DatasetRef: &api.CatalogReference{Name: "dataset"},
				SLO: api.WorkloadSLO{Percentile: 99, TargetLatency: meta.Duration{Duration: 1500 * time.Millisecond}}},
		},
		preprocessor: &api.Preprocessor{
			ObjectMeta: meta.ObjectMeta{Name: "preprocessor", UID: "preprocessor-uid", Generation: 1},
			Spec: api.PreprocessorSpec{
				Input: api.RawInputContract{
					Fields: []api.RawField{{Name: "text", Format: "UTF8", MaxBytes: 1020}},
				}},
		},
		timeout: 2 * time.Second,
	}
	f.profile.Condition.NodeUID = "node-uid"
	f.profile.Condition.LogicalModel = occupancyModelTest
	f.profile.Condition.RuntimeImage = "runtime@sha256:synthetic"
	f.profile.Condition.Preprocessor.Name = "preprocessor"
	f.profile.Condition.Preprocessor.Inputs = []string{"text"}
	f.preparation.Config.MaxQueueBytes = 64 << 20
	f.preparation.Config.MaxQueueRequests = 128
	return f
}

func (f occupancyFixture) build() (*api.DispatchOccupancyMeasurement, error) {
	return deriveOccupancy(
		"allowed",
		f.intent,
		f.policy,
		f.cm,
		"scheduler.json",
		f.profile,
		f.node,
		f.workload,
		f.preprocessor,
		f.preparation,
		f.timeout,
	)
}

func requireOccupancyError(t *testing.T, err error, code string) {
	t.Helper()
	var domain *serving.Error
	if !errors.As(err, &domain) || domain.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func TestOccupancyDerivationFreezesProtocolAndCompleteElapsedBudget(t *testing.T) {
	f := syntheticOccupancyFixture()
	task, err := f.build()
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	if task.Spec.Workers != f.policy.Workers || task.Spec.Conditions.WaitingBatches != 3 ||
		task.Spec.MinimumPairs != f.policy.MinimumPairs || task.Spec.WarmupSeconds != f.policy.WarmupSeconds ||
		task.Spec.DurationSeconds != f.policy.BurstSeconds {
		t.Fatalf("configured burst was changed: %+v", task.Spec)
	}
	s := task.Spec
	budget := s.PreparationSeconds + s.JobSetupSeconds + s.CollectionSeconds + s.WarmupSeconds +
		s.DurationSeconds + s.RequestLifetimeSeconds + (s.DeliveryMarginMillis+999)/1000 + s.DrainSeconds
	if budget != s.PlannedElapsedSeconds {
		t.Fatalf("budget %d != E %d", budget, s.PlannedElapsedSeconds)
	}
	var inputs struct {
		DispatchOccupancy dispatchOccupancyPolicy                                     `json:"dispatchOccupancy"`
		Coefficients      dispatchOccupancyPolicy                                     `json:"coefficients"`
		Configuration     struct{ Namespace, Name, UID, ResourceVersion, Key string } `json:"configuration"`
	}
	if err := json.Unmarshal([]byte(s.Conditions.InputsJSON), &inputs); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(inputs.DispatchOccupancy, f.policy) || !reflect.DeepEqual(inputs.Coefficients, f.policy) ||
		inputs.Configuration.Namespace != f.cm.Namespace || inputs.Configuration.Name != f.cm.Name ||
		inputs.Configuration.UID != "config-uid" ||
		inputs.Configuration.ResourceVersion != "9" ||
		inputs.Configuration.Key != "scheduler.json" {
		t.Fatal("policy/provenance not frozen")
	}
	again, err := f.build()
	if err != nil || !reflect.DeepEqual(task, again) {
		t.Fatal("derivation is not deterministic", err)
	}
	f.policy.PlannedElapsedCapSeconds = float64(budget)
	atCap, err := f.build()
	if err != nil {
		t.Fatal("complete budget exactly at cap failed", err)
	}
	if err = atCap.Spec.ValidateConditions(); err != nil {
		t.Fatal(err)
	}
	f.policy.PlannedElapsedCapSeconds--
	_, err = f.build()
	requireOccupancyError(t, err, "OccupancyInfeasible")
	// Also exercise the stored-contract elapsed validator, not just builder arithmetic.
	var frozen map[string]any
	if err = json.Unmarshal([]byte(atCap.Spec.Conditions.InputsJSON), &frozen); err != nil {
		t.Fatal(err)
	}
	frozen["coefficients"].(map[string]any)["plannedElapsedCapSeconds"] = float64(budget - 1)
	raw, _ := json.Marshal(frozen)
	atCap.Spec.Conditions.InputsJSON = string(raw)
	if atCap.Spec.ValidateConditions() == nil {
		t.Fatal("stored validator accepted cap-1")
	}
}

func TestOccupancyDerivationRefusesLifetimeIdentityAndSupply(t *testing.T) {
	for name, change := range map[string]func(*occupancyFixture){
		"SLO ceil": func(f *occupancyFixture) {
			f.workload.Spec.SLO.TargetLatency.Duration = 2001 * time.Millisecond
		},
		"actual gateway cap": func(f *occupancyFixture) { f.timeout = 1999 * time.Millisecond },
		"Workload request lifetime": func(f *occupancyFixture) {
			f.workload.Spec.RequestLifetime = &meta.Duration{Duration: 1500 * time.Millisecond}
		},
		"node replacement": func(f *occupancyFixture) { f.node.UID = "replacement" },
		"wrong model": func(f *occupancyFixture) {
			f.workload.Spec.ModelRef.Name = occupancyForeignSelectionTest
		},
		"unbounded raw field":       func(f *occupancyFixture) { f.preprocessor.Spec.Input.Fields[0].MaxBytes = 0 },
		"missing batch":             func(f *occupancyFixture) { f.intent.Batch = 3 },
		"workers cannot hold batch": func(f *occupancyFixture) { f.policy.Workers = 1 },
		"worker cap":                func(f *occupancyFixture) { f.policy.Workers = 65 },
		"lifetime cannot hold queued batches": func(f *occupancyFixture) {
			f.profile.Batches[0].DurationNanos = []float64{1e9}
		},
		"byte supply cap": func(f *occupancyFixture) {
			f.preprocessor.Spec.Input.Fields[0].MaxBytes = 32 << 20
		},
		"native byte cap": func(f *occupancyFixture) { f.preparation.Config.MaxQueueBytes = 8191 },
		"queue count cap": func(f *occupancyFixture) { f.preparation.Config.MaxQueueRequests = 5 },
		"zero service":    func(f *occupancyFixture) { f.profile.Batches[0].DurationNanos = []float64{0} },
		"negative service": func(f *occupancyFixture) {
			f.profile.Batches[0].DurationNanos = []float64{-1}
		},
		"nonfinite service": func(f *occupancyFixture) {
			f.profile.Batches[0].DurationNanos = []float64{math.Inf(1)}
		},
		"nan service": func(f *occupancyFixture) {
			f.profile.Batches[0].DurationNanos = []float64{math.NaN()}
		},
		"elapsed overflow": func(f *occupancyFixture) {
			f.policy.CollectionSeconds = math.MaxInt32
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := syntheticOccupancyFixture()
			change(&f)
			_, err := f.build()
			requireOccupancyError(t, err, "OccupancyInfeasible")
		})
	}
	f := syntheticOccupancyFixture()
	f.workload.Spec.SLO.TargetLatency.Duration = 2 * time.Second
	if _, err := f.build(); err != nil {
		t.Fatal("inclusive lifetime/SLO/gateway boundary", err)
	}
}

func TestOccupancyBurstDoesNotDeriveWorkersWarmupOrDurationFromService(t *testing.T) {
	for _, population := range [][]float64{{1e6}, {10e6, 20e6, 30e6}, {300e6}} {
		f := syntheticOccupancyFixture()
		f.profile.Batches[0].DurationNanos = population
		f.preparation.Config.MaxQueueBytes = 8 * 1024
		f.preparation.Config.MaxQueueRequests = 6
		task, err := f.build()
		if err != nil {
			t.Fatal(err)
		}
		s := task.Spec
		if s.Workers != 8 || s.WarmupSeconds != 5 || s.DurationSeconds != 15 || s.MinimumPairs != 100 {
			t.Fatalf("profile timing changed configured burst: %+v", s)
		}
		var inputs struct {
			Computed struct {
				TailBatchSeconds               float64 `json:"tailBatchSeconds"`
				MinimumRequestLifetimeSeconds  float64 `json:"minimumRequestLifetimeSeconds"`
				FramedDecodedRequestUpperBytes int64   `json:"framedDecodedRequestUpperBytes"`
				QueueRequestCap                int64   `json:"queueRequestCap"`
				QueueBytesCap                  int64   `json:"queueBytesCap"`
			} `json:"computed"`
		}
		if err := json.Unmarshal([]byte(s.Conditions.InputsJSON), &inputs); err != nil {
			t.Fatal(err)
		}
		c := inputs.Computed
		if c.TailBatchSeconds != population[len(population)-1]/1e9 ||
			c.MinimumRequestLifetimeSeconds != 2 || c.FramedDecodedRequestUpperBytes != 1024 ||
			c.QueueRequestCap != 6 || c.QueueBytesCap != 8192 {
			t.Fatalf("lost native supply derivation: %+v", c)
		}
	}
	f := syntheticOccupancyFixture()
	f.policy.MinimumPairs = 1000000
	task, err := f.build()
	if err != nil || task.Spec.MinimumPairs != f.policy.MinimumPairs || task.Spec.DurationSeconds != 15 {
		t.Fatal("sampling floor must not imply a duration or precision guarantee", err)
	}
}

func TestOccupancyBurstLifetimeIncludesPartialQueuedBatchAndMargin(t *testing.T) {
	f := syntheticOccupancyFixture()
	f.workload.Spec.SLO.TargetLatency.Duration = time.Second
	f.policy.Workers = 9
	f.profile.Batches[0].DurationNanos = []float64{398e6}
	task, err := f.build()
	if err != nil {
		t.Fatal("exact lifetime boundary rejected", err)
	}
	if task.Spec.Conditions.WaitingBatches != 4 {
		t.Fatal("partial outstanding batch was rounded down")
	}
	f.profile.Batches[0].DurationNanos = []float64{399e6}
	_, err = f.build()
	requireOccupancyError(t, err, "OccupancyInfeasible")
}
