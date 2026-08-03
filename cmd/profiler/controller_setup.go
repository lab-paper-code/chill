package main

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/lab-paper-code/chill/internal/component"
	evidences3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
	profilerapp "github.com/lab-paper-code/chill/internal/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
	profilerevidence "github.com/lab-paper-code/chill/internal/profiler/evidence"
)

// setupRunController is the composition root for bounded ProfilingRun
// transitions. Runtime work remains outside the controller process.
func setupRunController(mgr ctrl.Manager, opts options) error {
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
		return fmt.Errorf("construct candidate resolver: %w", err)
	}
	nodes, err := profilercontroller.NewKubernetesNodeResolver(reader)
	if err != nil {
		return fmt.Errorf("construct Node resolver: %w", err)
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
		return fmt.Errorf("construct metric-source resolver: %w", err)
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
		return fmt.Errorf("construct workload-observation resolver: %w", err)
	}
	preparer, err := profilerapp.NewPreparer(candidates, nodes, power, workloads)
	if err != nil {
		return fmt.Errorf("construct Run preparer: %w", err)
	}
	records, err := profilercontroller.NewIntentRecords(mgr.GetClient(), mgr.GetScheme())
	if err != nil {
		return fmt.Errorf("construct intent records: %w", err)
	}
	policies, err := profilercontroller.NewKubernetesAdmissionPolicyResolver(
		reader,
		profilercontroller.AdmissionPolicyResolverOptions{
			Namespace: opts.watchNamespace, ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey: opts.admissionPoliciesKey,
		},
	)
	if err != nil {
		return fmt.Errorf("construct admission-policy resolver: %w", err)
	}
	freezer, err := profilercontroller.NewIntentFreezer(mgr.GetClient(), preparer, policies, records)
	if err != nil {
		return fmt.Errorf("construct intent freezer: %w", err)
	}
	stores, err := profilercontroller.NewKubernetesEvidenceStoreResolver(
		reader,
		profilercontroller.EvidenceStoreResolverOptions{
			SystemName: opts.systemName, Namespace: opts.watchNamespace,
		},
	)
	if err != nil {
		return fmt.Errorf("construct Evidence Store resolver: %w", err)
	}
	jobs, err := profilercontroller.NewExecutionJobs(
		mgr.GetClient(),
		mgr.GetScheme(),
		stores,
		profilercontroller.ExecutionJobOptions{
			WorkloadObserverServiceAccountName: opts.workloadObserverServiceAccount,
			WorkloadObserverKubeAPIServer:      opts.workloadObserverKubeAPIServer,
		},
	)
	if err != nil {
		return fmt.Errorf("construct execution Jobs: %w", err)
	}
	finalizer, err := profilercontroller.NewKubernetesExecutionFinalizer(
		reader,
		newEvidenceObjectStore,
	)
	if err != nil {
		return fmt.Errorf("construct execution evidence finalizer: %w", err)
	}
	admitter, err := profilercontroller.NewKubernetesEvidenceAdmitter(reader, newEvidenceObjectStore)
	if err != nil {
		return fmt.Errorf("construct execution evidence admitter: %w", err)
	}
	reconciler, err := profilercontroller.NewRunReconciler(mgr.GetClient(), freezer, jobs, finalizer, admitter)
	if err != nil {
		return fmt.Errorf("construct ProfilingRun reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up ProfilingRun reconciler: %w", err)
	}
	return nil
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
