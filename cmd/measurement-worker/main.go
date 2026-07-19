package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lab-paper-code/chill/internal/profiler/worker"
)

func main() {
	options := options{}
	options.bindFlags(flag.CommandLine)
	flag.Parse()
	if err := options.validate(); err != nil {
		log.Printf("invalid measurement-worker options: %v", err)
		os.Exit(2)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(rootCtx, options.attemptTimeout)
	defer cancel()
	if err := execute(ctx, options, os.Stdout, os.Stderr, productionDependencies()); err != nil {
		log.Printf("measurement worker failed: %s", diagnostic(err))
		os.Exit(exitCode(err))
	}
}

func diagnostic(err error) string {
	if errors.Is(err, worker.ErrEvidenceCommit) {
		return worker.ErrEvidenceCommit.Error()
	}
	return err.Error()
}

func exitCode(err error) int {
	switch {
	case errors.Is(err, worker.ErrExecutionContract):
		return 2
	case errors.Is(err, worker.ErrRuntimeMeasurement):
		return 3
	case errors.Is(err, worker.ErrPowerObservation):
		return 4
	case errors.Is(err, worker.ErrEvidenceCommit):
		return 5
	default:
		return 5
	}
}
