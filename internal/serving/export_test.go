package serving

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

// The external-test constructor hook avoids a serving -> observation -> serving test import cycle without production exports.
var RuntimeObservationConstructor func(RuntimeTestObservationConfig) (RuntimeTestObservation, error)

type RuntimeTestObservationConfig struct {
	Directory   string
	Store       string
	Writer      evidencestore.StreamingImmutableWriter
	Retention   time.Duration
	Diagnostics *Diagnostics
}

type RuntimeTestObservation interface {
	RuntimeObservation
	Close(context.Context) error
	HasRequest(string, string) (bool, error)
}

type runtimeTestLog struct {
	RuntimeTestObservation
	config RuntimeTestObservationConfig
	mu     sync.Mutex
}

func newRuntimeTestLog(config RuntimeTestObservationConfig) (*runtimeTestLog, error) {
	if RuntimeObservationConstructor == nil {
		return nil, errors.New("runtime observation test constructor was not registered by serving_test")
	}
	log, err := RuntimeObservationConstructor(config)
	if err != nil {
		return nil, err
	}
	return &runtimeTestLog{RuntimeTestObservation: log, config: config}, nil
}

// The gate stops recorder admission, before forwarding to the real async log.
func (l *runtimeTestLog) RequestEvent(event RequestEvent) error {
	if event.Kind == phaseAssigned {
		l.mu.Lock()
		defer l.mu.Unlock()
	}
	return l.RuntimeTestObservation.RequestEvent(event)
}

func (l *runtimeTestLog) RequestClosed(id string, timing RequestTimings) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.RuntimeTestObservation.RequestClosed(id, timing)
}

// blockRuntimeObservationWrites creates real path conflicts for every minute in
// this and the next hour, so failures do not depend on a private clock or a minute boundary.
func blockRuntimeObservationWrites(t *testing.T, log *runtimeTestLog) {
	t.Helper()
	root := filepath.Join(log.config.Directory, "v1alpha6")
	hour := time.Now().UTC().Truncate(time.Hour)
	var paths []string
	for h := range 2 {
		for m := range 60 {
			at := hour.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
			path := filepath.Join(root, at.Format("20060102T15"), "requests."+at.Format("04")+".jsonl")
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			paths = append(paths, path)
		}
	}
	t.Cleanup(func() {
		for _, path := range paths {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		}
	})
}

func CapacityBridgeObservationFixture(t *testing.T) (SchedulerDecision, RuntimeTestObservationConfig) {
	t.Helper()
	settings, snapshot, _, _ := capacityBridgeFixture(t)
	decision := capacityDecideForTest(t, settings, snapshot)
	if decision.Target == nil {
		t.Fatal("bridge fixture lost its target")
	}
	return decision, RuntimeTestObservationConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: &observationArchive{directory: t.TempDir()},
	}
}

func NativeHTTPObservationFixture(t *testing.T, execute func(context.Context, Dispatch) (BackendResult, error)) (*Runtime, *Definition, RuntimeTestObservation) {
	t.Helper()
	r, instance, _ := routingRuntime(t, map[string]int32{"w1": 1})
	r.instances[instance.ID].Execute = execute
	return r, r.workloads["w1"].definition, r.config.ObservationLog.(*runtimeTestLog).RuntimeTestObservation
}
