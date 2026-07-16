package main

import (
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	profilerapp "github.com/lab-paper-code/chill/internal/profiler"
	profilercontroller "github.com/lab-paper-code/chill/internal/profiler/controller"
)

// setupIntentController is the composition root for the first bounded
// ProfilingRun transition. Runtime work remains outside the controller process.
func setupIntentController(mgr ctrl.Manager, opts options) error {
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
	reconciler, err := profilercontroller.NewIntentReconciler(mgr.GetClient(), freezer)
	if err != nil {
		return fmt.Errorf("construct intent reconciler: %w", err)
	}
	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up intent reconciler: %w", err)
	}
	return nil
}
