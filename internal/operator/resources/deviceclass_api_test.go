package resources

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

var _ = Describe("DeviceClass API", func() {
	It("accepts a manually selected class", func() {
		ctx := context.Background()
		deviceClass := validDeviceClass(uniqueDeviceClassName("valid"))

		Expect(k8sClient.Create(ctx, deviceClass)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, deviceClass)
		})
	})

	It("rejects an empty spec", func() {
		ctx := context.Background()
		deviceClass := &edgev1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: uniqueDeviceClassName("empty-spec")},
		}

		err := k8sClient.Create(ctx, deviceClass)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("rejects an empty node selector", func() {
		ctx := context.Background()
		deviceClass := validDeviceClass(uniqueDeviceClassName("empty-selector"))
		deviceClass.Spec.NodeSelector = map[string]string{}

		err := k8sClient.Create(ctx, deviceClass)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})

	It("rejects an empty power-mode list", func() {
		ctx := context.Background()
		deviceClass := validDeviceClass(uniqueDeviceClassName("empty-modes"))
		deviceClass.Spec.PowerModes = nil

		err := k8sClient.Create(ctx, deviceClass)
		Expect(err).To(HaveOccurred())
		Expect(apierrors.IsInvalid(err)).To(BeTrue())
	})
})

func uniqueDeviceClassName(prefix string) string {
	return fmt.Sprintf("deviceclass-%s-%d", prefix, time.Now().UnixNano())
}

func validDeviceClass(name string) *edgev1alpha1.DeviceClass {
	modeID := 0

	return &edgev1alpha1.DeviceClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: edgev1alpha1.DeviceClassSpec{
			NodeSelector: map[string]string{
				"edge.dacs.io/device-class": name,
			},
			Architecture: "test-arch",
			MemoryBytes:  resource.MustParse("8Gi"),
			Accelerator:  "test-accelerator",
			PowerModes: []edgev1alpha1.PowerMode{
				{Name: "default", NvpID: &modeID},
			},
		},
	}
}
