package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lab-paper-code/chill/internal/serving/entrypoint"
)

func buildHandler(routes entrypoint.Routes, opts options) http.Handler {
	return &entrypoint.Handler{
		Routes:    routes,
		Namespace: opts.servingNamespace,
		Port:      opts.servingPort,
	}
}

func serve(ctx context.Context, opts options, handler http.Handler) error {
	// No response deadline is set: how long a consumer waits belongs to its SLO
	// class, and nothing carries one yet.
	server := &http.Server{
		Addr: opts.bindAddress, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shut down the inference entry point: %w", err)
		}
		return nil
	}
}
