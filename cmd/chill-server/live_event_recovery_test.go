package main

import (
	"context"
	"encoding/json"
	servingstate "github.com/lab-paper-code/chill/internal/serving/state"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/application"

	"github.com/lab-paper-code/chill/internal/serving"
)

func TestRecordedActionUsesSameDurableAndLiveTimestamp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	publisher := newLivePublisher(ctx)
	log, err := serving.NewObservationLog(serving.ObservationLogConfig{
		Directory: t.TempDir(), Store: "evidence", Retention: 2 * time.Hour,
		Writer: schedulerPowerArchive{directory: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := log.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	subscription, err := publisher.hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	if _, err := subscription.Next(ctx); err != nil {
		t.Fatal(err)
	}
	service := application.NewSchedulerService(context.Background(), application.Dependencies{
		ObservationLog: log, Live: &application.Live{Decision: publisher.decision, Action: publisher.action},
	})
	result := servingstate.OperationResult{
		Operation: servingstate.Operation{
			ID: "operation", Kind: "power", Node: serving.NodeReference{UID: "node"},
		},
		StartedAt: time.Now().UTC(), Status: "running",
	}
	if err := service.RecordAction(result); err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	for {
		raw, err := subscription.Next(deadline)
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type string    `json:"type"`
			At   time.Time `json:"at"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "event" {
			continue
		}
		records, err := log.EventObservations(ctx, event.At, 10)
		if err != nil || len(records) != 1 || records[0].Action == nil || records[0].Action.ID != result.Operation.ID {
			t.Fatalf("published event has no exact durable identity: %+v err=%v", records, err)
		}
		break
	}
}
