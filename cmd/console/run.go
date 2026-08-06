package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	evidencekube "github.com/lab-paper-code/chill/internal/profiler/evidence/kube"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type consoleEvidenceLoader struct {
	retained *evidencekube.RetainedLoader
}

func (l consoleEvidenceLoader) Load(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (operatorconsole.VerifiedEvidenceView, error) {
	return l.retained.Load(ctx, run, reference)
}

func buildHandler(reader client.Reader, opts options) (http.Handler, error) {
	loader, err := evidencekube.NewRetainedLoader(
		reader,
		func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
			return evidences3.New(evidences3.Config{
				LogicalStore:    binding.Store,
				Bucket:          binding.Bucket,
				Endpoint:        binding.Endpoint,
				Region:          binding.Region,
				AccessKeyID:     credentials.AccessKeyID,
				SecretAccessKey: credentials.SecretAccessKey,
				TempDir:         opts.tempDir,
			})
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct retained evidence loader: %w", err)
	}
	query, err := operatorconsole.NewKubernetesEvidencePassportQuery(
		reader,
		consoleEvidenceLoader{retained: loader},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Passport query: %w", err)
	}
	handler, err := operatorconsole.NewHTTPHandler(query)
	if err != nil {
		return nil, fmt.Errorf("construct Console HTTP handler: %w", err)
	}
	return handler, nil
}

func serve(ctx context.Context, opts options, handler http.Handler) error {
	server := &http.Server{
		Addr: opts.bindAddress, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
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
			return fmt.Errorf("shut down Console API: %w", err)
		}
		return nil
	}
}
