package serving

import (
	"encoding/json"
	"math"
	"testing"
	"time"
)

func TestRequestTimingsJSONPreservesPresenceAndInt64(t *testing.T) {
	var timings RequestTimings
	timings.Record(ServerReceiveDone, time.Second)
	body := []byte(`[{"stage":"authDone","elapsedNanos":9223372036854775807},{"stage":"authStart","elapsedNanos":0},{"stage":"authStart","elapsedNanos":99},{"stage":"workerHTTPStart","elapsedNanos":-9223372036854775808},{"stage":"futureStage","elapsedNanos":3}]`)
	if err := json.Unmarshal(body, &timings); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		stage   ServerStage
		elapsed time.Duration
		present bool
	}{
		{ServerAuthStart, 0, true},
		{ServerAuthDone, time.Duration(math.MaxInt64), true},
		{ServerWorkerHTTPStart, time.Duration(math.MinInt64), true},
		{ServerReceiveDone, 0, false},
		{ServerWorkerHTTPDone, 0, false},
	} {
		if elapsed, present := timings.Elapsed(check.stage); elapsed != check.elapsed || present != check.present {
			t.Fatalf("stage %d = %d, %v; want %d, %v", check.stage, elapsed, present, check.elapsed, check.present)
		}
	}
	encoded, err := json.Marshal(timings)
	want := `[{"stage":"authStart","elapsedNanos":0},{"stage":"authDone","elapsedNanos":9223372036854775807},{"stage":"workerHTTPStart","elapsedNanos":-9223372036854775808}]`
	if err != nil || string(encoded) != want {
		t.Fatalf("timing JSON changed: %s, %v", encoded, err)
	}
	before := timings
	if err := json.Unmarshal([]byte(`[{"stage":"authStart","elapsedNanos":9223372036854775808}]`), &timings); err == nil || timings != before {
		t.Fatalf("invalid integer changed receiver: %+v, %v", timings, err)
	}
	if err := json.Unmarshal([]byte(`null`), &timings); err != nil || !timings.IsZero() {
		t.Fatalf("null timing did not clear receiver: %+v, %v", timings, err)
	}
	empty, err := json.Marshal(timings)
	if err != nil || string(empty) != "[]" {
		t.Fatalf("empty timing invented stages: %s, %v", empty, err)
	}
	var old RequestRow
	if err := json.Unmarshal([]byte(`{"kind":"final","requestID":"old","futureField":true}`), &old); err != nil || !old.ServerTiming.IsZero() {
		t.Fatalf("additive decoder changed old row: %+v, %v", old, err)
	}
}

func TestRequestTimingsFoldOwnsValuesAndKeepsFirstObservation(t *testing.T) {
	var source RequestTimings
	source.Record(ServerAuthStart, 0)
	event := RequestEvent{ServerTiming: source}
	row := RequestRow{}
	row.Fold(&event)
	assigned := row
	source.Record(ServerAuthDone, time.Second)
	event.ServerTiming.Record(ServerBodyDecodeDone, 2*time.Second)
	row.Fold(&event)
	var later RequestTimings
	later.Record(ServerAuthStart, 99)
	later.Record(ServerResponseWriteDone, 3*time.Second)
	row.Fold(&RequestEvent{ServerTiming: later})
	if elapsed, ok := row.ServerTiming.Elapsed(ServerAuthStart); !ok || elapsed != 0 {
		t.Fatalf("duplicate replaced observed zero: %d, %v", elapsed, ok)
	}
	if _, ok := row.ServerTiming.Elapsed(ServerAuthDone); ok {
		t.Fatal("source mutation leaked through event value")
	}
	if _, ok := assigned.ServerTiming.Elapsed(ServerBodyDecodeDone); ok {
		t.Fatal("later fold changed assigned snapshot")
	}
	if elapsed, ok := row.ServerTiming.Elapsed(ServerResponseWriteDone); !ok || elapsed != 3*time.Second {
		t.Fatalf("later event did not fill missing stage: %d, %v", elapsed, ok)
	}
	row.ServerTiming.Record(ServerReplyReceived, 4*time.Second)
	if _, ok := event.ServerTiming.Elapsed(ServerReplyReceived); ok {
		t.Fatal("row mutation leaked to event")
	}
}

func TestRequestTimingMarkersDoNotAllocate(t *testing.T) {
	origin := time.Now()
	var retained RequestTimings
	allocations := testing.AllocsPerRun(100, func() {
		var local RequestTimings
		local.Record(ServerAuthStart, 0)
		local.Record(ServerAuthStart, time.Second)
		local.Mark(ServerAuthDone, origin)
		retained.Merge(local)
	})
	if allocations != 0 {
		t.Fatalf("markers allocated %g objects", allocations)
	}
	if elapsed, present := retained.Elapsed(ServerAuthDone); !present || elapsed < 0 {
		t.Fatalf("monotonic mark missing: %d, %v", elapsed, present)
	}
	retained.Mark(ServerResponseFlushDone, time.Time{})
	if _, present := retained.Elapsed(ServerResponseFlushDone); present {
		t.Fatal("missing origin invented a reached stage")
	}
}
