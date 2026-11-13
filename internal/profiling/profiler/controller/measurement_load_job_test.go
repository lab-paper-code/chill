package controller

import (
	"context"
	"errors"
	"net"
	"slices"
	"strconv"
	"strings"
	"testing"

	api "github.com/lab-paper-code/chill/api/v1alpha1"
	profilerkube "github.com/lab-paper-code/chill/internal/profiling/profiler/kube"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func completedMeasurementLoad(run *api.ProfilingRun, worker *batchv1.Job) *batchv1.Job {
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: worker.Name + "-load", Namespace: run.Namespace, UID: "load-uid", OwnerReferences: worker.OwnerReferences},
		Spec:   batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeName: "gateway-node"}}},
		Status: batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}}
}

// This exercises the controller consumer, not just the load Job projection.
func TestRunReconcilerCreatesOffNodeLoadThenCollectsWorkerUpload(t *testing.T) {
	ctx := context.Background()
	run := testRun()
	grant(run)
	prepared := currentPreparedRun(t)
	resolved := resolvedRunForPrepared(t, prepared)
	worker, err := buildExecutionJob(run, resolved, testEvidenceStoreBinding(), testExecutionJobOptions(), runReconcilerTestScheme(t))
	if err != nil {
		t.Fatal(err)
	}
	worker.UID = "worker-uid"
	worker.Status.Active = 1
	worker.Status.StartTime = ptr.To(metav1.Now())
	run.Status.Job = &api.ProfilingRunObjectReference{Name: worker.Name, UID: string(worker.UID)}
	run.Status.JobCleanup = runJobCleanupPending
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "measurement-pod", Namespace: run.Namespace, Labels: map[string]string{"job-name": worker.Name},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: worker.Name, UID: worker.UID, Controller: ptr.To(true)}}},
		Spec: corev1.PodSpec{NodeName: prepared.RunConfig.TargetNode.Name}, Status: corev1.PodStatus{PodIP: "10.42.1.7", Phase: corev1.PodRunning}}
	gatewayIdentity := prepared.RunConfig.LoadOriginGatewayPod
	gateway := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: gatewayIdentity.Name, Namespace: gatewayIdentity.Namespace, UID: types.UID(gatewayIdentity.UID),
		Labels: map[string]string{"app.kubernetes.io/instance": "actual-installation", "app.kubernetes.io/component": "serving-entrypoint"}},
		Spec: corev1.PodSpec{NodeName: prepared.RunConfig.LoadOriginNode, Containers: []corev1.Container{{Name: "serving-entrypoint", Image: "gateway:test"}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	system := &api.ChillSystem{ObjectMeta: metav1.ObjectMeta{Name: "installation-system", Labels: map[string]string{"app.kubernetes.io/instance": "actual-installation"}}}
	random := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "arbitrary-other-node"}}
	foreignGateway := gateway.DeepCopy()
	foreignGateway.Name, foreignGateway.UID, foreignGateway.Spec.NodeName = "foreign-gateway", "foreign-uid", random.Name
	foreignGateway.Labels = map[string]string{"app.kubernetes.io/instance": "other-installation", "app.kubernetes.io/component": "serving-entrypoint"}
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithStatusSubresource(run, worker, pod).WithObjects(run, worker, pod, gateway, system, random, foreignGateway).Build()
	originNode, originPod, err := profilerkube.ResolveGatewayLoadOrigin(ctx, kube, run.Namespace, system.Name, prepared.RunConfig.TargetNode.Name)
	if err != nil || originNode != prepared.RunConfig.LoadOriginNode || originPod != *gatewayIdentity {
		t.Fatalf("did not select actual gateway origin: node=%q pod=%+v err=%v", originNode, originPod, err)
	}
	collected := errors.New("uploaded runtime evidence reached finalizer")
	finalizer := &recordingRunEvidenceFinalizer{err: collected}
	jobs := &recordingExecutionJobs{}
	r, err := NewRunReconciler(kube, kube, (&recordingRunConfigResolver{result: resolved}).Resolve, jobs.Find, jobs.Ensure, finalizer.Finalize, finalizer.RuntimeTermination)
	if err != nil {
		t.Fatal(err)
	}
	r.MeasurementLoad = MeasurementLoadOptions{ServiceAccountName: "load-account"}
	request := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	if _, err := r.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: worker.Name + "-load"}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("created load before control socket readiness: %v", err)
	}
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	if err := kube.Status().Update(ctx, pod); err != nil {
		t.Fatal(err)
	}
	result, err := r.Reconcile(ctx, request)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("load creation result=%+v err=%v", result, err)
	}
	load := &batchv1.Job{}
	if err := kube.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: worker.Name + "-load"}, load); err != nil {
		t.Fatal(err)
	}
	if !metav1.IsControlledBy(load, run) || load.Spec.Template.Spec.NodeName != gateway.Spec.NodeName ||
		load.Spec.Template.Spec.NodeName == random.Name || load.Spec.Template.Spec.NodeName == pod.Spec.NodeName {
		t.Fatal("load must use the actual gateway Node, never an arbitrary other Node")
	}
	container := load.Spec.Template.Spec.Containers[0]
	if container.Image != "gateway:test" || !slices.Equal(container.Args[:len(container.Args)-1], []string{"profiling-load", "--control-endpoint", "http://10.42.1.7:8081", "--dataset-dir=/data"}) {
		t.Fatalf("load target=%+v", container)
	}
	if got := load.Spec.Template.Spec.InitContainers[0].Command; !strings.HasSuffix(got[len(got)-1], "/profiling") {
		t.Fatalf("wrong dataset subset: %v", got)
	}
	if finalizer.calls != 0 {
		t.Fatal("collected evidence before load completed")
	}
	load.Status = completedMeasurementLoad(run, worker).Status
	if err := kube.Status().Update(ctx, load); err != nil {
		t.Fatal(err)
	}
	worker.Status.Active, worker.Status.Succeeded = 0, 1
	worker.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}
	if err := kube.Status().Update(ctx, worker); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, request); !errors.Is(err, collected) || finalizer.calls != 1 {
		t.Fatalf("did not collect uploaded result: finalizer=%d err=%v", finalizer.calls, err)
	}
	if err := r.deleteMeasurementLoad(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(load), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("load cleanup err=%v", err)
	}
}

func TestMeasurementLoadEndpointMatchesControlReadiness(t *testing.T) {
	for _, address := range []string{"10.42.1.7", "fd00::7"} {
		t.Run(address, func(t *testing.T) {
			run := testRun()
			prepared := currentPreparedRun(t)
			worker, err := buildExecutionJob(run, resolvedRunForPrepared(t, prepared), testEvidenceStoreBinding(), testExecutionJobOptions(), runReconcilerTestScheme(t))
			if err != nil {
				t.Fatal(err)
			}
			worker.Status.StartTime = ptr.To(metav1.Now())
			pod := &corev1.Pod{Spec: *worker.Spec.Template.Spec.DeepCopy(), Status: corev1.PodStatus{PodIP: address, HostIP: "192.0.2.10"}}
			pod.Spec.NodeName = prepared.RunConfig.TargetNode.Name
			runtime := findContainer(t, pod.Spec.Containers, runtimeContainerName)
			var controlPort corev1.ContainerPort
			for _, port := range runtime.Ports {
				if port.Name == "load-control" {
					controlPort = port
				}
			}
			if controlPort.ContainerPort == 0 || runtime.ReadinessProbe == nil || runtime.ReadinessProbe.TCPSocket == nil ||
				runtime.ReadinessProbe.TCPSocket.Port.StrVal != controlPort.Name || runtime.ReadinessProbe.TCPSocket.Host != "" {
				t.Fatal("control readiness must resolve the declared port on this Pod IP")
			}
			gateway := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "serving-entrypoint", Image: "gateway:test"}}}}
			load, err := buildMeasurementLoadJob(run, worker, pod, gateway, prepared.RunConfig, MeasurementLoadOptions{ServiceAccountName: "load"})
			if err != nil {
				t.Fatal(err)
			}
			want := "http://" + net.JoinHostPort(address, strconv.Itoa(int(controlPort.ContainerPort)))
			if !slices.Contains(load.Spec.Template.Spec.Containers[0].Args, want) {
				t.Fatalf("load must use the same Pod endpoint as readiness: want %s, got %v", want, load.Spec.Template.Spec.Containers[0].Args)
			}
		})
	}
}

func TestProfilingLoadRejectsSameNodeAndCollectsFailure(t *testing.T) {
	run := testRun()
	config := currentPreparedRun(t).RunConfig
	worker := ownedJob(run, nil)
	pod := &corev1.Pod{Spec: corev1.PodSpec{NodeName: config.TargetNode.Name}, Status: corev1.PodStatus{PodIP: "10.42.1.7"}}
	config.LoadOriginNode = config.TargetNode.Name
	if _, err := buildMeasurementLoadJob(run, worker, pod, &corev1.Pod{}, config, MeasurementLoadOptions{ServiceAccountName: "load"}); err == nil {
		t.Fatal("accepted same-node active load")
	}
	config.LoadOriginNode = "gateway-node"
	load := completedMeasurementLoad(run, worker)
	load.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "DeadlineExceeded", Message: "load deadline"}}
	kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(load).Build()
	r := &RunReconciler{Client: kube, jobReader: kube}
	if _, err := r.reconcileMeasurementLoad(context.Background(), run, worker, config); err == nil || !strings.Contains(err.Error(), "DeadlineExceeded") {
		t.Fatalf("lost load failure: %v", err)
	}
}
