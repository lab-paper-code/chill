package service

import (
	"context"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (s *Service) Preflight(
	ctx context.Context, request EvaluationRequest,
) (EvaluationPreflight, error) {
	result := EvaluationPreflight{
		Checks: []EvaluationCheck{}, LiveTarget: []EvaluationLiveTarget{},
	}
	add := func(id string, passed bool, reason string) {
		result.Checks = append(result.Checks, EvaluationCheck{ID: id, Passed: passed, Reason: reason})
	}
	err := request.Spec.Validate()
	add("spec", err == nil, errorReason(err))
	add("name", len(validation.IsDNS1123Subdomain(request.Name)) == 0, "name must be a DNS subdomain")
	// Evidence refs carry this name and the result reader serves only this store.
	add("evidenceStore", request.Spec.EvidenceStore == s.store, "evidenceStore must be "+s.store)
	if err != nil {
		return result, nil
	}
	patchChecks, err := s.settingsPatchChecks(&request.Spec)
	if err != nil {
		return result, err
	}
	result.Checks = append(result.Checks, patchChecks...)
	inputChecks, err := s.stageInputChecks(ctx, &request.Spec)
	if err != nil {
		return result, err
	}
	result.Checks = append(result.Checks, inputChecks...)
	for _, component := range request.Spec.Target.Components {
		target, err := s.liveTarget(ctx, component)
		if err != nil {
			return result, err
		}
		result.LiveTarget = append(result.LiveTarget, target)
	}
	var runs api.EvaluationRunList
	if err := s.reader.List(ctx, &runs); err != nil {
		return result, err
	}
	free := true
	for i := range runs.Items {
		if meta.IsStatusConditionTrue(runs.Items[i].Status.Conditions, api.EvaluationConditionPoolAcquired) {
			free = false
		}
	}
	add("pool", free, "another EvaluationRun occupies the pool")
	idle, err := s.profilingIdle(ctx)
	if err != nil {
		return result, err
	}
	add("profiling", idle, "profiling measurement is active")
	return result, nil
}

// profilingIdle reports no profiling measurement in progress. Campaign and baseline runs
// are judged through their parent, which stays Measuring while it will launch more runs;
// only standalone runs are read one by one, so preflight never decodes every run.
func (s *Service) profilingIdle(ctx context.Context) (bool, error) {
	var runs api.ProfilingRunList
	standalone, err := labels.Parse("!edge.dacs.io/batch-energy-optimization,!edge.dacs.io/model-loaded-idle-baseline-uid")
	if err != nil {
		return false, err
	}
	if err := s.reader.List(ctx, &runs, client.MatchingLabelsSelector{Selector: standalone}); err != nil {
		return false, err
	}
	for _, run := range runs.Items {
		condition := meta.FindStatusCondition(run.Status.Conditions, api.ProfilingRunConditionExecutionComplete)
		if condition == nil || condition.Status == metav1.ConditionUnknown {
			return false, nil
		}
	}
	var campaigns api.BatchEnergyOptimizationList
	if err := s.reader.List(ctx, &campaigns); err != nil {
		return false, err
	}
	for _, campaign := range campaigns.Items {
		if parentMeasuring(campaign.Status.Phase) {
			return false, nil
		}
	}
	var baselines api.ModelLoadedIdleBaselineList
	if err := s.reader.List(ctx, &baselines); err != nil {
		return false, err
	}
	for _, baseline := range baselines.Items {
		if parentMeasuring(baseline.Status.Phase) {
			return false, nil
		}
	}
	return true, nil
}

func parentMeasuring(phase string) bool {
	return phase == "" || phase == string(api.MeasurementPhaseMeasuring)
}

func errorReason(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s *Service) liveTarget(
	ctx context.Context, component api.EvaluationTargetComponent,
) (EvaluationLiveTarget, error) {
	result := EvaluationLiveTarget{
		Component: component.Name, Resource: component.Resource,
		LiveImageIDs: []string{}, Ready: true,
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(schema.FromAPIVersionAndKind(component.Resource.APIVersion, component.Resource.Kind))
	key := client.ObjectKey{Namespace: component.Resource.Namespace, Name: component.Resource.Name}
	if err := s.reader.Get(ctx, key, object); err != nil {
		return result, err
	}
	var pods corev1.PodList
	if component.Resource.Kind == "Pod" {
		var pod corev1.Pod
		if err := s.reader.Get(ctx, client.ObjectKeyFromObject(object), &pod); err != nil {
			return result, err
		}
		pods.Items = []corev1.Pod{pod}
	} else {
		selector, _, _ := unstructured.NestedStringMap(object.Object, "spec", "selector", "matchLabels")
		if len(selector) == 0 {
			result.Ready = false
			return result, nil
		}
		options := []client.ListOption{client.InNamespace(component.Resource.Namespace), client.MatchingLabels(selector)}
		if err := s.reader.List(ctx, &pods, options...); err != nil {
			return result, err
		}
	}
	count := 0
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		count++
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		for _, status := range pod.Status.ContainerStatuses {
			if !status.Ready {
				ready = false
			}
			result.LiveImageIDs = append(result.LiveImageIDs, status.ImageID)
		}
		if !ready {
			result.Ready = false
		}
	}
	if count == 0 {
		result.Ready = false
	}
	return result, nil
}

// stageInputChecks reports each stage's pinned LoadScenario and the workload coverage
// rule the controller applies when it freezes the inputs.
func (s *Service) stageInputChecks(ctx context.Context, spec *api.EvaluationRunSpec) ([]EvaluationCheck, error) {
	checks := make([]EvaluationCheck, 0, 2*len(spec.Stages)+1)
	var config corev1.ConfigMap
	err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: api.EvaluationConfigName}, &config)
	configFound := err == nil
	if apierrors.IsNotFound(err) {
		checks = append(checks, EvaluationCheck{ID: "evaluationConfig", Reason: api.EvaluationConfigName + " not found"})
	} else if err != nil {
		return nil, err
	}
	for _, stage := range spec.Stages {
		var load api.LoadScenario
		err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: stage.LoadScenarioRef.Name}, &load)
		if apierrors.IsNotFound(err) {
			checks = append(checks, EvaluationCheck{ID: "loadScenario/" + stage.ID, Reason: "LoadScenario not found"})
			continue
		}
		if err != nil {
			return nil, err
		}
		checks = append(checks, EvaluationCheck{ID: "loadScenario/" + stage.ID,
			Passed: load.UID == stage.LoadScenarioRef.UID, Reason: "LoadScenario UID must match"})
		if configFound {
			err := api.EvaluationWorkloadCoverage(load.Spec, spec.LoadGenerator.Shards, config.Data)
			checks = append(checks, EvaluationCheck{ID: "coverage/" + stage.ID, Passed: err == nil, Reason: errorReason(err)})
		}
	}
	return checks, nil
}

func (s *Service) settingsPatchChecks(spec *api.EvaluationRunSpec) ([]EvaluationCheck, error) {
	var checks []EvaluationCheck
	var current serving.SchedulerSettings
	if s.settings != nil {
		var err error
		current, err = s.settings()
		if err != nil {
			return nil, err
		}
	}
	validatePatch := func(id string, raw []byte) {
		check := EvaluationCheck{ID: "settingsPatch/" + id}
		if s.settings == nil {
			check.Reason = "public scheduler settings unavailable"
		} else if _, err := serving.ValidateSchedulerSettingsPatch(current, raw); err != nil {
			check.Reason = err.Error()
		} else {
			check.Passed = true
		}
		checks = append(checks, check)
	}
	if patch := spec.SettingsPatch; patch != nil {
		validatePatch("run", patch.Raw)
	}
	for _, stage := range spec.Stages {
		if stage.SettingsPatch != nil {
			validatePatch(stage.ID, stage.SettingsPatch.Raw)
		}
	}
	return checks, nil
}
