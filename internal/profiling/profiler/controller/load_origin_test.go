package controller

import (
	"context"
	"errors"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestProfilingLoadRejectsChangedFrozenGatewayIdentity(t *testing.T) {
	for _, changed := range []string{"uid", "node"} {
		t.Run(changed, func(t *testing.T) {
			ctx := context.Background()
			run := testRun()
			config := currentPreparedRun(t).RunConfig
			worker := ownedJob(run, nil)
			measurement := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "measurement", Namespace: run.Namespace, Labels: map[string]string{"job-name": worker.Name},
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: worker.Name, UID: worker.UID, Controller: ptr.To(true)}}},
				Spec: corev1.PodSpec{NodeName: config.TargetNode.Name}, Status: corev1.PodStatus{PodIP: "10.42.1.7", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
			identity := config.LoadOriginGatewayPod
			gateway := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: identity.Name, Namespace: identity.Namespace, UID: types.UID(identity.UID)}, Spec: corev1.PodSpec{NodeName: config.LoadOriginNode}}
			if changed == "uid" {
				gateway.UID = "replacement-pod"
			} else {
				gateway.Spec.NodeName = "different-node"
			}
			kube := fake.NewClientBuilder().WithScheme(runReconcilerTestScheme(t)).WithObjects(measurement, gateway).Build()
			r := &RunReconciler{Client: kube, jobReader: kube, MeasurementLoad: MeasurementLoadOptions{Image: "load:test", ServiceAccountName: "load"}}
			if _, err := r.reconcileMeasurementLoad(ctx, run, worker, config); !errors.Is(err, errMeasurementLoadFailed) {
				t.Fatalf("accepted changed gateway identity: %v", err)
			}
			if err := kube.Get(ctx, client.ObjectKey{Namespace: run.Namespace, Name: worker.Name + "-load"}, &batchv1.Job{}); !apierrors.IsNotFound(err) {
				t.Fatalf("created load after gateway identity changed: %v", err)
			}
		})
	}
}
