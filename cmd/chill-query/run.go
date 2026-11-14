package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	servingkube "github.com/lab-paper-code/chill/internal/serving/kube"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	profilerapp "github.com/lab-paper-code/chill/internal/profiling/profiler"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	"github.com/lab-paper-code/chill/internal/queryapi"
	querykube "github.com/lab-paper-code/chill/internal/queryapi/kube"
	"github.com/lab-paper-code/chill/internal/queryapi/readapi"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type queryEvidenceLoader struct {
	retained *evidencekube.RetainedLoader
}

func (l queryEvidenceLoader) Load(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (querykube.VerifiedEvidenceView, error) {
	return l.retained.Load(ctx, run, reference)
}

func newRetainedEvidenceLoader(
	reader client.Reader,
	opts options,
) (*evidencekube.RetainedLoader, error) {
	return evidencekube.NewRetainedLoader(
		reader,
		func(binding storekube.Binding, credentials storekube.Credentials) (evidencestore.LocationReader, error) {
			return evidences3.New(evidences3.Config{
				LogicalStore: binding.Store, Bucket: binding.Bucket, Endpoint: binding.Endpoint, Region: binding.Region,
				AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey, TempDir: opts.tempDir,
			})
		},
	)
}

// newEvidencePassportQuery keeps passport reads behind retained-evidence verification authority.
func newEvidencePassportQuery(
	reader client.Reader,
	opts options,
) (*querykube.KubernetesEvidencePassportQuery, error) {
	loader, err := newRetainedEvidenceLoader(reader, opts)
	if err != nil {
		return nil, fmt.Errorf("construct retained evidence loader: %w", err)
	}
	query, err := querykube.NewKubernetesEvidencePassportQuery(
		reader,
		queryEvidenceLoader{retained: loader},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Passport query: %w", err)
	}
	return query, nil
}

// newCampaignResultQuery keeps result reads behind the declared Evidence Store binding authority.
func newCampaignResultQuery(
	reader client.Reader,
	opts options,
) (*querykube.KubernetesBatchEnergyOptimizationResultQuery, error) {
	resolver, err := profilerkube.NewKubernetesEvidenceStoreResolver(
		reader,
		profilerkube.EvidenceStoreResolverOptions{
			SystemName: opts.systemName, Namespace: opts.namespace,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Store resolver: %w", err)
	}
	objects := campaignResultObjectReader{
		reader: reader, resolver: resolver,
		namespace: opts.namespace, tempDir: opts.tempDir,
	}
	results, err := querykube.NewKubernetesBatchEnergyOptimizationResultQuery(reader, objects)
	if err != nil {
		return nil, fmt.Errorf("construct batch-energy-optimization result query: %w", err)
	}
	return results, nil
}

// newCampaignPreflightQuery keeps preflight reads behind the production profiling authorities.
func newCampaignPreflightQuery(
	reader client.Reader,
	opts options,
) (*querykube.KubernetesBatchEnergyOptimizationPreflightQuery, error) {
	nodes, err := profilerkube.NewKubernetesNodeResolver(reader)
	if err != nil {
		return nil, fmt.Errorf("construct Node resolver: %w", err)
	}
	preflights, err := querykube.NewKubernetesBatchEnergyOptimizationPreflightQuery(reader)
	if err != nil {
		return nil, fmt.Errorf("construct batch-energy-optimization preflight query: %w", err)
	}
	sessionCandidates, candidateErr := profilerkube.NewKubernetesSessionCandidateResolver(reader,
		profilerkube.CandidateResolverOptions{
			Namespace: opts.namespace, ConfigMapName: opts.factSourceConfigMap, ConfigMapKey: opts.executionFactsKey,
		})
	sessionWorkloads, workloadErr := profilerkube.NewKubernetesSessionWorkloadResolver(reader)
	sessionExecution, executionErr := profilerkube.NewKubernetesSessionExecutionPolicyResolver(reader,
		profilerkube.ExecutionPolicyResolverOptions{
			Namespace: opts.namespace, ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey: opts.executionPoliciesKey, MeasurementMemoryFraction: opts.measurementMemoryFraction,
		})
	if err := errors.Join(candidateErr, workloadErr, executionErr); err != nil {
		return nil, fmt.Errorf("construct session resolvers: %w", err)
	}
	sessionPreparer, err := profilerapp.NewSessionPreparer(sessionCandidates, nodes, sessionWorkloads, sessionExecution)
	if err != nil {
		return nil, fmt.Errorf("construct session preparer: %w", err)
	}
	// The same call and request ChillServer's session runner prepares with.
	preflights.Session = func(
		ctx context.Context, campaign *edgev1alpha1.BatchEnergyOptimization,
	) (queryapi.SessionPreflightPlan, error) {
		state, protocol := campaign.Spec.State, campaign.Spec.ActiveProtocol
		session, err := sessionPreparer.PrepareSession(ctx, profilerapp.SessionRequest{
			Candidate: profilerapp.CandidateSelection{
				ModelSpec: state.Candidate.ModelSpec, DeviceClass: state.Candidate.DeviceClass, Node: state.TargetNode,
				ExecutionPath: state.Candidate.ExecutionPath, PowerMode: state.Candidate.PowerMode,
				Preprocessor: state.Candidate.Preprocessor, Dataset: protocol.Dataset,
			},
			Workload: protocol.Workload, WarmupIterations: int(protocol.WarmupIterations),
		})
		if err != nil {
			return queryapi.SessionPreflightPlan{}, err
		}
		return queryapi.SessionPreflightPlan{
			PlannedBatchSizes: session.PlannedBatchSizes,
			MinimumSamples:    session.RunConfig.Measurement.MinServiceSamples,
			SLOPercentile:     session.RunConfig.Measurement.SLOPercentile,
		}, nil
	}
	return preflights, nil
}

func buildHandler(
	reader client.Reader, opts options, authorizer *servingkube.Authorizer,
) (http.Handler, error) {
	if opts.servingNamespace == "" {
		opts.servingNamespace = opts.namespace
	}
	passports, err := newEvidencePassportQuery(reader, opts)
	if err != nil {
		return nil, err
	}
	results, err := newCampaignResultQuery(reader, opts)
	if err != nil {
		return nil, err
	}
	preflights, err := newCampaignPreflightQuery(reader, opts)
	if err != nil {
		return nil, err
	}
	systems, err := querykube.NewKubernetesSystemQuery(reader)
	if err != nil {
		return nil, err
	}
	inventory, err := querykube.NewKubernetesInventoryQuery(reader)
	if err != nil {
		return nil, err
	}
	compatibility, err := newModelCompatibilityQuery(reader, opts)
	if err != nil {
		return nil, err
	}
	queries := readapi.NewQueryHandler(readapi.QueryReaders{
		Systems:                  systems.Read,
		Inventory:                inventory.Read,
		ModelCompatibility:       compatibility.Read,
		ModelCompatibilityMatrix: newModelCompatibilityMatrixQuery(reader, opts),
		ProfilingRuns: func(ctx context.Context, namespace string) (queryapi.ProfilingRunsSnapshot, error) {
			query, err := querykube.NewKubernetesProfilingRunsQuery(reader, namespace)
			if err != nil {
				return queryapi.ProfilingRunsSnapshot{}, err
			}
			return query.Read(ctx)
		},
	})
	catalog, err := newServingCatalogReader(opts)
	if err != nil {
		return nil, err
	}
	resolver, err := profilerkube.NewKubernetesEvidenceStoreResolver(
		reader,
		profilerkube.EvidenceStoreResolverOptions{SystemName: opts.systemName, Namespace: opts.namespace},
	)
	if err != nil {
		return nil, err
	}
	measurements := &measurementService{client: reader, opts: opts, catalog: catalog,
		objects: campaignResultObjectReader{
			reader:    reader,
			resolver:  resolver,
			namespace: opts.namespace,
			tempDir:   opts.tempDir,
		},
	}
	measurements.coverage = &querykube.CoverageService{
		Reader: reader, Objects: measurements.objects,
		Catalog:         queryCatalogSource(catalog),
		Decoder:         decodeOccupancyEvidence,
		Authorize:       measurements.namespace,
		RequestTimeout:  opts.requestTimeout,
		MaxRequestBytes: opts.maxRequestBytes, MaxEvidenceBytes: opts.maxEvidenceBytes,
		Inventory: func(ctx context.Context) (queryapi.InventoryView, error) {
			snapshot, err := inventory.Read(ctx)
			return queryapi.BuildInventory(snapshot), err
		},
		Compatibility: newModelCompatibilityMatrixQuery(reader, opts),
	}
	handler, err := readapi.NewHandler(
		passports,
		querykube.MeasurementResultReader{ReadSource: results.Read, Catalog: queryCatalogSource(catalog)},
		querykube.MeasurementPreflightReader{
			ReadSource: preflights.Read,
			Enrichment: &querykube.MeasurementPreflightService{
				Reader: reader, Objects: measurements.objects, Catalog: queryCatalogSource(catalog),
				Authorize: measurements.namespace, StoreName: opts.storeName, MaxEvidenceBytes: opts.maxEvidenceBytes,
			},
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Query API HTTP handler: %w", err)
	}
	mux := http.NewServeMux()
	measurements.register(mux)
	mux.Handle("GET /api/v1/systems", queries)
	mux.Handle("GET /api/v1/inventory", queries)
	mux.Handle("GET /api/v1/model-compatibility", queries)
	mux.Handle("GET /api/v1/model-compatibility-matrix", queries)
	mux.Handle("GET /api/v1/namespaces/{namespace}/profiling-runs", queries)
	mux.Handle("/", handler)
	return authenticatedQueryHandler(mux, authorizer, opts), nil
}

// campaignResultObjectReader opens one stored object using the Evidence Store
// binding the owning ChillSystem currently declares. A campaign result is not
// recorded in a measurement Job the way Run evidence is, so the binding is
// resolved per read rather than recovered from a Job.
type campaignResultObjectReader struct {
	reader    client.Reader
	resolver  *profilerkube.KubernetesEvidenceStoreResolver
	namespace string
	tempDir   string
}

func (r campaignResultObjectReader) store(ctx context.Context) (*evidences3.Store, error) {
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
	return store, nil
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
			return fmt.Errorf("shut down Query API API: %w", err)
		}
		return nil
	}
}

func (r campaignResultObjectReader) Open(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	store, err := r.store(ctx)
	if err != nil {
		return nil, err
	}
	return store.Open(ctx, identity)
}

func (r campaignResultObjectReader) OpenLocation(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	store, err := r.store(ctx)
	if err != nil {
		return nil, err
	}
	return store.OpenLocation(ctx, location)
}

func (r campaignResultObjectReader) List(ctx context.Context, prefix string) ([]evidencestore.ObjectIdentity, error) {
	store, err := r.store(ctx)
	if err != nil {
		return nil, err
	}
	return store.List(ctx, prefix)
}
