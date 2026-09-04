package main

import (
	"context"
	"fmt"
	"io"
	"time"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/evidencestore"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	batchcontroller "github.com/lab-paper-code/chill/internal/profiling/campaign/controller"
	idlebaselinecontroller "github.com/lab-paper-code/chill/internal/profiling/idlebaseline/controller"
	profilerapp "github.com/lab-paper-code/chill/internal/profiling/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiling/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence"
	evidencekube "github.com/lab-paper-code/chill/internal/profiling/profiler/evidence/kube"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	transitioncontroller "github.com/lab-paper-code/chill/internal/profiling/transitioncost/controller"
)

const profilingResultMaximumObjectBytes int64 = 64 << 20

type profilerControllerShared struct {
	reader            client.Reader
	candidates        profilerapp.CandidateResolver
	nodes             profilerapp.NodeResolver
	power             profilerapp.MetricSourceResolver
	actuationPolicies profilerapp.ActuationPolicyResolver
	preparer          profilercontroller.RunPreparer
	records           *profilercontroller.RunRecords
	policies          profilercontroller.AdmissionPolicyResolver
	executionPolicies profilercontroller.ExecutionPolicyResolver
	stores            profilercontroller.EvidenceStoreBindingResolver
	evidence          *currentEvidenceStore
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
	if err := setupBatchEnergyOptimizationController(mgr, opts, shared); err != nil {
		return err
	}
	if err := setupModelLoadedIdleBaselineController(mgr, opts, shared); err != nil {
		return err
	}
	return setupTransitionCostController(mgr, opts, shared)
}

// setupRunController is the composition root for bounded ProfilingRun
// transitions. Runtime work remains outside the controller process.
func setupRunController(mgr ctrl.Manager, opts options, shared *profilerControllerShared) error {
	resolver, err := profilercontroller.NewRunConfigResolver(
		shared.preparer,
		shared.policies,
		shared.executionPolicies,
	)
	if err != nil {
		return fmt.Errorf("construct RunConfig resolver: %w", err)
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
		mgr.GetClient(), resolver, jobs, finalizer, admitter,
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
	candidates, err := profilerkube.NewKubernetesCandidateResolver(
		reader,
		profilerkube.CandidateResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.executionFactsKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct candidate resolver: %w", err)
	}
	nodes, err := profilerkube.NewKubernetesNodeResolver(reader)
	if err != nil {
		return nil, fmt.Errorf("construct Node resolver: %w", err)
	}
	power, err := profilerkube.NewKubernetesMetricSourceResolver(
		reader,
		profilerkube.MetricSourceResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.metricSourcesKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct metric-source resolver: %w", err)
	}
	workloads, err := profilerkube.NewKubernetesWorkloadObservationResolver(
		reader,
		profilerkube.WorkloadObservationResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.workloadObservationKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct workload-observation resolver: %w", err)
	}
	actuationPolicies, err := profilerkube.NewKubernetesActuationPolicyResolver(
		reader,
		profilerkube.ActuationPolicyResolverOptions{
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
	records, err := profilercontroller.NewRunRecords(reader)
	if err != nil {
		return nil, fmt.Errorf("construct Run record loader: %w", err)
	}
	policies, err := profilerkube.NewKubernetesAdmissionPolicyResolver(
		reader,
		profilerkube.AdmissionPolicyResolverOptions{
			Namespace: opts.watchNamespace, ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey: opts.admissionPoliciesKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct admission-policy resolver: %w", err)
	}
	executionPolicies, err := profilerkube.NewKubernetesExecutionPolicyResolver(
		reader,
		profilerkube.ExecutionPolicyResolverOptions{
			Namespace: opts.watchNamespace, ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey: opts.executionPoliciesKey,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("construct execution-policy resolver: %w", err)
	}
	stores, err := profilerkube.NewKubernetesEvidenceStoreResolver(
		reader,
		profilerkube.EvidenceStoreResolverOptions{
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
	evidence := &currentEvidenceStore{
		reader: reader, namespace: opts.watchNamespace,
		resolver: stores, factory: newEvidenceObjectStore,
	}
	return &profilerControllerShared{
		reader: reader, candidates: candidates, nodes: nodes, power: power,
		actuationPolicies: actuationPolicies, preparer: preparer, records: records,
		policies: policies, executionPolicies: executionPolicies, stores: stores,
		evidence: evidence, retainedEvidence: retainedEvidence,
	}, nil
}

func setupBatchEnergyOptimizationController(
	mgr ctrl.Manager,
	opts options,
	shared *profilerControllerShared,
) error {
	logicalStore := component.EvidenceStoreServiceName(opts.systemName)
	curvePlans, err := batchcontroller.NewCurvePlanSource(
		shared.reader, shared.candidates, shared.preparer, shared.evidence,
		batchcontroller.CurvePlanSourceOptions{
			Store: logicalStore, MaxObjectBytes: profilingResultMaximumObjectBytes,
		},
	)
	if err != nil {
		return fmt.Errorf("construct BatchEnergyOptimization curve-plan source: %w", err)
	}
	curves, err := batchcontroller.NewCurveDeriver(
		shared.records,
		retainedCurveEvidenceLoader{retained: shared.retainedEvidence},
	)
	if err != nil {
		return fmt.Errorf("construct BatchEnergyOptimization curve deriver: %w", err)
	}
	results, err := batchcontroller.NewResultWriter(
		shared.evidence,
		batchcontroller.ResultWriterOptions{
			Store: logicalStore, MaxObjectBytes: profilingResultMaximumObjectBytes,
		},
	)
	if err != nil {
		return fmt.Errorf("construct BatchEnergyOptimization result writer: %w", err)
	}
	reconciler, err := batchcontroller.NewBatchEnergyOptimizationReconciler(
		mgr.GetClient(), curvePlans, curves, results,
	)
	if err != nil {
		return fmt.Errorf("construct BatchEnergyOptimization reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up BatchEnergyOptimization reconciler: %w", err)
	}
	return nil
}

func setupModelLoadedIdleBaselineController(
	mgr ctrl.Manager,
	opts options,
	shared *profilerControllerShared,
) error {
	deriver, err := idlebaselinecontroller.NewDeriver(
		shared.records,
		retainedCurveEvidenceLoader{retained: shared.retainedEvidence},
	)
	if err != nil {
		return fmt.Errorf("construct model-loaded idle baseline deriver: %w", err)
	}
	results, err := idlebaselinecontroller.NewResultWriter(
		shared.evidence,
		idlebaselinecontroller.ResultWriterOptions{
			Store:          component.EvidenceStoreServiceName(opts.systemName),
			MaxObjectBytes: profilingResultMaximumObjectBytes,
		},
	)
	if err != nil {
		return fmt.Errorf("construct model-loaded idle baseline result writer: %w", err)
	}
	reconciler, err := idlebaselinecontroller.NewReconciler(
		mgr.GetClient(), shared.preparer, deriver, results,
	)
	if err != nil {
		return fmt.Errorf("construct ModelLoadedIdleBaseline reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up ModelLoadedIdleBaseline reconciler: %w", err)
	}
	return nil
}

func setupTransitionCostController(
	mgr ctrl.Manager,
	opts options,
	shared *profilerControllerShared,
) error {
	deadlineOverhead := time.Duration(opts.transitionObserverDeadlineOverheadSeconds) * time.Second
	logicalStore := component.EvidenceStoreServiceName(opts.systemName)
	err := transitioncontroller.SetupWithManager(mgr, transitioncontroller.SetupOptions{
		Nodes: shared.nodes, Metrics: shared.power, ActuationPolicies: shared.actuationPolicies,
		StoreBindings: shared.stores, Evidence: shared.evidence, EvidenceStore: logicalStore,
		ObserverNodeName: opts.transitionObserverNodeName,
		AttemptJob: transitioncontroller.AttemptJobOptions{
			ObserverImage:          opts.transitionObserverImage,
			ImagePullPolicy:        corev1.PullPolicy(opts.transitionObserverPullPolicy),
			ServiceAccountName:     opts.transitionObserverServiceAccount,
			ActiveDeadlineOverhead: deadlineOverhead,
			CoordinationBytes:      opts.transitionMaximumObjectBytes,
			WorkspaceBytes:         opts.transitionMaximumObjectBytes,
		},
		MaximumObjectBytes: opts.transitionMaximumObjectBytes,
	})
	if err != nil {
		return fmt.Errorf("set up PowerModeTransitionCost controller: %w", err)
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

func (s *currentEvidenceStore) Open(
	ctx context.Context,
	identity evidencestore.ObjectIdentity,
) (io.ReadCloser, error) {
	return s.OpenLocation(ctx, identity.Location())
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
