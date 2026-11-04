package service

import (
	"context"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	add("spec", err == nil, reason)
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
	for _, stage := range request.Spec.Stages {
		var load api.LoadScenario
		err := s.reader.Get(ctx, client.ObjectKey{Namespace: s.namespace, Name: stage.LoadScenarioRef.Name}, &load)
		if apierrors.IsNotFound(err) {
			add("loadScenario/"+stage.ID, false, "LoadScenario not found")
			continue
		}
		if err != nil {
			return result, err
		}
		add("loadScenario/"+stage.ID, load.UID == stage.LoadScenarioRef.UID, "LoadScenario UID must match")
	}
	for _, component := range request.Spec.Target.Components {
		target, err := s.liveTarget(ctx, component)
		if err != nil {
			return result, err
		}
		result.LiveTarget = append(result.LiveTarget, target)
		add("target/"+component.Name, target.Ready, "external target Pods must be Ready")
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
	var jobs batchv1.JobList
	if err := s.reader.List(ctx, &jobs, client.InNamespace(s.namespace)); err != nil {
		return result, err
	}
	idle := true
	for _, job := range jobs.Items {
		if job.Labels["edge.dacs.io/workload-role"] != "load-generator" {
			continue
		}
		terminal := false
		for _, condition := range job.Status.Conditions {
			if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) &&
				condition.Status == corev1.ConditionTrue {
				terminal = true
			}
		}
		if !terminal && (job.Spec.Suspend == nil || !*job.Spec.Suspend) {
			idle = false
		}
	}
	add("load-generator", idle, "load-generator Job is active")
	var profiles api.ProfilingRunList
	if err := s.reader.List(ctx, &profiles); err != nil {
		return result, err
	}
	idle = true
	for _, run := range profiles.Items {
		condition := meta.FindStatusCondition(run.Status.Conditions, api.ProfilingRunConditionExecutionComplete)
		if condition == nil || condition.Status == metav1.ConditionUnknown {
			idle = false
		}
	}
	var measurements api.BatchEnergyOptimizationList
	if err := s.reader.List(ctx, &measurements); err != nil {
		return result, err
	}
	for _, measurement := range measurements.Items {
		if measurement.Status.Phase == "" || measurement.Status.Phase == string(api.MeasurementPhaseMeasuring) {
			idle = false
		}
	}
	add("profiling", idle, "profiling measurement is active")
	return result, nil
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
