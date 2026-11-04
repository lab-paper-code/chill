package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/serving/observation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

type options struct {
	directory, controlDirectory string
	store                       stores3.Config
	shutdownTimeout             time.Duration
}

func (o *options) bindFlags(f *flag.FlagSet) {
	f.StringVar(&o.directory, "observation-directory", "", "Private observation owner root, mounted read-only.")
	f.StringVar(&o.controlDirectory, "archive-control-directory", "",
		"Serving-created archive-control directory, mounted read-write.")
	f.StringVar(&o.store.LogicalStore, "store-name", "", "Logical Evidence Store name.")
	f.StringVar(&o.store.Endpoint, "store-endpoint", "", "S3-compatible Evidence Store endpoint.")
	f.StringVar(&o.store.Bucket, "store-bucket", "", "Evidence Store bucket.")
	f.StringVar(&o.store.Region, "store-region", "", "Evidence Store region.")
	f.DurationVar(&o.store.RequestTimeout, "request-timeout", 30*time.Second,
		"Bound of each multipart SDK request, not the whole upload.")
	f.DurationVar(&o.shutdownTimeout, "shutdown-timeout", 0,
		"Required process cancel/join budget from deployment configuration.")
}

func (o options) validate() error {
	for _, path := range []string{o.directory, o.controlDirectory} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("observation and archive-control directories must be clean absolute paths")
		}
	}
	if o.store.RequestTimeout <= 0 || o.shutdownTimeout <= 0 {
		return errors.New("request-timeout and shutdown-timeout must be positive")
	}
	if o.store.RequestTimeout >= o.shutdownTimeout {
		return errors.New("shutdown-timeout must exceed request-timeout to allow bounded multipart abort and join")
	}
	return nil
}

func main() {
	var opts options
	opts.bindFlags(flag.CommandLine)
	zapOptions := zap.Options{Development: true}
	zapOptions.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOptions)))
	if flag.NArg() != 0 {
		ctrl.Log.Error(errors.New("unexpected positional arguments"), "invalid observation archiver options")
		os.Exit(1)
	}
	if err := run(ctrl.SetupSignalHandler(), opts); err != nil {
		ctrl.Log.Error(err, "run observation archiver")
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if err := opts.validate(); err != nil {
		return err
	}
	config := opts.store
	config.AccessKeyID = os.Getenv("AWS_ACCESS_KEY_ID")
	config.SecretAccessKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	config.SessionToken = os.Getenv("AWS_SESSION_TOKEN")
	objects, err := stores3.New(config)
	if err != nil {
		return err
	}
	worker, err := observation.NewArchiveWorker(observation.ArchiveWorkerConfig{
		Directory: opts.directory, ControlDirectory: opts.controlDirectory,
		Store: config.LogicalStore, Writer: objects,
	})
	if err != nil {
		return err
	}
	return runArchive(ctx, opts, worker.Run)
}

// The runner owns its flock and upload inputs until it returns. Cancellation
// must precede the join even when a signal arrives during the initial scan.
func runArchive(ctx context.Context, opts options,
	run func(context.Context, time.Duration, func(error)) error,
) error {
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(lifetime, observation.MaintenanceInterval, func(err error) {
			ctrl.Log.Error(err, "publish sealed observation hours")
		})
	}()
	select {
	case err := <-done:
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	cancel()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), opts.shutdownTimeout)
	defer cancelShutdown()
	select {
	case err := <-done:
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	case <-shutdown.Done():
		// A context cannot interrupt arbitrary local filesystem I/O. Do not
		// release dependencies or claim a clean shutdown while it still runs.
		err := fmt.Errorf("observation archiver cancel/join exceeded shutdown-timeout: %w", shutdown.Err())
		ctrl.Log.Error(err, "waiting for archive worker to release its inputs and lock")
		return errors.Join(err, <-done)
	}
}
