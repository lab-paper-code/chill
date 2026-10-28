package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

func TestLiveOnlyPublishesRecordedDecisionAndActionIdentity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := newLivePublisher(ctx)
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	service := &schedulerService{observationLog: log, live: p}
	at := time.Now().UTC()
	d := serving.SchedulerDecision{At: at, Status: "Ready"}
	if err := service.recordDecision(d, serving.SchedulerSnapshot{At: at}); err != nil {
		t.Fatal(err)
	}
	result := servingkube.OperationResult{
		Operation: servingkube.Operation{ID: "same", Kind: "prepare", Node: serving.NodeReference{UID: "node"}},
		StartedAt: at, Status: "running",
	}
	p.action(result)
	result.StartedAt = at.Add(time.Second)
	p.action(result)
	sub, err := p.hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	frame, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var baseline struct {
		Type     string         `json:"type"`
		Decision *live.Decision `json:"decision"`
		Actions  []live.Action  `json:"actions"`
	}
	if err := json.Unmarshal(frame, &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.Type != "snapshot" || baseline.Decision == nil ||
		baseline.Decision.At != liveAt(at) || len(baseline.Actions) != 2 {
		t.Fatalf("lost recorded decision or reused action ID: %s", frame)
	}
	if err := log.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.recordDecision(
		serving.SchedulerDecision{At: at.Add(time.Minute)}, serving.SchedulerSnapshot{},
	); err == nil {
		t.Fatal("closed observation store accepted a decision")
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if frame, err := sub.Next(short); err == nil {
		t.Fatalf("unrecorded decision was emitted: %s", frame)
	}
}
