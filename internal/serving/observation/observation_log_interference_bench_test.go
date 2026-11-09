package observation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
)

// This public-consumer-only benchmark can be overlaid unchanged on 02b521a0.
// One synchronous scheduler producer drains each record, bounding its backlog.
func BenchmarkRuntimeObservationInterference(b *testing.B) {
	for _, recording := range []bool{false, true} {
		name := "idle"
		if recording {
			name = "scheduler-1MiB"
		}
		b.Run(name, func(b *testing.B) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			log, err := NewObservationLog(ObservationLogConfig{Directory: b.TempDir(), Store: "evidence", Writer: &observationArchive{directory: b.TempDir()}, Retention: time.Hour})
			if err != nil {
				b.Fatal(err)
			}
			defer func() { _ = log.Close(ctx) }()
			runtime, err := serving.NewRuntime(ctx, serving.RuntimeConfig{QueueLimit: 2, ObservationLog: log})
			if err != nil {
				b.Fatal(err)
			}
			_, snapshot := schedulerFixture()
			definition, instance := snapshot.Workloads[0], snapshot.Instances[0]
			runtime.SetProfiles([]serving.Profile{*instance.Profile})
			if err := runtime.Publish(definition); err != nil {
				b.Fatal(err)
			}
			if err := runtime.InstallInstances([]serving.ReadyInstance{{
				State: serving.InstanceState{ID: instance.ID, Node: instance.Node, Profile: instance.Profile.ID, Ready: true},
				Profile: instance.Profile, Routing: map[string]int32{definition.UID: 1}, Capacity: 1,
				Execute: func(context.Context, serving.Dispatch) (serving.BackendResult, error) {
					return serving.BackendResult{Finished: true}, nil
				},
			}}); err != nil {
				b.Fatal(err)
			}
			if err := log.Drain(ctx); err != nil {
				b.Fatal(err)
			}
			stop, stopped := make(chan struct{}), make(chan error, 1)
			if recording {
				payload := json.RawMessage(`{"payload":"` + strings.Repeat("x", 1<<20) + `"}`)
				go func() {
					for {
						select {
						case <-stop:
							stopped <- nil
							return
						default:
						}
						if err := log.SchedulerRecord(SchedulerObservation{At: time.Now(), Action: &SchedulerActionObservation{ID: "concurrent", Expected: payload}}); err != nil {
							stopped <- err
							return
						}
						if err := log.Drain(ctx); err != nil {
							stopped <- err
							return
						}
					}
				}()
			}
			b.ReportAllocs()
			b.ResetTimer()
			for index := range b.N {
				receipt, err := runtime.Receive(definition.Name, time.Now())
				if err != nil {
					b.Fatal(err)
				}
				call, err := runtime.Accept(&receipt, &serving.InferenceInput{Inputs: []serving.RawInput{{Name: "x", Datatype: "BYTES", Data: []byte("1")}}})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := call.Wait(ctx); err != nil {
					b.Fatal(err)
				}
				if err := runtime.Response(receipt, "completed", ""); err != nil {
					b.Fatal(err)
				}
				if index%64 == 63 {
					b.StopTimer()
					if err := log.Drain(ctx); err != nil {
						b.Fatal(err)
					}
					b.StartTimer()
				}
			}
			b.StopTimer()
			if recording {
				close(stop)
				if err := <-stopped; err != nil {
					b.Fatal(err)
				}
			}
			if done, err := runtime.Drain(ctx, definition.UID); err != nil || !done {
				b.Fatalf("request prefix did not drain: %v %v", done, err)
			}
		})
	}
}
