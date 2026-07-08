package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/gearedge/api/v1alpha1"
)

var _ = Describe("OwnerReference scope", func() {
	It("accepts a namespaced Job owned by a cluster-scoped DeviceProfile", func() {
		ctx := context.Background()
		name := fmt.Sprintf("ownerref-%d", time.Now().UnixNano())

		profile := &edgev1alpha1.DeviceProfile{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		Expect(k8sClient.Create(ctx, profile)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, profile)
		})

		namespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, namespace)
		})

		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "probe",
				Namespace: namespace.Name,
				OwnerReferences: []metav1.OwnerReference{
					{
						APIVersion: edgev1alpha1.GroupVersion.String(),
						Kind:       "DeviceProfile",
						Name:       profile.Name,
						UID:        profile.UID,
					},
				},
			},
			Spec: batchv1.JobSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						RestartPolicy: corev1.RestartPolicyNever,
						Containers: []corev1.Container{
							{
								Name:    "noop",
								Image:   "busybox",
								Command: []string{"true"},
							},
						},
					},
				},
			},
		}
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, job)
		})
	})
})
