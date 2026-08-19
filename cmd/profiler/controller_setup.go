package main

import (
	"context"
	"fmt"
	"io"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	batchcontroller "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	profilerapp "github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
)

const batchSaturationMaximumObjectBytes int64 = 64 << 20

type profilerControllerShared struct {
	reader            client.Reader
	candidates        profilerapp.CandidateResolver
	preparer          profilercontroller.RunPreparer
	records           *profilercontroller.IntentRecords
	policies          profilercontroller.AdmissionPolicyResolver
	executionPolicies profilercontroller.ExecutionPolicyResolver
	stores            profilercontroller.EvidenceStoreBindingResolver
	retainedEvidence  *evidencekube.RetainedLoader
}

func setupProfilerControllers(mgr ctrl.Manager, opts options) error {
	shared, err := newProfilerControllerShared(mgr, opts)
	if err != nil {
		return err
	}
	if err := setupRunController(mgr, opts, shared); err != nil {
		return err
	}
	return setupBatchSaturationController(mgr, opts, shared)
}

// setupRunController is the composition root for bounded ProfilingRun
// transitions. Runtime work remains outside the controller process.
func setupRunController(mgr ctrl.Manager, opts options, shared *profilerControllerShared) error {
	freezer, err := profilercontroller.NewIntentFreezer(
		mgr.GetClient(),
		shared.preparer,
		shared.policies,
		shared.executionPolicies,
		shared.records,
	)
	if err != nil {
		return fmt.Errorf("construct intent freezer: %w", err)
	}
	jobs, err := profilercontroller.NewExecutionJobs(
		mgr.GetClient(),
		mgr.GetScheme(),
		shared.stores,
		profilercontroller.ExecutionJobOptions{
			WorkloadObserverServiceAccountName: opts.workloadObserverServiceAccount,
			WorkloadObserverKubeAPIServer:      opts.workloadObserverKubeAPIServer,
		},
	)
	if err != nil {
		return fmt.Errorf("construct execution Jobs: %w", err)
	}
	finalizer, err := profilercontroller.NewKubernetesExecutionFinalizer(
		shared.reader,
		newEvidenceObjectStore,
	)
	if err != nil {
		return fmt.Errorf("construct execution evidence finalizer: %w", err)
	}
	admitter, err := profilercontroller.NewKubernetesEvidenceAdmitterWithRetainedLoader(shared.retainedEvidence)
	if err != nil {
		return fmt.Errorf("construct execution evidence admitter: %w", err)
	}
	reconciler, err := profilercontroller.NewRunReconciler(
		mgr.GetClient(), freezer, jobs, finalizer, admitter,
	)
	if err != nil {
		return fmt.Errorf("construct ProfilingRun reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up ProfilingRun reconciler: %w", err)
	}
	return nil
}

func newProfilerControllerShared(mgr ctrl.Manager, opts options) (*profilerControllerShared, error) {
	reader := mgr.GetAPIReader()
	candidates, err := profilercontroller.NewKubernetesCandidateResolver(
		reader,
		profilercontroller.CandidateResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.executionFactsKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct candidate resolver: %w", err)
	}
	nodes, err := profilercontroller.NewKubernetesNodeResolver(reader)
	if err != nil {
		return nil, fmt.Errorf("construct Node resolver: %w", err)
	}
	power, err := profilercontroller.NewKubernetesMetricSourceResolver(
		reader,
		profilercontroller.MetricSourceResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.metricSourcesKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct metric-source resolver: %w", err)
	}
	workloads, err := profilercontroller.NewKubernetesWorkloadObservationResolver(
		reader,
		profilercontroller.WorkloadObservationResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.workloadObservationKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct workload-observation resolver: %w", err)
	}
	actuationPolicies, err := profilercontroller.NewKubernetesActuationPolicyResolver(
		reader,
		profilercontroller.ActuationPolicyResolverOptions{
			Namespace: opts.watchNamespace, ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey: opts.actuationPoliciesKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct actuation-policy resolver: %w", err)
	}
	preparer, err := profilerapp.NewPreparer(candidates, nodes, power, workloads, actuationPolicies)
	if err != nil {
		return nil, fmt.Errorf("construct Run preparer: %w", err)
	}
	records, err := profilercontroller.NewIntentRecords(mgr.GetClient(), mgr.GetScheme())
	if err != nil {
		return nil, fmt.Errorf("construct intent records: %w", err)
	}
	policies, err := profilercontroller.NewKubernetesAdmissionPolicyResolver(
		reader,
		profilercontroller.AdmissionPolicyResolverOptions{
			Namespace: opts.watchNamespace, ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey: opts.admissionPoliciesKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct admission-policy resolver: %w", err)
	}
	executionPolicies, err := profilercontroller.NewKubernetesExecutionPolicyResolver(
		reader,
		profilercontroller.ExecutionPolicyResolverOptions{
			Namespace: opts.watchNamespace, ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey: opts.executionPoliciesKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct execution-policy resolver: %w", err)
	}
	stores, err := profilercontroller.NewKubernetesEvidenceStoreResolver(
		reader,
		profilercontroller.EvidenceStoreResolverOptions{
			SystemName: opts.systemName, Namespace: opts.watchNamespace,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct Evidence Store resolver: %w", err)
	}
	retainedEvidence, err := newRetainedEvidenceLoader(reader)
	if err != nil {
		return nil, err
	}
	return &profilerControllerShared{
		reader: reader, candidates: candidates, preparer: preparer, records: records,
		policies: policies, executionPolicies: executionPolicies, stores: stores,
		retainedEvidence: retainedEvidence,
	}, nil
}

func setupBatchSaturationController(
	mgr ctrl.Manager,
	opts options,
	shared *profilerControllerShared,
) error {
	store := &currentEvidenceStore{
		reader: shared.reader, namespace: opts.watchNamespace,
		resolver: shared.stores, factory: newEvidenceObjectStore,
	}
	logicalStore := component.EvidenceStoreServiceName(opts.systemName)
	curvePlans, err := batchcontroller.NewCurvePlanSource(
		shared.reader, shared.candidates, shared.preparer, store,
		batchcontroller.CurvePlanSourceOptions{
			Store: logicalStore, MaxObjectBytes: batchSaturationMaximumObjectBytes,
		},
	)
	if err != nil {
		return fmt.Errorf("construct BatchSaturation curve-plan source: %w", err)
	}
	curves, err := batchcontroller.NewCurveDeriver(
		shared.records,
		retainedCurveEvidenceLoader{retained: shared.retainedEvidence},
	)
	if err != nil {
		return fmt.Errorf("construct BatchSaturation curve deriver: %w", err)
	}
	results, err := batchcontroller.NewResultWriter(
		store,
		batchcontroller.ResultWriterOptions{
			Store: logicalStore, MaxObjectBytes: batchSaturationMaximumObjectBytes,
		},
	)
	if err != nil {
		return fmt.Errorf("construct BatchSaturation result writer: %w", err)
	}
	reconciler, err := batchcontroller.NewBatchSaturationReconciler(
		mgr.GetClient(), curvePlans, curves, results,
	)
	if err != nil {
		return fmt.Errorf("construct BatchSaturation reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up BatchSaturation reconciler: %w", err)
	}
	return nil
}

type currentEvidenceStoreResolver interface {
	Resolve(context.Context) (profilercontroller.EvidenceStoreBinding, error)
}

type currentEvidenceStore struct {
	reader    client.Reader
	namespace string
	resolver  currentEvidenceStoreResolver
	factory   profilercontroller.EvidenceObjectStoreFactory
}

func (s *currentEvidenceStore) PutImmutable(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
	content io.Reader,
) (evidencestore.Receipt, error) {
	store, err := s.resolve(ctx, identity.Store)
	if err != nil {
		return evidencestore.Receipt{}, err
	}
	return store.PutImmutable(ctx, identity, content)
}

func (s *currentEvidenceStore) OpenLocation(
	ctx context.Context,
	location evidencestore.ObjectLocation,
) (io.ReadCloser, error) {
	store, err := s.resolve(ctx, location.Store)
	if err != nil {
		return nil, err
	}
	return store.OpenLocation(ctx, location)
}

func (s *currentEvidenceStore) resolve(
	ctx context.Context,
	logicalStore string,
) (profilerevidence.ObjectStore, error) {
	binding, err := s.resolver.Resolve(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve current Evidence Store: %w", err)
	}
	if binding.Store != logicalStore {
		return nil, fmt.Errorf(
			"resolved Evidence Store %q does not match requested logical store %q",
			binding.Store, logicalStore,
		)
	}
	credentials, err := storekube.LoadCredentials(
		ctx, s.reader, s.namespace, binding.CredentialsSecretName,
	)
	if err != nil {
		return nil, err
	}
	store, err := s.factory(binding, credentials.AccessKeyID, credentials.SecretAccessKey)
	if err != nil {
		return nil, fmt.Errorf("construct current Evidence Store: %w", err)
	}
	return store, nil
}

type retainedCurveEvidenceLoader struct {
	retained *evidencekube.RetainedLoader
}

func (l retainedCurveEvidenceLoader) Load(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (profilerevidence.VerifiedEvidence, error) {
	verified, err := l.retained.Load(ctx, run, reference)
	if err != nil {
		return profilerevidence.VerifiedEvidence{}, err
	}
	return verified.ForAdmission(), nil
}

func (l retainedCurveEvidenceLoader) LoadIdle(
	ctx context.Context,
	run *edgev1alpha1.ProfilingRun,
	reference evidencestore.ObjectIdentity,
) (profilerevidence.VerifiedIdleEvidence, error) {
	verified, err := l.retained.LoadIdle(ctx, run, reference)
	if err != nil {
		return profilerevidence.VerifiedIdleEvidence{}, err
	}
	return verified.ForAdmission(), nil
}

func newRetainedEvidenceLoader(reader client.Reader) (*evidencekube.RetainedLoader, error) {
	loader, err := evidencekube.NewRetainedLoader(
		reader,
		func(
			binding storekube.Binding,
			credentials storekube.Credentials,
		) (evidencestore.LocationReader, error) {
			return newEvidenceObjectStore(
				binding, credentials.AccessKeyID, credentials.SecretAccessKey,
			)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct retained evidence loader: %w", err)
	}
	return loader, nil
}

func newEvidenceObjectStore(
	binding profilercontroller.EvidenceStoreBinding,
	accessKeyID, secretAccessKey string,
) (profilerevidence.ObjectStore, error) {
	return evidences3.New(evidences3.Config{
		LogicalStore:    binding.Store,
		Bucket:          binding.Bucket,
		Endpoint:        binding.Endpoint,
		Region:          binding.Region,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
		TempDir:         component.ProfilerEvidenceStagingPath,
	})
}
