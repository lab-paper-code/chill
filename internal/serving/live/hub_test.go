package live

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testMutated = "mutated"

var testAt = time.Date(2026, 10, 25, 12, 0, 0, 123, time.UTC)

type testEnvelope struct {
	Type string `json:"type"`
	Seq  uint64 `json:"seq"`
	At   string `json:"at"`
	Snapshot
}

func nextEnvelope(t *testing.T, sub *Subscription) testEnvelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	data, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var message testEnvelope
	if err := json.Unmarshal(data, &message); err != nil {
		t.Fatal(err)
	}
	return message
}

func TestCheckpointResumeAndOriginalSnapshotTime(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	hello, err := hub.Hello(testAt.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var greeting struct {
		Type, Protocol, At, ServerAt string
		Seq, RetainFrom              uint64
	}
	if err := json.Unmarshal(hello, &greeting); err != nil {
		t.Fatal(err)
	}
	if greeting.Type != "hello" || greeting.Protocol != Protocol || greeting.Seq != 0 || greeting.RetainFrom != 1 || greeting.At != timestamp(testAt.Add(time.Hour)) || greeting.ServerAt != greeting.At {
		t.Fatalf("hello checkpoint: %+v", greeting)
	}
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	baseline := nextEnvelope(t, sub)
	if baseline.Type != messageSnapshot || baseline.Seq != 0 || baseline.At != timestamp(testAt) || baseline.Workloads == nil || baseline.Routing == nil || baseline.Instances == nil || baseline.Actions == nil {
		t.Fatalf("initial baseline: %+v", baseline)
	}
	if err := hub.Heartbeat(testAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	checkpoint := nextEnvelope(t, sub)
	if err := hub.Heartbeat(testAt.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	valid, err := hub.Subscribe(checkpoint.Seq, checkpoint.At)
	if err != nil {
		t.Fatal(err)
	}
	defer valid.Close()
	if message := nextEnvelope(t, valid); message.Type != "heartbeat" || message.Seq != 2 {
		t.Fatalf("valid replay: %+v", message)
	}
	for _, from := range []uint64{0, checkpoint.Seq, 100} {
		invalid, err := hub.Subscribe(from, timestamp(testAt))
		if err != nil {
			t.Fatal(err)
		}
		message := nextEnvelope(t, invalid)
		invalid.Close()
		if message.Type != messageSnapshot || message.Seq != 2 || message.At != timestamp(testAt.Add(2*time.Second)) {
			t.Fatalf("mismatched checkpoint: %+v", message)
		}
	}
}

func TestDeltaReplacementUpsertRemovalAndInputOwnership(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	workloads := []Workload{{UID: "one"}, {UID: "two"}}
	instances := []Instance{{ID: "a", Running: map[string]int{"one": 1}}, {ID: "b"}}
	actions := []Action{{ID: "same", Node: NodeRef{UID: "n"}, StartedAt: "first"}, {ID: "same", Node: NodeRef{UID: "n"}, StartedAt: "second"}}
	if err := hub.Publish(testAt, Delta{Workloads: &workloads, Instances: &instances, Actions: &actions}, nil); err != nil {
		t.Fatal(err)
	}
	instances[0].Running["one"] = 99
	workloads = []Workload{}
	instances = []Instance{{ID: "a", Ready: true, Running: map[string]int{"one": 2}}}
	decision := &Decision{At: timestamp(testAt), State: "keep"}
	event := &Event{Kind: "decision", Decision: decision}
	if err := hub.Publish(testAt.Add(time.Second), Delta{Workloads: &workloads, Instances: &instances, Decision: &decision, Removed: &Removed{Actions: []string{actions[0].Key()}}}, event); err != nil {
		t.Fatal(err)
	}
	decision.State = testMutated
	instances[0].Running["one"] = 88
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	baseline := nextEnvelope(t, sub)
	if len(baseline.Workloads) != 0 || baseline.Workloads == nil || len(baseline.Instances) != 2 || baseline.Instances[0].Running["one"] != 2 || len(baseline.Actions) != 1 || baseline.Actions[0].StartedAt != "second" || baseline.Decision.State != "keep" {
		t.Fatalf("delta semantics/ownership: %+v", baseline)
	}
	var cleared *Decision
	if err := hub.Publish(testAt, Delta{Decision: &cleared}, nil); err != nil {
		t.Fatal(err)
	}
	message := nextEnvelope(t, sub)
	if message.Type != "delta" || message.Decision != nil {
		t.Fatalf("decision clear: %+v", message)
	}
	if err := hub.Publish(testAt, Delta{Removed: &Removed{Instances: []string{"a", "b"}, Actions: []string{actions[1].Key()}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := sub.Resume(0, ""); err != nil {
		t.Fatal(err)
	}
	if message := nextEnvelope(t, sub); len(message.Instances) != 0 || len(message.Actions) != 0 {
		t.Fatalf("explicit removal left identities: %+v", message)
	}
}

func TestRetentionAndSafeSequenceBounds(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	for i := range RetainedMessages + 2 {
		if err := hub.Heartbeat(testAt.Add(time.Duration(i) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if hub.count != RetainedMessages || hub.ring[hub.start].seq != 3 {
		t.Fatalf("ring count/start: %d/%d", hub.count, hub.ring[hub.start].seq)
	}
	tooOld, err := hub.Subscribe(1, timestamp(testAt))
	if err != nil {
		t.Fatal(err)
	}
	if message := nextEnvelope(t, tooOld); message.Type != messageSnapshot {
		t.Fatal("evicted checkpoint replayed")
	}
	tooOld.Close()
	hub.head = MaxSeq - 1
	decision := &Decision{}
	if err := hub.Publish(testAt, Delta{Decision: &decision}, &Event{Kind: "decision", Decision: decision}); !errors.Is(err, ErrSeqExhausted) {
		t.Fatalf("atomic seq reserve: %v", err)
	}
	if hub.head != MaxSeq-1 {
		t.Fatal("failed publish consumed sequence")
	}
	if err := hub.Heartbeat(testAt); err != nil {
		t.Fatal(err)
	}
	if err := hub.Heartbeat(testAt); !errors.Is(err, ErrSeqExhausted) {
		t.Fatalf("unsafe sequence accepted: %v", err)
	}
}

func TestFrameRingBytesAndOversizedSnapshot(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	row := []Workload{{UID: "one", Name: strings.Repeat("x", 700<<10)}}
	for i := range 20 {
		row[0].UID = string(rune('a' + i))
		if err := hub.Publish(testAt, Delta{Workloads: &row}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if hub.bytes > RetainedBytes || hub.count >= 20 {
		t.Fatalf("byte retention not enforced: %d/%d", hub.bytes, hub.count)
	}
	tooLarge := []Workload{{Name: strings.Repeat("x", MaxFrameBytes)}}
	if err := hub.Publish(testAt, Delta{Workloads: &tooLarge}, nil); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized delta: %v", err)
	}
	// Small row-upserts can produce a baseline larger than a single frame.
	instances := []Instance{{ID: "a", ProfileID: strings.Repeat("x", 600<<10)}}
	if err := hub.Publish(testAt, Delta{Workloads: &[]Workload{}, Instances: &instances}, nil); err != nil {
		t.Fatal(err)
	}
	instances[0].ID = "b"
	if err := hub.Publish(testAt, Delta{Instances: &instances}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.Subscribe(0, ""); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversized baseline truncated: %v", err)
	}
}

func TestSlowQueueBarrierBeforeLaterStateAndReplay(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	_ = nextEnvelope(t, sub)
	if err := hub.Heartbeat(testAt); err != nil {
		t.Fatal(err)
	}
	checkpoint := nextEnvelope(t, sub)
	metric := MetricsFrame{BucketAt: timestamp(testAt), Gaps: []Gap{{Reason: strings.Repeat("x", 800<<10)}}}
	for range 4 {
		if err := hub.PublishMetrics(testAt, metric); err != nil {
			t.Fatal(err)
		}
	}
	instances := []Instance{{ID: "after"}}
	if err := hub.Publish(testAt, Delta{Instances: &instances}, nil); err != nil {
		t.Fatal(err)
	}
	if sub.bytes > QueueBytes || len(sub.pending) > QueueMessages {
		t.Fatal("queue exceeds bound")
	}
	barrier := nextEnvelope(t, sub)
	if barrier.Type != messageSnapshot || barrier.Seq <= checkpoint.Seq {
		t.Fatalf("missing slow-consumer baseline barrier: %+v", barrier)
	}
	// Replay itself may exceed the per-client byte bound. Its head barrier must
	// not be followed by any older replay frame.
	replay, err := hub.Subscribe(checkpoint.Seq, checkpoint.At)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	last := uint64(0)
	for len(replay.pending) > 0 {
		message := nextEnvelope(t, replay)
		if message.Seq <= last {
			t.Fatalf("replay moved backwards: %d after %d", message.Seq, last)
		}
		last = message.Seq
	}
	if last != hub.head {
		t.Fatalf("replay did not reach head: %d/%d", last, hub.head)
	}
}

func TestSnapshotRegistrationHasNoPublishWindow(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	var producers sync.WaitGroup
	producers.Add(1)
	go func() {
		defer producers.Done()
		for range 100 {
			if err := hub.Heartbeat(testAt); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	baseline := nextEnvelope(t, sub)
	if baseline.Type != messageSnapshot {
		t.Fatal("delta before baseline")
	}
	producers.Wait()
	last := baseline.Seq
	for last < 100 {
		message := nextEnvelope(t, sub)
		if message.Seq != last+1 {
			t.Fatalf("subscription lost publication %d: %+v", last+1, message)
		}
		last = message.Seq
	}
}

func TestHubRunShutdownReleasesSubscribers(t *testing.T) {
	hub := New(testAt)
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := hub.Run(ctx, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-hub.Done():
	default:
		t.Fatal("hub shutdown notification missing")
	}
	if _, err := sub.Next(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("subscriber retained after shutdown: %v", err)
	}
	if _, err := hub.Subscribe(0, ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed hub accepted subscriber: %v", err)
	}
}

func TestProjectionCollectionsCopyOwnershipAndReplaceReservations(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	at := timestamp(testAt)
	nodes := []Node{{UID: "node", Ready: false, Measurement: true, Reasons: []string{"Unavailable"}, ObservedAt: &at}}
	instances := []Instance{{ID: "shared", Waiting: map[string]int{"a": 2, "b": 1}}}
	reservations := []Reservation{{Owner: "operation", Nodes: []NodeRef{{UID: "node"}},
		WorkloadUIDs: []string{"a", "b"}, PodUIDs: []string{"pod"}, Instances: []string{"shared"}, Explanation: "이전 변경이 자원을 예약하고 있어요."}}
	if err := hub.Publish(testAt, Delta{Nodes: &nodes, Instances: &instances, Reservations: &reservations}, nil); err != nil {
		t.Fatal(err)
	}
	nodes[0].Reasons[0], at = testMutated, testMutated
	instances[0].Waiting["a"] = 99
	reservations[0].Nodes[0].UID = testMutated
	reservations[0].WorkloadUIDs[0] = testMutated
	reservations[0].PodUIDs[0] = testMutated
	reservations[0].Instances[0] = testMutated
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	frame := nextEnvelope(t, sub)
	if frame.Nodes[0].Reasons[0] != "Unavailable" || *frame.Nodes[0].ObservedAt != timestamp(testAt) ||
		frame.Instances[0].Waiting["a"] != 2 || frame.Reservations[0].Nodes[0].UID != "node" ||
		frame.Reservations[0].WorkloadUIDs[0] != "a" || frame.Reservations[0].PodUIDs[0] != "pod" ||
		frame.Reservations[0].Instances[0] != "shared" {
		t.Fatalf("producer retained mutable ownership: %+v", frame)
	}
	emptyReservations := []Reservation{}
	if err := hub.Publish(testAt, Delta{Reservations: &emptyReservations, Removed: &Removed{Instances: []string{"shared"}}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := sub.Resume(0, ""); err != nil {
		t.Fatal(err)
	}
	if frame = nextEnvelope(t, sub); frame.Reservations == nil || len(frame.Reservations) != 0 {
		t.Fatalf("delta failed to clear reservations: %+v", frame)
	}
}

func TestResumeSnapshotReportsRetainedHistoryAndGaps(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	if err := hub.Publish(testAt, Delta{}, &Event{Kind: "cluster", Cluster: &ClusterEvent{Kind: "issue", Reason: "fact"}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishMetrics(testAt.Add(time.Second), MetricsFrame{BucketAt: timestamp(testAt)}); err != nil {
		t.Fatal(err)
	}
	if err := hub.Heartbeat(testAt.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	fresh := readResumeHistory(t, hub, 0, "")
	if fresh.Reason != "fresh" || fresh.Replayed || fresh.Requested.AfterSeq != 0 || fresh.Requested.ThroughSeq != 3 ||
		fresh.Retained.Events == nil || *fresh.Retained.Events != (SequenceRange{FromSeq: 1, ToSeq: 1}) ||
		fresh.Retained.Power == nil || *fresh.Retained.Power != (SequenceRange{FromSeq: 2, ToSeq: 2}) {
		t.Fatalf("fresh retained ranges: %+v", fresh)
	}
	want := []HistoryGap{
		{SequenceRange: SequenceRange{FromSeq: 2, ToSeq: 3}, Scope: "events", Reason: "NoRetainedFrames"},
		{SequenceRange: SequenceRange{FromSeq: 1, ToSeq: 1}, Scope: "power", Reason: "NoRetainedFrames"},
		{SequenceRange: SequenceRange{FromSeq: 3, ToSeq: 3}, Scope: "power", Reason: "NoRetainedFrames"},
	}
	if !reflect.DeepEqual(fresh.Gaps, want) {
		t.Fatalf("fresh gap complement: %+v", fresh.Gaps)
	}
	unmatched := readResumeHistory(t, hub, 1, "not-the-checkpoint")
	if unmatched.Reason != "checkpoint-unmatched" || unmatched.Requested.AfterSeq != 1 || unmatched.Requested.At != "not-the-checkpoint" ||
		len(unmatched.Gaps) != 2 || unmatched.Gaps[0] != want[0] || unmatched.Gaps[1] != want[2] {
		t.Fatalf("unmatched checkpoint interval: %+v", unmatched)
	}
	// Metadata never turns a matched resume into a snapshot or old history seed.
	matched, err := hub.Subscribe(1, timestamp(testAt))
	if err != nil {
		t.Fatal(err)
	}
	defer matched.Close()
	if message := nextEnvelope(t, matched); message.Type != "metrics" || message.Seq != 2 {
		t.Fatalf("matched replay changed: %+v", message)
	}
}

func TestEvictedAndFutureResumeHistory(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	if err := hub.Publish(testAt, Delta{}, &Event{Kind: "cluster", Cluster: &ClusterEvent{Kind: "issue"}}); err != nil {
		t.Fatal(err)
	}
	if err := hub.PublishMetrics(testAt.Add(time.Second), MetricsFrame{BucketAt: timestamp(testAt)}); err != nil {
		t.Fatal(err)
	}
	for i := range RetainedMessages {
		if err := hub.Heartbeat(testAt.Add(time.Duration(i+3) * time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	evicted := readResumeHistory(t, hub, 1, timestamp(testAt))
	if evicted.Retained.Events != nil || evicted.Retained.Power != nil || len(evicted.Gaps) != 2 ||
		evicted.Gaps[0].FromSeq != 2 || evicted.Gaps[0].ToSeq != hub.head || evicted.Gaps[1].SequenceRange != evicted.Gaps[0].SequenceRange {
		t.Fatalf("evicted history falsely retained: %+v", evicted)
	}
	future := readResumeHistory(t, hub, hub.head+1, timestamp(testAt))
	if future.Reason != "checkpoint-unmatched" || len(future.Gaps) != 0 {
		t.Fatalf("future checkpoint invented reversed gap: %+v", future)
	}
}

func TestEmptyResumeHistoryKeepsNullRangesAndEmptyGaps(t *testing.T) {
	hub := New(testAt)
	defer hub.Close()
	sub, err := hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	data, err := sub.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		History ResumeHistory `json:"history"`
	}
	if err := json.Unmarshal(data, &message); err != nil || message.History.Retained.Events != nil || message.History.Retained.Power != nil || message.History.Gaps == nil || len(message.History.Gaps) != 0 {
		t.Fatalf("empty history: %s %v", data, err)
	}
}

func readResumeHistory(t *testing.T, hub *Hub, from uint64, at string) ResumeHistory {
	t.Helper()
	sub, err := hub.Subscribe(from, at)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	data, err := sub.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var message struct {
		testEnvelope
		History *ResumeHistory `json:"history"`
	}
	if err := json.Unmarshal(data, &message); err != nil || message.Type != messageSnapshot || message.Seq != hub.head || message.History == nil {
		t.Fatalf("resume metadata: %s %v", data, err)
	}
	return *message.History
}
