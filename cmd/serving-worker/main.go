package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/lab-paper-code/chill/internal/firstfloor"
)

type options struct {
	configPath  string
	bindAddress string
	nodeName    string
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.configPath, "config", "", "Path to the complete serving configuration snapshot.")
	flags.StringVar(&o.bindAddress, "bind-address", "127.0.0.1:8080", "Local HTTP listener address.")
	flags.StringVar(&o.nodeName, "node-name", os.Getenv("NODE_NAME"), "Assigned Node name; defaults to NODE_NAME.")
}

func (o options) validate() error {
	if o.configPath == "" || o.bindAddress == "" {
		return errors.New("config path and bind address are required")
	}
	if o.nodeName == "" || strings.TrimSpace(o.nodeName) != o.nodeName {
		return errors.New("node name is required without surrounding whitespace; set NODE_NAME or --node-name")
	}
	return nil
}

func main() {
	opts := options{}
	opts.bindFlags(flag.CommandLine)
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts); err != nil {
		slog.Error("serving worker stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts options) error {
	if err := opts.validate(); err != nil {
		return err
	}
	slog.SetDefault(slog.Default().With("node", opts.nodeName))
	cfg, err := firstfloor.ReadConfig(opts.configPath)
	if err != nil {
		return fmt.Errorf("read configuration: %w", err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runner, err := firstfloor.StartRunner(workerCtx, cfg)
	if err != nil {
		return fmt.Errorf("start runner: %w", err)
	}
	scheduler := firstfloor.NewScheduler(workerCtx, cfg, runner)
	if cfg.Mode == "serving" {
		go firstfloor.WatchConfig(workerCtx, opts.configPath, scheduler)
	}
	server := &http.Server{Addr: opts.bindAddress, Handler: firstfloor.NewHandler(scheduler),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	slog.Info("serving worker started", "bind_address", opts.bindAddress, "model", cfg.Model,
		"profile", cfg.Policy.ProfileID, "pod", os.Getenv("POD_NAME"), "namespace", os.Getenv("POD_NAMESPACE"),
		"service_url", os.Getenv("SERVICE_URL"))
	select {
	case err = <-serverErr:
	case <-scheduler.Done():
		err = errors.New("runner stopped")
	case <-ctx.Done():
	}
	cancel()
	shutdownCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil {
		if closeErr := server.Close(); closeErr != nil {
			slog.Warn("close HTTP server after failed shutdown", "error", closeErr)
		}
		if err == nil {
			err = shutdownErr
		}
	}
	<-scheduler.Done()
	if ctx.Err() != nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
