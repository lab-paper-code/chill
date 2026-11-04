package main

import (
	"context"
	"errors"
	"flag"
	"testing"
	"time"
)

func TestArchiveCancellationJoinsCleanup(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(map[bool]string{false: "signal", true: "shutdown-budget"}[expire], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- runArchive(ctx, options{shutdownTimeout: time.Second},
					func(ctx context.Context, _ time.Duration, _ func(error)) error {
						close(entered)
						<-ctx.Done()
						close(canceled)
						<-release // Represents producer/Abort cleanup before flock release.
						return ctx.Err()
					})
			}()
			<-entered
			cancel()
			select {
			case <-canceled:
			case <-time.After(time.Second):
				close(release)
				<-done
				t.Fatal("composition root joined before canceling worker")
			}
			if expire {
				select {
				case err := <-done:
					close(release)
					t.Fatalf("shutdown budget released a still-running worker: %v", err)
				case <-time.After(2 * time.Second):
				}
			} else {
				select {
				case err := <-done:
					close(release)
					t.Fatalf("root returned before cleanup joined: %v", err)
				default:
				}
			}
			close(release)
			select {
			case err := <-done:
				if expire && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("missing shutdown-budget error: %v", err)
				}
				if !expire && err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("composition root did not join completed cleanup")
			}
		})
	}
}

func TestArchiveRunnerErrorReturnsWithoutSignal(t *testing.T) {
	want := errors.New("worker initialization failed")
	ctx := context.Background()
	err := runArchive(ctx, options{shutdownTimeout: time.Second},
		func(context.Context, time.Duration, func(error)) error { return want })
	if !errors.Is(err, want) || ctx.Err() != nil {
		t.Fatalf("runner error=%v parent=%v", err, ctx.Err())
	}
}

func TestArchiveOptionsUseExistingBudgets(t *testing.T) {
	var opts options
	flags := flag.NewFlagSet("observation-archiver", flag.ContinueOnError)
	opts.bindFlags(flags)
	if err := flags.Parse([]string{
		"--observation-directory=/observations", "--archive-control-directory=/archive-control",
		"--store-name=evidence", "--store-endpoint=https://store", "--store-bucket=bucket", "--store-region=region",
		"--request-timeout=129s", "--shutdown-timeout=194s",
	}); err != nil {
		t.Fatal(err)
	}
	if err := opts.validate(); err != nil {
		t.Fatal(err)
	}
	opts.shutdownTimeout = opts.store.RequestTimeout
	if err := opts.validate(); err == nil {
		t.Fatal("shutdown budget did not leave time for multipart abort and join")
	}
	opts.shutdownTimeout = 194 * time.Second
	opts.controlDirectory = "relative"
	if err := opts.validate(); err == nil {
		t.Fatal("relative control mount accepted")
	}
}
