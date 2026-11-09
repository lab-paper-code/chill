package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// Observer republishes cloned profile slices to Runtime, while scheduler capture
// uses a separate canonical pointer. Equal catalog bodies must adopt that new
// source, or every assignment and final keeps marshaling the same large profile.
func TestRuntimeCatalogAdoptsRepublishedImmutableSource(t *testing.T) {
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{QueueLimit: 1, ObservationLog: log})
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot := schedulerFixture()
	definition, instance := snapshot.Workloads[0], snapshot.Instances[0]
	runtime.SetProfiles([]serving.Profile{*instance.Profile})
	if err := runtime.Publish(definition); err != nil {
		t.Fatal(err)
	}
	if err := log.SchedulerRecord(SchedulerObservation{At: time.Now(), Snapshot: &serving.SchedulerSnapshot{Instances: []serving.SchedulerInstance{instance}}}); err != nil {
		t.Fatal(err)
	}
	const publications, requests = 8, 3
	for publication := range publications {
		profiles := slices.Clone([]serving.Profile{*instance.Profile})
		profile := &profiles[0]
		if err := runtime.InstallInstances([]serving.ReadyInstance{{
			State: serving.InstanceState{ID: instance.ID, Node: instance.Node, Profile: profile.ID, Ready: true},
			Profile: profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
			Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
				return serving.BackendResult{Finished: true}, nil
			},
		}}); err != nil {
			t.Fatal(err)
		}
		for range requests {
			receipt, err := runtime.Receive(definition.Name, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			call, err := runtime.Accept(&receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := call.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if err := runtime.Response(receipt, "completed", ""); err != nil {
				t.Fatal(err)
			}
			if done, err := runtime.Drain(ctx, definition.UID); err != nil || !done {
				t.Fatalf("request did not drain: %v %v", done, err)
			}
			log.submitMu.Lock()
			source := log.references[observationProfileCatalog+"/"+profile.ID].source
			retained, queued := len(log.references), len(log.queue)
			log.submitMu.Unlock()
			if source != profile || retained != 2 || queued != 0 {
				t.Fatalf("publication %d retained old source or backlog: source=%p want=%p keys=%d queue=%d", publication, source, profile, retained, queued)
			}
		}
	}
	log.requestMu.Lock()
	open := len(log.open)
	log.requestMu.Unlock()
	if open != 0 {
		t.Fatalf("completed requests retained %d payloads", open)
	}
	log.mu.Lock()
	for key, entry := range log.catalog {
		if entry.source != nil {
			t.Errorf("committed catalog %s retained a source graph", key)
		}
	}
	log.mu.Unlock()
	rows := readRequestRows(t, log.config.Directory)
	if len(rows) != 2*publications*requests {
		t.Fatalf("assignment/final rows lost: %d", len(rows))
	}
	for index := 0; index < len(rows); index += 2 {
		assigned, final := rows[index], rows[index+1]
		if assigned.Kind != serving.RequestRowAssigned || final.Kind != serving.RequestRowFinal || assigned.RequestID != final.RequestID || final.Outcome != "completed" || !final.ExecutionComplete || final.ProfileID != instance.Profile.ID {
			t.Fatalf("request record order or retained facts changed: %+v %+v", assigned, final)
		}
	}
	page, _, _, _, err := log.SchedulerObservationPage(ctx, log.started.Add(-time.Second), time.Now().Add(time.Second), 1, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 1 || page[0].Snapshot.Instances[0].Profile.ID != instance.Profile.ID {
		t.Fatalf("catalog consumer lost republished profile: %+v %v", page, err)
	}
}

// Overlay this source on 02b521a0 and compare with C1 at the same Go/toolchain.
// Draining outside the timer bounds the real FIFO without measuring fsync.
func BenchmarkRequestCatalogRepublication(b *testing.B) {
	for _, size := range []int{1 << 10, 1 << 20} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			ctx := context.Background()
			at := observationTestHour
			log, err := NewObservationLog(ObservationLogConfig{Directory: b.TempDir(), Store: "evidence", Writer: &observationArchive{directory: b.TempDir()}, Retention: time.Hour, Now: func() time.Time { return at }})
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = log.Close(ctx) }()
			original := &serving.Profile{ID: "profile", RuntimeDelivery: json.RawMessage(`{"payload":"` + strings.Repeat("x", size) + `"}`)}
			seed := func(id string, profile *serving.Profile) {
				if err := log.RequestEvent(serving.RequestEvent{RequestID: id, Kind: serving.RequestRowAssigned, At: at, ReceivedAt: at, Assignment: &serving.Assignment{Instance: "physical", Profile: profile.ID, ProfileBody: profile, DispatchedAt: at}}); err != nil {
					b.Fatal(err)
				}
				if err := log.RequestClosed(id, serving.RequestTimings{}); err != nil {
					b.Fatal(err)
				}
			}
			seed("seed", original)
			if err := log.Drain(ctx); err != nil {
				b.Fatal(err)
			}
			profile := *original
			b.ReportAllocs()
			b.ResetTimer()
			for index := range b.N {
				seed(fmt.Sprint(index), &profile)
				if index%64 == 63 {
					b.StopTimer()
					if err := log.Drain(ctx); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
			}
		})
	}
}
