package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	consolekube "github.com/lab-paper-code/chill/internal/operatorconsole/kube"
	"github.com/lab-paper-code/chill/internal/operatorconsole/readapi"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
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
) (consolekube.VerifiedEvidenceView, error) {
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
	query, err := consolekube.NewKubernetesEvidencePassportQuery(
		reader,
		consoleEvidenceLoader{retained: loader},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Passport query: %w", err)
	}
	resolver, err := profilercontroller.NewKubernetesEvidenceStoreResolver(
		reader,
		profilercontroller.EvidenceStoreResolverOptions{
			SystemName: opts.systemName, Namespace: opts.namespace,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Store resolver: %w", err)
	}
	results, err := consolekube.NewKubernetesBatchSaturationResultQuery(
		reader,
		campaignResultObjectReader{
			reader: reader, resolver: resolver,
			namespace: opts.namespace, tempDir: opts.tempDir,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct batch-saturation result query: %w", err)
	}
	handler, err := readapi.NewHandler(query, results)
	if err != nil {
		return nil, fmt.Errorf("construct Console HTTP handler: %w", err)
	}
	return handler, nil
}

// campaignResultObjectReader opens one stored object using the Evidence Store
// binding the owning ChillSystem currently declares. A campaign result is not
// frozen into a measurement Job the way Run evidence is, so the binding is
// resolved per read rather than recovered from a Job.
type campaignResultObjectReader struct {
	reader    client.Reader
	resolver  *profilercontroller.KubernetesEvidenceStoreResolver
	namespace string
	tempDir   string
}

func (r campaignResultObjectReader) Open(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	binding, err := r.resolver.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve Evidence Store binding: %w", err)
	}
	credentials, err := storekube.LoadCredentials(ctx, r.reader, r.namespace, binding.CredentialsSecretName)
	if err != nil {
		return nil, err
	}
	store, err := evidences3.New(evidences3.Config{
		LogicalStore:    binding.Store,
		Bucket:          binding.Bucket,
		Endpoint:        binding.Endpoint,
		Region:          binding.Region,
		AccessKeyID:     credentials.AccessKeyID,
		SecretAccessKey: credentials.SecretAccessKey,
		TempDir:         r.tempDir,
	})
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Store reader: %w", err)
	}
	return store.Open(ctx, identity)
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
