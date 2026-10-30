package main

import (
	"context"
	"encoding/json"
	"flag"
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/serving"
	"github.com/lab-paper-code/chill/internal/serving/live"
)

// SCH-AC-54: startup identity is recorded before projection; failed appends emit nothing.
func TestClusterPublishesOnlyCommittedRows(t *testing.T) {
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
	sub, err := p.hub.Subscribe(0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, err = sub.Next(ctx); err != nil {
		t.Fatal(err)
	}
	started := time.Unix(123, 456).UTC()
	row := serverStartCluster("self-uid", "system", "server", started, options{
		queueLimit: 1024, sourceInterval: 2 * time.Second, profileInterval: time.Minute,
	})
	if row.At != started || row.QueueLimit == nil || *row.QueueLimit != 1024 ||
		row.SourceInterval == nil || *row.SourceInterval != 2*time.Second ||
		row.ProfileInterval == nil || *row.ProfileInterval != time.Minute {
		t.Fatalf("startup selection lost: %+v", row)
	}
	if err = recordCluster(log, p, row); err != nil {
		t.Fatal(err)
	}
	frame, err := sub.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Type string `json:"type"`
		live.Event
	}
	if err = json.Unmarshal(frame, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != liveTestEvent || event.Kind != "cluster" || event.Cluster == nil ||
		event.Cluster.Event != "server-start" || event.Cluster.Pod.UID != "self-uid" {
		t.Fatalf("wrong committed projection: %s", frame)
	}
	if err = log.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err = recordCluster(log, p, row); err == nil {
		t.Fatal("closed writer accepted cluster row")
	}
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if frame, err = sub.Next(short); err == nil {
		t.Fatalf("uncommitted row emitted: %s", frame)
	}
}

func TestServerStartFreezesSelectedMeasurementConfiguration(t *testing.T) {
	for _, args := range [][]string{nil, {"--queue-limit=27", "--source-interval=2s", "--profile-interval=3m"}} {
		var opts options
		flags := flag.NewFlagSet("server-start", flag.ContinueOnError)
		opts.bindFlags(flags)
		if err := flags.Parse(args); err != nil {
			t.Fatal(err)
		}
		started := time.Now().UTC()
		row := serverStartCluster("uid", "namespace", "server", started, opts)
		wantQueue, wantSource, wantProfile := 1024, time.Second, time.Minute
		if len(args) > 0 {
			wantQueue, wantSource, wantProfile = 27, 2*time.Second, 3*time.Minute
		}
		if row.At != started || *row.QueueLimit != wantQueue || *row.SourceInterval != wantSource ||
			*row.ProfileInterval != wantProfile {
			t.Fatalf("startup record did not freeze selected config: %+v", row)
		}
	}
}
