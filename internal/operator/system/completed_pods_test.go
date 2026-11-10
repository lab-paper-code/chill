package system

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

func TestReconcileCompletedDeploymentPods(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*edgev1alpha1.ChillSystem, *appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod)
		remove bool
	}{
		{name: "system evidence store", remove: true},
		{name: "system profiler deployment", change: func(_ *edgev1alpha1.ChillSystem, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.Labels["app.kubernetes.io/component"] = component.Profiler
		}, remove: true},
		{name: "failed retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodFailed
		}},
		{name: "running retained with completed init", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.Status.Phase = corev1.PodRunning
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}}}
		}},
		{name: "unrelated deployment retained", change: func(_ *edgev1alpha1.ChillSystem, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.OwnerReferences = nil
		}},
		{name: "stale system UID retained", change: func(_ *edgev1alpha1.ChillSystem, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.OwnerReferences[0].UID = "old-system"
		}},
		{name: "stale deployment UID retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			rs.OwnerReferences[0].UID = "old-deployment"
		}},
		{name: "stale replica set UID retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.OwnerReferences[0].UID = "old-replica-set"
		}},
		{name: "non-controller pod owner retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			*p.OwnerReferences[0].Controller = false
		}},
		{name: "non-controller replica set owner retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			*rs.OwnerReferences[0].Controller = false
		}},
		{name: "wrong pod owner kind retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.OwnerReferences[0].Kind = "Job"
			p.OwnerReferences[0].APIVersion = batchv1.SchemeGroupVersion.String()
		}},
		{name: "wrong replica set owner kind retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			rs.OwnerReferences[0].Kind = "StatefulSet"
		}},
		{name: "wrong replica set owner API retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			rs.OwnerReferences[0].APIVersion = "foreign.io/v1"
		}},
		{name: "missing replica set retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, _ *appsv1.ReplicaSet, p *corev1.Pod) {
			p.OwnerReferences[0].Name = "missing"
		}},
		{name: "missing deployment retained", change: func(_ *edgev1alpha1.ChillSystem, _ *appsv1.Deployment, rs *appsv1.ReplicaSet, _ *corev1.Pod) {
			rs.OwnerReferences[0].Name = "missing"
		}},
		{name: "unknown system component retained", change: func(_ *edgev1alpha1.ChillSystem, d *appsv1.Deployment, _ *appsv1.ReplicaSet, _ *corev1.Pod) {
			d.Labels["app.kubernetes.io/component"] = "executor"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, system, deployment, rs, pod := completedPodFixture(t)
			if tc.change != nil {
				tc.change(system, deployment, rs, pod)
			}
			r.Client = fake.NewClientBuilder().WithScheme(r.Scheme).WithStatusSubresource(system).WithObjects(system, deployment, rs, pod).Build()
			reconcileCompletedPod(t, r, system.Name)
			err := r.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{})
			if tc.remove && !apierrors.IsNotFound(err) {
				t.Fatalf("completed Pod Get = %v, want NotFound", err)
			}
			if !tc.remove && err != nil {
				t.Fatalf("retained Pod Get = %v", err)
			}
			reconcileCompletedPod(t, r, system.Name)
		})
	}
}

func TestReconcileCompletedHelmDeploymentPods(t *testing.T) {
	for _, componentName := range []string{"operator", "query", "dashboard", "serving-entrypoint", "evaluation-store"} {
		for _, mismatch := range []string{"", "release", "chart", "component", "namespace", "name", "managed-by"} {
			t.Run(componentName+"/"+mismatch, func(t *testing.T) {
				r, system, deployment, rs, pod := completedPodFixture(t)
				// system.name and nameOverride may both differ from release name.
				system.Labels = map[string]string{
					"app.kubernetes.io/name":       "custom-chill",
					"app.kubernetes.io/instance":   "release",
					"app.kubernetes.io/managed-by": "Helm",
					"helm.sh/chart":                "chill-0.1.0",
				}
				deployment.OwnerReferences = nil
				deployment.Labels = map[string]string{}
				for key, value := range system.Labels {
					deployment.Labels[key] = value
				}
				deployment.Labels["app.kubernetes.io/component"] = componentName
				switch mismatch {
				case "release":
					deployment.Labels["app.kubernetes.io/instance"] = "other"
				case "chart":
					deployment.Labels["helm.sh/chart"] = "foreign-0.1.0"
				case "component":
					deployment.Labels["app.kubernetes.io/component"] = "executor"
				case "namespace":
					deployment.Namespace, rs.Namespace, pod.Namespace = completedPodOperatorNamespace, completedPodOperatorNamespace, completedPodOperatorNamespace
				case "name":
					deployment.Labels["app.kubernetes.io/name"] = "foreign"
				case "managed-by":
					deployment.Labels["app.kubernetes.io/managed-by"] = "foreign"
				}
				r.Client = fake.NewClientBuilder().WithScheme(r.Scheme).WithStatusSubresource(system).WithObjects(system, deployment, rs, pod).Build()
				reconcileCompletedPod(t, r, system.Name)
				err := r.Get(context.Background(), client.ObjectKeyFromObject(pod), &corev1.Pod{})
				if mismatch == "" && !apierrors.IsNotFound(err) {
					t.Fatalf("owned Helm Pod Get = %v, want NotFound", err)
				}
				if mismatch != "" && err != nil {
					t.Fatalf("foreign Helm Pod Get = %v", err)
				}
			})
		}
	}
}

func TestReconcileCleansLaterSucceededDeploymentPod(t *testing.T) {
	r, system, deployment, rs, pod := completedPodFixture(t)
	pod.Status.Phase = corev1.PodRunning
	// Exercise initial finalizer reconciliation as well as subsequent refreshes.
	system.Finalizers = nil
	r.Client = fake.NewClientBuilder().WithScheme(r.Scheme).WithStatusSubresource(system, pod).WithObjects(system, deployment, rs, pod).Build()
	reconcileCompletedPod(t, r, system.Name)
	reconcileCompletedPod(t, r, system.Name)
	current := &corev1.Pod{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pod), current); err != nil {
		t.Fatal(err)
	}
	current.Status.Phase = corev1.PodSucceeded
	if err := r.Status().Update(context.Background(), current); err != nil {
		t.Fatal(err)
	}
	reconcileCompletedPod(t, r, system.Name)
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(pod), current); !apierrors.IsNotFound(err) {
		t.Fatalf("later completed Pod Get = %v, want NotFound", err)
	}
	reconcileCompletedPod(t, r, system.Name)
}

const completedPodOperatorNamespace = "operator-namespace"

func completedPodFixture(t *testing.T) (*ChillSystemReconciler, *edgev1alpha1.ChillSystem, *appsv1.Deployment, *appsv1.ReplicaSet, *corev1.Pod) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{appsv1.AddToScheme, batchv1.AddToScheme, corev1.AddToScheme, edgev1alpha1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	system := &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{Name: "research", UID: "system-uid", Finalizers: []string{finalizerName}},
		Spec:       edgev1alpha1.ChillSystemSpec{ManagementNamespace: "chill-system"},
	}
	controller := func(apiVersion, kind, name string, uid types.UID) []metav1.OwnerReference {
		control := true
		return []metav1.OwnerReference{{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid, Controller: &control}}
	}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{
		Name: "evidence-store", Namespace: "chill-system", UID: "deployment-uid",
		Labels: map[string]string{
			"app.kubernetes.io/name":      component.DefaultSystemName,
			"app.kubernetes.io/instance":  system.Name,
			"app.kubernetes.io/component": component.EvidenceStore,
		},
		OwnerReferences: controller(edgev1alpha1.GroupVersion.String(), "ChillSystem", system.Name, system.UID),
	}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "replica-set", Namespace: deployment.Namespace, UID: "replica-set-uid",
		OwnerReferences: controller(appsv1.SchemeGroupVersion.String(), "Deployment", deployment.Name, deployment.UID),
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "completed", Namespace: deployment.Namespace, UID: "pod-uid",
			OwnerReferences: controller(appsv1.SchemeGroupVersion.String(), "ReplicaSet", rs.Name, rs.UID),
		},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded},
	}
	r := &ChillSystemReconciler{Scheme: scheme, Options: Options{SystemName: system.Name, Namespace: completedPodOperatorNamespace}}
	return r, system, deployment, rs, pod
}

func TestReconcileKeepsSucceededJobAndPod(t *testing.T) {
	r, system, _, _, pod := completedPodFixture(t)
	controller := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "procedure", Namespace: pod.Namespace, UID: "job-uid"},
		Status: batchv1.JobStatus{Conditions: []batchv1.JobCondition{
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
		}},
	}
	pod.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller,
	}}
	r.Client = fake.NewClientBuilder().WithScheme(r.Scheme).WithStatusSubresource(system).WithObjects(system, job, pod).Build()
	reconcileCompletedPod(t, r, system.Name)
	for _, object := range []client.Object{job, pod} {
		if err := r.Get(context.Background(), client.ObjectKeyFromObject(object), object); err != nil {
			t.Fatalf("retained %T Get = %v", object, err)
		}
	}
}

func reconcileCompletedPod(t *testing.T, r *ChillSystemReconciler, name string) {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: name}}); err != nil {
		t.Fatalf("Reconcile() error = %v", err)
	}
}
