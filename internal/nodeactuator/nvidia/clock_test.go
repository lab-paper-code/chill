package nvidia

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/nodeactuator"
)

func clockTarget(mhz int32) nodeactuator.Target {
	return nodeactuator.Target{Kind: nodeactuator.ActuatorKindNvidiaLockedGPUClock,
		NvidiaLockedGPUClock: &nodeactuator.NvidiaLockedGPUClockTarget{MinMHz: mhz, MaxMHz: mhz}}
}

func clockAdapter(t *testing.T) (*Clock, nodeactuator.Request) {
	t.Helper()
	a, err := New(Options{Command: []string{"/host/ld.so", "/host/nvidia-smi"}, Timeout: time.Second, Clocks: []int32{1500, 1200, 900}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a.now = func() time.Time { return now }
	return a, nodeactuator.Request{PowerMode: "GPU_900", Target: clockTarget(900), Deadline: now.Add(time.Minute)}
}

func TestApplyObservesIndependentlyAndRestartsWithoutMutation(t *testing.T) {
	a, request := clockAdapter(t)
	var calls [][]string
	mhz := "1500"
	a.run = func(_ context.Context, argv []string) ([]byte, error) {
		calls = append(calls, append([]string(nil), argv...))
		if argv[len(argv)-2] == "-lgc" {
			mhz = "900"
		}
		return []byte(mhz + ", " + mhz + "\n"), nil
	}
	if err := a.Apply(context.Background(), request, request.Target); err != nil {
		t.Fatal(err)
	}
	observation, err := a.Observe(context.Background(), request)
	if err != nil || observation.Target.NvidiaLockedGPUClock.MinMHz != 900 || observation.ObservedAt.IsZero() {
		t.Fatalf("observation=%+v error=%v", observation, err)
	}
	if len(calls) != 4 || !reflect.DeepEqual(calls[1], []string{"/host/ld.so", "/host/nvidia-smi", "-i", "0", "-lgc", "900,900"}) {
		t.Fatalf("calls=%v", calls)
	}
	// A new process has no cached intent. Only driver output determines state.
	restarted, _ := clockAdapter(t)
	restarted.run = a.run
	before := len(calls)
	if _, err := restarted.Observe(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(calls) != before+1 || strings.Contains(strings.Join(calls[len(calls)-1], " "), "-lgc") {
		t.Fatalf("restart mutated host: %v", calls)
	}
	mhz = "300" // Driver defaults after reboot must not look like desired 900.
	observation, err = restarted.Observe(context.Background(), request)
	if err != nil || observation.Target.NvidiaLockedGPUClock.MinMHz != 300 {
		t.Fatalf("post-boot observation=%+v error=%v", observation, err)
	}
}

func TestApplyLocksWhenInstantaneousClockAlreadyMatches(t *testing.T) {
	a, request := clockAdapter(t)
	locks := 0
	a.run = func(_ context.Context, argv []string) ([]byte, error) {
		if argv[len(argv)-2] == "-lgc" {
			locks++
		}
		return []byte("900,900"), nil
	}
	if err := a.Apply(context.Background(), request, request.Target); err != nil {
		t.Fatal(err)
	}
	if locks != 1 {
		t.Fatalf("matching instantaneous MHz skipped lock: %d commands", locks)
	}
}

func TestApplyFailureRestoresAndRemainsFailure(t *testing.T) {
	for _, test := range []struct {
		name, previous                       string
		commandError, restoreError, mismatch bool
		want                                 string
	}{
		{name: "permission", previous: "1500", commandError: true, want: "previous clock restored"},
		{name: "mismatch", previous: "1500", mismatch: true, want: "read-back mismatch"},
		{name: "default-policy", previous: "300", commandError: true, want: "unlocked policy is not queryable"},
		{name: "restore-error", previous: "1500", commandError: true, restoreError: true, want: "restoration failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, request := clockAdapter(t)
			var mutations []string
			current := test.previous
			a.run = func(ctx context.Context, argv []string) ([]byte, error) {
				joined := strings.Join(argv, " ")
				if strings.Contains(joined, "-lgc") || strings.Contains(joined, "-rgc") {
					mutations = append(mutations, joined)
					if len(mutations) == 1 {
						if test.commandError {
							return []byte("Insufficient Permissions"), errors.New("denied")
						}
						if test.mismatch {
							current = "1200"
						}
					} else {
						if ctx.Err() != nil {
							t.Fatal("cleanup context canceled")
						}
						if test.restoreError {
							return nil, errors.New("restore denied")
						}
						current = test.previous
					}
				}
				return []byte(current + "," + current), nil
			}
			err := a.Apply(context.Background(), request, request.Target)
			if err == nil || !strings.Contains(err.Error(), test.want) || len(mutations) != 2 {
				t.Fatalf("error=%v mutations=%v", err, mutations)
			}
			wantRestore := "-lgc 1500,1500"
			if test.previous == "300" {
				wantRestore = "-rgc"
			}
			if !strings.Contains(mutations[1], wantRestore) {
				t.Fatalf("restore=%v", mutations)
			}
		})
	}
}

func TestObserveRejectsUnestablishedAndMultipleGPUs(t *testing.T) {
	for _, output := range []string{"N/A,N/A", "900,899", "0,0", "900", "900,900\n900,900", "", "-1,-1", "900,900,900"} {
		t.Run(output, func(t *testing.T) {
			a, request := clockAdapter(t)
			a.run = func(context.Context, []string) ([]byte, error) { return []byte(output), nil }
			if _, err := a.Observe(context.Background(), request); err == nil {
				t.Fatalf("accepted %q", output)
			}
		})
	}
}

func TestInvalidTargetsAndExpiredDeadlineNeverMutate(t *testing.T) {
	for _, mutate := range []func(*nodeactuator.Request){
		func(r *nodeactuator.Request) { r.Target.NvidiaLockedGPUClock.MinMHz = 800 },
		func(r *nodeactuator.Request) { r.Target = clockTarget(1234) },
		func(r *nodeactuator.Request) { mode := int32(1); r.Target.NvidiaLockedGPUClock.Mode = &mode },
		func(r *nodeactuator.Request) { mode := int32(0); r.Target.NvidiaLockedGPUClock.Mode = &mode },
		func(r *nodeactuator.Request) { r.Deadline = time.Time{} },
	} {
		a, request := clockAdapter(t)
		mutate(&request)
		a.run = func(context.Context, []string) ([]byte, error) { t.Fatal("unexpected host command"); return nil, nil }
		if err := a.Apply(context.Background(), request, request.Target); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
}

func TestCanceledApplyStillHasBoundedRestoration(t *testing.T) {
	a, request := clockAdapter(t)
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	a.run = func(commandCtx context.Context, argv []string) ([]byte, error) {
		calls++
		if calls == 2 {
			cancel()
			return nil, context.Canceled
		}
		if calls > 2 {
			if _, ok := commandCtx.Deadline(); !ok || commandCtx.Err() != nil {
				t.Fatal("restoration is not independently bounded")
			}
		}
		return []byte("1500,1500"), nil
	}
	if err := a.Apply(ctx, request, request.Target); err == nil || calls != 4 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
