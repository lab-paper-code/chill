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
	power, err := profilercontroller.NewKubernetesPowerSourceResolver(
		reader,
		profilercontroller.PowerSourceResolverOptions{
			Namespace:     opts.watchNamespace,
			ConfigMapName: opts.factSourceConfigMap,
			ConfigMapKey:  opts.powerSourcesKey,
		},
	)
	if err != nil {
		return fmt.Errorf("construct power-source resolver: %w", err)
	}
	preparer, err := profilerapp.NewPreparer(candidates, nodes, power)
	if err != nil {
		return fmt.Errorf("construct Run preparer: %w", err)
	}
	records, err := profilercontroller.NewIntentRecords(mgr.GetClient(), mgr.GetScheme())
	if err != nil {
		return fmt.Errorf("construct intent records: %w", err)
	}
	freezer, err := profilercontroller.NewIntentFreezer(mgr.GetClient(), preparer, records)
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
	jobs, err := profilercontroller.NewExecutionJobs(mgr.GetClient(), mgr.GetScheme(), stores)
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
