package serving_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/observation"
)

func TestCapacityBridgeCatalogProjectionDoesNotMutateLiveProfiles(t *testing.T) {
	candidate, config := serving.CapacityBridgeObservationFixture(t)
	stage := slices.IndexFunc(candidate.Actions, func(action serving.Action) bool { return action.RouteTarget != nil })
	if stage < 0 {
		t.Fatal("bridge fixture lost its staged handoff")
	}
	if err := cleanObservationPredecessor(config.Directory); err != nil {
		t.Fatal(err)
	}
	log, err := observation.NewObservationLog(observation.ObservationLogConfig{
		Directory: config.Directory, Store: config.Store, Writer: config.Writer, Retention: config.Retention,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	at := time.Now().UTC()
	if err = log.SchedulerRecord(observation.SchedulerObservation{At: at, Decision: &serving.SchedulerDecision{At: at, Target: &candidate}}); err != nil {
		t.Fatal(err)
	}
	if err := log.Drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(config.Directory, "v1alpha5", "*", "scheduler.*.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("scheduler evidence paths: %v, %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Decision *serving.SchedulerDecision `json:"decision"`
	}
	if err = json.Unmarshal(bytes.TrimSpace(raw), &stored); err != nil {
		t.Fatal(err)
	}
	if stored.Decision == nil || stored.Decision.Target == nil {
		t.Fatal("recorded bridge lost its target")
	}
	if stored.Decision.Target.Actions[stage].RouteTarget.Instances[0].Profile != nil || candidate.Actions[stage].RouteTarget.Instances[0].Profile == nil {
		t.Fatal("nested bridge catalog projection leaked or mutated live profiles")
	}
}
