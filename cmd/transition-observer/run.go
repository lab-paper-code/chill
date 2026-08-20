package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lab-paper-code/chill/internal/evidencestore"
	"github.com/lab-paper-code/chill/internal/metricobserver"
	"github.com/lab-paper-code/chill/internal/profiling/transitioncost"
	transitionworker "github.com/lab-paper-code/chill/internal/profiling/transitioncost/worker"
)

const maximumReceiptBytes = 4 << 10

type components struct {
	power        transitionworker.PowerObserver
	powerRequest metricobserver.Request
	facts        transitionworker.FactObserver
	store        evidencestore.ImmutableWriter
}

type dependencies struct {
	load    func(string, int64) (attemptContract, error)
	build   func(options, attemptContract) (components, error)
	observe func(
		context.Context,
		transitionworker.PowerObserver,
		metricobserver.Request,
		transitionworker.FactObserver,
		func(),
	) (transitioncost.RawObservation, error)
	writeRaw func(
		context.Context,
		evidencestore.ImmutableWriter,
		evidencestore.ObjectLocation,
		transitioncost.FrozenContract,
		transitioncost.RawObservation,
		transitioncost.Policy,
	) (evidencestore.Receipt, transitioncost.Result, error)
}

func execute(ctx context.Context, opts options, deps dependencies) error {
	attempt, err := deps.load(opts.contractPath, opts.maxContractBytes)
	if err != nil {
		return err
	}
	parts, err := deps.build(opts, attempt)
	if err != nil {
		return err
	}

	observeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := false
	var readyErr error
	raw, observeErr := deps.observe(observeCtx, parts.power, parts.powerRequest, parts.facts, func() {
		readyErr = publishReady(opts.readyPath)
		if readyErr == nil {
			ready = true
		} else {
			cancel()
		}
	})
	if readyErr != nil {
		return fmt.Errorf("publish transition observation ready: %w", readyErr)
	}
	if !ready {
		if observeErr != nil {
			return observeErr
		}
		return errors.New("transition observation ended before ready")
	}
	receipt, _, writeErr := deps.writeRaw(
		ctx, parts.store, attempt.rawLocation, attempt.frozen, raw, attempt.policy,
	)
	if writeErr != nil {
		return errors.Join(observeErr, writeErr)
	}
	if observeErr != nil {
		return observeErr
	}
	return writeReceipt(opts.terminationPath, receipt)
}

func publishReady(path string) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".transition-ready-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := file.Close(); err != nil {
		return err
	}
	return os.Link(temporary, path)
}

func writeReceipt(path string, receipt evidencestore.Receipt) error {
	if err := receipt.Validate(); err != nil {
		return err
	}
	payload, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	if len(payload) > maximumReceiptBytes {
		return errors.New("receipt exceeds Kubernetes termination-message limit")
	}
	return os.WriteFile(path, payload, 0o600)
}
