package main

import (
	"context"
	"strings"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/serving/httpapi"
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

func evaluationOccupied(run *api.EvaluationRun) bool {
	return run.Status.StartedAt != nil &&
		!meta.IsStatusConditionTrue(run.Status.Conditions, api.EvaluationConditionCleanupComplete) &&
		len(run.Status.ReleaseRecords) == 0
}

func (s *evaluationService) preflight(
	ctx context.Context, request httpapi.EvaluationRequest,
) (httpapi.EvaluationPreflight, error) {
	result := httpapi.EvaluationPreflight{
		Checks: []httpapi.EvaluationCheck{}, LiveTarget: []httpapi.EvaluationLiveTarget{},
	}
	add := func(id string, passed bool, reason string) {
		result.Checks = append(result.Checks, httpapi.EvaluationCheck{ID: id, Passed: passed, Reason: reason})
	}
	err := request.Spec.Validate()
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	add("spec", err == nil, reason)
	add("name", len(validation.IsDNS1123Subdomain(request.Name)) == 0, "name must be a DNS subdomain")
	if err != nil {
		return result, nil
	}
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
		add("target/"+component.Name, target.Match, "ready live image digest must match spec")
	}
	var runs api.EvaluationRunList
	if err := s.reader.List(ctx, &runs); err != nil {
		return result, err
	}
	free := true
	for i := range runs.Items {
		if evaluationOccupied(&runs.Items[i]) {
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
		if job.Labels["chill.dacs.io/e5"] == "" {
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
	add("e5", idle, "legacy E5 producer/measurement Job is active")
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
		if measurement.Status.Phase == "" || measurement.Status.Phase == measurementPhaseMeasuring {
			idle = false
		}
	}
	add("profiling", idle, "profiling measurement is active")
	return result, nil
}

func imageRepository(image string) string {
	image = strings.Split(image, "@")[0]
	if colon := strings.LastIndex(image, ":"); colon > strings.LastIndex(image, "/") {
		return image[:colon]
	}
	return image
}
func imageDigest(image string) string {
	if index := strings.LastIndex(image, "://"); index >= 0 {
		image = image[index+3:]
	}
	if index := strings.LastIndex(image, "@"); index >= 0 {
		image = image[index+1:]
	}
	return image
}
func (s *evaluationService) liveTarget(
	ctx context.Context, component api.EvaluationTargetComponent,
) (httpapi.EvaluationLiveTarget, error) {
	result := httpapi.EvaluationLiveTarget{
		Component: component.Name, Resource: component.Resource, SpecImage: component.Image,
		LiveImageIDs: []string{}, Match: true,
	}
	object := &unstructured.Unstructured{}
	object.SetGroupVersionKind(schema.FromAPIVersionAndKind(component.Resource.APIVersion, component.Resource.Kind))
	key := client.ObjectKey{Namespace: component.Resource.Namespace, Name: component.Resource.Name}
	if err := s.reader.Get(ctx, key, object); err != nil {
		return result, err
	}
	podPath := []string{"spec", "template", "spec", "containers"}
	if component.Resource.Kind == "Pod" {
		podPath = []string{"spec", "containers"}
	}
	containers, _, _ := unstructured.NestedSlice(object.Object, podPath...)
	names := map[string]bool{}
	for _, value := range containers {
		container := value.(map[string]interface{})
		image, _ := container["image"].(string)
		name, _ := container["name"].(string)
		if imageRepository(image) == imageRepository(component.Image) {
			names[name] = true
		}
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
			result.Match = false
			return result, nil
		}
		options := []client.ListOption{client.InNamespace(component.Resource.Namespace), client.MatchingLabels(selector)}
		if err := s.reader.List(ctx, &pods, options...); err != nil {
			return result, err
		}
	}
	count := 0
	for _, pod := range pods.Items {
		if pod.DeletionTimestamp != nil {
			continue
		}
		count++
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		matched := false
		for _, status := range pod.Status.ContainerStatuses {
			if !status.Ready || status.RestartCount != 0 || status.ImageID == "" {
				ready = false
			}
			if !names[status.Name] {
				continue
			}
			matched = true
			result.LiveImageIDs = append(result.LiveImageIDs, status.ImageID)
			if imageDigest(status.ImageID) != imageDigest(component.Image) {
				result.Match = false
			}
		}
		if !ready || !matched {
			result.Match = false
		}
	}
	if count == 0 {
		result.Match = false
	}
	return result, nil
}
