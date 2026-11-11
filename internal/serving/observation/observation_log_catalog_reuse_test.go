package observation

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
	"github.com/lab-paper-code/chill/internal/serving"
)

// Observer republishes cloned profile slices to Runtime, while scheduler capture
// uses a separate canonical pointer. Equal catalog bodies must adopt that new
// source, or every assignment and final keeps marshaling the same large profile.
func TestRuntimeCatalogAdoptsRepublishedImmutableSource(t *testing.T) {
	ctx := context.Background()
	log, _ := clockedObservationLog(t, ObservationLogConfig{}, time.Now().UTC())
	runtime, err := newObservedRuntime(t, ctx, serving.RuntimeConfig{ObservationLog: log})
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
			State:   serving.InstanceState{ID: instance.ID, Node: instance.Node, Profile: profile.ID, Ready: true, ObservedAt: time.Now(), Native: fixtureAdmissionNative(profile)},
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
			if err := runtime.Response(receipt, outcomeCompleted, ""); err != nil {
				t.Fatal(err)
			}
			if done, err := runtime.Drain(ctx, definition.UID); err != nil || !done {
				t.Fatalf("request did not drain: %v %v", done, err)
			}
			log.submitMu.Lock()
			retained, queued := len(log.references), len(log.queue)
			log.submitMu.Unlock()
			if retained != 2 || queued != 0 {
				t.Fatalf("publication %d retained old references or backlog: keys=%d queue=%d", publication, retained, queued)
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
		if assigned.Kind != serving.RequestRowAssigned || final.Kind != serving.RequestRowFinal || assigned.RequestID != final.RequestID || final.Outcome != outcomeCompleted || !final.ExecutionComplete || final.ProfileID != instance.Profile.ID {
			t.Fatalf("request record order or retained facts changed: %+v %+v", assigned, final)
		}
	}
	page, _, _, _, err := log.SchedulerObservationPage(ctx, log.started.Add(-time.Second), time.Now().Add(time.Second), 1, 0, SchedulerObservationSelection{})
	if err != nil || len(page) != 1 || page[0].Snapshot.Instances[0].ProfileID != instance.Profile.ID {
		t.Fatalf("catalog consumer lost republished profile: %+v %v", page, err)
	}
}

// Repeat paired records within the same physical trials. Hydrate the expanded
// serialized population without changing its conditional service law.
func repeatedPairedObservationProfile(t testing.TB, source *serving.Profile, copies int) *serving.Profile {
	t.Helper()
	profile := *source
	profile.Batches = slices.Clone(source.Batches)
	for index, batch := range source.Batches {
		samples := make([]firstfloor.ServiceSample, 0, copies*len(batch.ServiceSamples))
		for range copies {
			for _, original := range batch.ServiceSamples {
				samples = append(samples, original.Clone())
			}
		}
		distribution, err := firstfloor.NewServiceDistribution(samples)
		if err != nil {
			t.Fatal(err)
		}
		profile.Batches[index].ServiceSamples = samples
		profile.Batches[index].ServiceDistribution = distribution
	}
	return &profile
}

// Overlay this source on 02b521a0 and compare with C1 at the same Go/toolchain.
// Draining outside the timer bounds the real FIFO without measuring fsync.
func BenchmarkRequestCatalogRepublication(b *testing.B) {
	for _, copies := range []int{8, 8192} {
		b.Run(fmt.Sprintf("paired-copies=%d", copies), func(b *testing.B) {
			ctx := context.Background()
			at := observationTestHour
			log, _ := clockedObservationLog(b, ObservationLogConfig{}, at)
			_, snapshot := schedulerFixture()
			original := repeatedPairedObservationProfile(b, snapshot.Instances[0].Profile, copies)
			body, err := json.Marshal(original)
			if err != nil {
				b.Fatal(err)
			}
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
			b.ReportMetric(float64(len(body)), "profile-bytes")
		})
	}
}
