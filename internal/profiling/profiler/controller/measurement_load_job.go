package controller

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

var errMeasurementLoadFailed = errors.New("profiling load failed")

// MeasurementLoadOptions reuses the chart's existing load-generator image and identity.
type MeasurementLoadOptions struct {
	Image              string
	ServiceAccountName string
}

func measurementLoadJobName(worker *batchv1.Job) string { return worker.Name + "-load" }

func buildMeasurementLoadJob(run *api.ProfilingRun, worker *batchv1.Job, pod *corev1.Pod, config profilingrun.RunConfig, options MeasurementLoadOptions) (*batchv1.Job, error) {
	if options.Image == "" || options.ServiceAccountName == "" || config.Measurement.Dataset == nil {
		return nil, errors.New("active profiling load requires image, ServiceAccount and frozen dataset")
	}
	if pod.Status.PodIP == "" || pod.Spec.NodeName == "" || pod.Spec.NodeName != config.TargetNode.Name {
		return nil, errors.New("measurement Pod must have an IP on the frozen target Node")
	}
	if config.LoadOriginNode == "" || config.LoadOriginGatewayPod == nil {
		return nil, errors.New("profiling load requires the frozen serving gateway origin")
	}
	if config.LoadOriginNode == pod.Spec.NodeName {
		return nil, errors.New("profiling load gateway Node must differ from the measurement Node")
	}
	data := corev1.VolumeMount{Name: "data", MountPath: "/data"}
	labels := map[string]string{"chill.dacs.io/profiling-load": string(run.UID)}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: measurementLoadJobName(worker), Namespace: run.Namespace, Labels: labels}, Spec: batchv1.JobSpec{
		BackoffLimit: ptr.To[int32](0), ActiveDeadlineSeconds: worker.Spec.ActiveDeadlineSeconds,
		Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}, Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: options.ServiceAccountName,
			AutomountServiceAccountToken: ptr.To(false),
			NodeName:                     config.LoadOriginNode,
			InitContainers: []corev1.Container{{Name: "dataset", Image: config.Measurement.Dataset.Image, VolumeMounts: []corev1.VolumeMount{data},
				Command: []string{"sh", "-ec", `cp -R "$1"/. /data/`, "--", path.Join(config.Measurement.Dataset.Path, "profiling")}}},
			Containers: []corev1.Container{{Name: "load", Image: options.Image, VolumeMounts: []corev1.VolumeMount{data},
				Args: []string{"native-profile", "--control-endpoint", "http://" + net.JoinHostPort(pod.Status.PodIP, "8081"), "--dataset-dir", "/data"}}},
			Volumes: []corev1.Volume{{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		}},
	}}
	return job, nil
}

// reconcileMeasurementLoad creates load only after the measurement Pod has a
// routable IP. The worker receives phase results over its control endpoint.
func (r *RunReconciler) reconcileMeasurementLoad(ctx context.Context, run *api.ProfilingRun, worker *batchv1.Job, config profilingrun.RunConfig) (bool, error) {
	if config.Measurement.WorkloadMode == profilingrun.IdleModelLoadedWorkloadMode {
		return true, nil
	}
	job := &batchv1.Job{}
	key := client.ObjectKey{Namespace: run.Namespace, Name: measurementLoadJobName(worker)}
	if err := r.jobReader.Get(ctx, key, job); err == nil {
		if job.Spec.Template.Spec.NodeName != config.LoadOriginNode {
			return false, fmt.Errorf("%w: load Job Node differs from frozen gateway origin", errMeasurementLoadFailed)
		}
		if !metav1.IsControlledBy(job, run) {
			return false, fmt.Errorf("%w: load Job belongs to another owner", errMeasurementLoadFailed)
		}
		for _, condition := range job.Status.Conditions {
			if condition.Status != corev1.ConditionTrue {
				continue
			}
			if condition.Type == batchv1.JobFailed {
				return false, fmt.Errorf("%w: %s: %s", errMeasurementLoadFailed, condition.Reason, condition.Message)
			}
			if condition.Type == batchv1.JobComplete {
				return true, nil
			}
		}
		return false, nil
	} else if !apierrors.IsNotFound(err) {
		return false, err
	}
	for _, condition := range worker.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return false, fmt.Errorf("%w: completed measurement has no load Job", errMeasurementLoadFailed)
		}
	}
	pods := &corev1.PodList{}
	if err := r.jobReader.List(ctx, pods, client.InNamespace(run.Namespace), client.MatchingLabels{"job-name": worker.Name}); err != nil {
		return false, err
	}
	for i := range pods.Items {
		pod := &pods.Items[i]
		if !metav1.IsControlledBy(pod, worker) || pod.Status.PodIP == "" || pod.Spec.NodeName == "" {
			continue
		}
		ready := false
		for _, condition := range pod.Status.Conditions {
			if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready {
			continue
		}
		gatewayIdentity := config.LoadOriginGatewayPod
		if gatewayIdentity == nil {
			return false, fmt.Errorf("%w: missing frozen gateway identity", errMeasurementLoadFailed)
		}
		gateway := &corev1.Pod{}
		if err := r.jobReader.Get(ctx, client.ObjectKey{Namespace: gatewayIdentity.Namespace, Name: gatewayIdentity.Name}, gateway); err != nil {
			if apierrors.IsNotFound(err) {
				return false, fmt.Errorf("%w: frozen serving gateway Pod no longer exists", errMeasurementLoadFailed)
			}
			return false, err
		}
		if string(gateway.UID) != gatewayIdentity.UID || gateway.Spec.NodeName != config.LoadOriginNode {
			return false, fmt.Errorf("%w: actual serving gateway Pod differs from frozen load origin", errMeasurementLoadFailed)
		}
		desired, err := buildMeasurementLoadJob(run, worker, pod, config, r.MeasurementLoad)
		if err != nil {
			return false, fmt.Errorf("%w: %v", errMeasurementLoadFailed, err)
		}
		if err := controllerutil.SetControllerReference(run, desired, r.Scheme()); err != nil {
			return false, err
		}
		if err := r.Create(ctx, desired); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, err
		}
		return false, nil
	}
	return false, nil
}

func (r *RunReconciler) deleteMeasurementLoad(ctx context.Context, run *api.ProfilingRun) error {
	if run.Status.Job == nil {
		return nil
	}
	job := &batchv1.Job{}
	key := client.ObjectKey{Namespace: run.Namespace, Name: run.Status.Job.Name + "-load"}
	if err := r.jobReader.Get(ctx, key, job); err != nil {
		return client.IgnoreNotFound(err)
	}
	if !metav1.IsControlledBy(job, run) {
		return errors.New("cannot clean up another owner's profiling load Job")
	}
	background := metav1.DeletePropagationBackground
	return client.IgnoreNotFound(r.Delete(ctx, job, &client.DeleteOptions{PropagationPolicy: &background, Preconditions: &metav1.Preconditions{UID: &job.UID}}))
}
