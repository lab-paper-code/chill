package serving

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	profilederivation "github.com/lab-paper-code/chill/internal/profiling/derivation"
)

func TestSelectConditionProfilesReplacementLineage(t *testing.T) {
	const replacementID = "replacement"
	q1 := Profile{ID: "q1", MeasuredAt: time.Unix(1, 0)}
	q2 := q1
	q2.ID, q2.MeasuredAt = "q2", time.Unix(2, 0)
	q2.Condition.ActiveProtocol.MetricObserverImage = "different-observer"
	q2.Condition.ActiveProtocol.DurationSeconds = 30
	q3 := q1
	q3.ID, q3.MeasuredAt = "q3", time.Unix(3, 0)
	replacement := q1
	replacement.ID, replacement.MeasuredAt = replacementID, time.Unix(4, 0)
	replacement.Condition.ActiveProtocol.DurationSeconds = 60
	newer := q1
	newer.ID, newer.MeasuredAt = "newer", time.Unix(5, 0)
	for _, tc := range []struct {
		name     string
		profiles []Profile
		replaced map[string]bool
		want     []string
	}{
		{"replacement retires target lineage across protocols", []Profile{q2, replacement, q1, q3}, map[string]bool{"q3": true}, []string{replacementID}},
		{"without replacement latest semantic condition", []Profile{q2, replacement, q1, q3}, nil, []string{replacementID}},
		{"newer same condition survives replacement", []Profile{q2, replacement, q1, q3, newer}, map[string]bool{"q3": true}, []string{"newer"}},
		{"newer same key without replacement", []Profile{q2, q1, q3, newer}, nil, []string{"newer"}},
		{"failed replacement does not revive protocol sibling", []Profile{q1, q2, q3}, map[string]bool{"q3": true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eligible, err := ExcludeReplacedConditionProfiles(tc.profiles, tc.replaced)
			if err != nil {
				t.Fatal(err)
			}
			got, err := SelectConditionProfiles(eligible, nil)
			if err != nil || len(got) != len(tc.want) {
				t.Fatalf("selection=%+v error=%v, want %v", got, err, tc.want)
			}
			for i := range got {
				if got[i].ID != tc.want[i] {
					t.Fatalf("selection=%+v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestSelectConditionProfilesSemanticExecutionIdentity(t *testing.T) {
	base := Profile{ID: "old", MeasuredAt: time.Unix(1, 0),
		Condition: profilederivation.CurvePlanSpec{
			NodeUID: "node", RuntimeFamily: "firstfloor", Backend: "ort-cpu",
			ActiveProtocol: profilederivation.CurveProtocolContract{
				Dataset: &profilederivation.CurveDatasetContract{Name: "inputs", UID: "population"}},
			Actor: firstfloor.Config{Backend: "ort-cpu", BatchSizes: []int{1, 2}, WarmupBatchSizes: []int{1, 2}},
		}}
	base.Condition.Actor.BuildRunnerCommand()
	for _, tc := range []struct {
		name     string
		change   func(*profilederivation.CurvePlanSpec)
		distinct bool
	}{
		{"runtime image", func(c *profilederivation.CurvePlanSpec) {
			c.RuntimeImage = schedulerTestNewInstance
			c.Actor.RuntimeImage = schedulerTestNewInstance
		}, true},
		{"observers", func(c *profilederivation.CurvePlanSpec) {
			c.ActiveProtocol.MetricObserverImage = schedulerTestNewInstance
			c.IdleProtocol.WorkloadObserverImage = schedulerTestNewInstance
		}, false},
		{"windows", func(c *profilederivation.CurvePlanSpec) {
			c.ActiveProtocol.DurationSeconds = 60
			c.IdleProtocol.SettlingDurationSeconds = 5
		}, false},
		{"meter", func(c *profilederivation.CurvePlanSpec) { c.MetricSource.Endpoint = "new-meter" }, false},
		{"lifecycle", func(c *profilederivation.CurvePlanSpec) {
			c.Actor.Mode = "profiling"
			c.Actor.Policy.FixedBatchSize = 2
			c.Actor.RunnerTimeoutMS = 1000
		}, false},
		{"dataset source UID", func(c *profilederivation.CurvePlanSpec) {
			c.ActiveProtocol.Dataset.UID = schedulerTestOther
		}, false},
		{"dataset", func(c *profilederivation.CurvePlanSpec) {
			c.ActiveProtocol.Dataset.Name = schedulerTestOther
		}, true},
		{"node", func(c *profilederivation.CurvePlanSpec) { c.NodeUID = schedulerTestOther }, true},
		{"backend", func(c *profilederivation.CurvePlanSpec) { c.Backend = "ort-cuda" }, true},
		{"declared runtime version", func(c *profilederivation.CurvePlanSpec) {
			c.Actor.RuntimePackages = map[string]string{"onnxruntime": schedulerTestOther}
		}, false},
		{"observed runtime version", func(c *profilederivation.CurvePlanSpec) { c.RuntimeVersion = "1.27" }, true},
		{"observed provider", func(c *profilederivation.CurvePlanSpec) { c.RuntimeProvider = "CPUExecutionProvider" }, false},
		{"memory", func(c *profilederivation.CurvePlanSpec) { c.Actor.MemoryLimitBytes = 1024 }, true},
		{"power", func(c *profilederivation.CurvePlanSpec) { c.PowerMode = schedulerTestOther }, true},
		{"generation", func(c *profilederivation.CurvePlanSpec) { c.Actor.MaxNewTokens = 32 }, true},
		{"warmup domain", func(c *profilederivation.CurvePlanSpec) {
			c.Actor.WarmupBatchSizes = []int{1}
			c.Actor.BuildRunnerCommand()
		}, false},
		{"warmup execution", func(c *profilederivation.CurvePlanSpec) {
			c.Actor.WarmupIterations = 2
			c.Actor.BuildRunnerCommand()
		}, false},
		{"initialized domain", func(c *profilederivation.CurvePlanSpec) { c.Actor.BatchSizes = []int{1} }, false},
		{"initialized capacity", func(c *profilederivation.CurvePlanSpec) { c.Actor.MaxBatchSize = 4 }, false},
		{"native command", func(c *profilederivation.CurvePlanSpec) { c.Actor.RunnerCommand[1] = "/different-runner.py" }, true},
		{"native inputs", func(c *profilederivation.CurvePlanSpec) { c.Actor.Inputs = []string{"different-input"} }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := base
			next.ID, next.MeasuredAt = schedulerTestNewInstance, time.Unix(2, 0)
			next.Condition = profilederivation.CloneCurvePlanSpec(base.Condition)
			tc.change(&next.Condition)
			got, err := SelectConditionProfiles([]Profile{base, next}, nil)
			want := 1
			if tc.distinct {
				want = 2
			}
			if err != nil || len(got) != want || got[len(got)-1].ID != schedulerTestNewInstance {
				t.Fatalf("selection=%+v error=%v, want %d conditions", got, err, want)
			}
		})
	}
}
