package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// A slow hourly upload runs beside the serve loop: each tick returns at once,
// and a tick during the upload starts no second one.
func TestArchiveInBackgroundRunsOneFlushAtATime(t *testing.T) {
	running := make(chan struct{}, 1)
	release := make(chan struct{})
	var started atomic.Int32
	flush := func(ctx context.Context) error {
		started.Add(1)
		<-release
		return ctx.Err()
	}
	for range 3 {
		ticked := make(chan struct{})
		go func() {
			archiveInBackground(context.Background(), running, time.Minute, flush)
			close(ticked)
		}()
		select {
		case <-ticked:
		case <-time.After(time.Second):
			t.Fatal("serve loop blocked on a running Flush")
		}
	}
	close(release)
	running <- struct{}{} // The serve loop waits here for the running Flush.
	if got := started.Load(); got != 1 {
		t.Fatalf("started %d Flushes, want 1", got)
	}
	<-running
	archiveInBackground(context.Background(), running, time.Minute, flush)
	running <- struct{}{}
	if got := started.Load(); got != 2 {
		t.Fatalf("Flush after the previous one finished: started %d, want 2", got)
	}
}
