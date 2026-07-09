package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/chilllabels"
	"github.com/lab-paper-code/chill/internal/deviceclassdiscovery"
)

var _ = Describe("DeviceClass discovery", func() {
	It("creates a catalog-matched DeviceClass and labels the node", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoveryCatalog(ctx, namespace.Name, "catalog", orinNanoCatalogYAML())
		node := createDiscoveryNode(ctx, "orin-nano-"+runID, runID, map[string]string{
			"jetson-model": "orin-nano",
		})

		reconciler := discoveryReconciler(namespace.Name, "catalog", runID, true)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		deviceClass := &edgev1alpha1.DeviceClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "jetson-orin-nano-8g"}, deviceClass)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, deviceClass)
		})
		Expect(deviceClass.Spec.NodeSelector).To(Equal(map[string]string{
			defaultDeviceDiscoveryLabelKey: "jetson-orin-nano-8g",
		}))
		Expect(deviceClass.Spec.Architecture).To(Equal("arm64"))
		Expect(deviceClass.Spec.MemoryBytes.Cmp(resource.MustParse("8Gi"))).To(Equal(0))
		Expect(deviceClass.Spec.Accelerator).To(Equal("nvidia-jetson-orin-nano"))
		Expect(deviceClass.Spec.PowerModes).To(HaveLen(2))

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels[defaultDeviceDiscoveryLabelKey]).To(Equal("jetson-orin-nano-8g"))
		Expect(updatedNode.Annotations[deviceDiscoveryManagedByKey]).To(Equal(deviceDiscoveryManagedBy))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryResult]).To(Equal(chilllabels.DiscoveryResultMatched))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryReason]).To(Equal(chilllabels.DiscoveryReasonCatalogMatched))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryClass]).To(Equal("jetson-orin-nano-8g"))
	})

	It("does not overwrite an existing manual node label by default", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoveryCatalog(ctx, namespace.Name, "catalog", orinNanoCatalogYAML())
		node := createDiscoveryNode(ctx, "manual-"+runID, runID, map[string]string{
			"jetson-model":                 "orin-nano",
			defaultDeviceDiscoveryLabelKey: "manual-class",
		})

		reconciler := discoveryReconciler(namespace.Name, "catalog", runID, true)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels[defaultDeviceDiscoveryLabelKey]).To(Equal("manual-class"))
		Expect(updatedNode.Annotations).NotTo(HaveKey(deviceDiscoveryManagedByKey))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryResult]).To(Equal(chilllabels.DiscoveryResultMatched))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryReason]).To(Equal(chilllabels.DiscoveryReasonManualLabelPreserved))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryClass]).To(Equal("jetson-orin-nano-8g"))

		deviceClass := &edgev1alpha1.DeviceClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "jetson-orin-nano-8g"}, deviceClass)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, deviceClass)
		})
	})

	It("updates an existing CHILL-managed node label by default", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoveryCatalog(ctx, namespace.Name, "catalog", orinNanoCatalogYAML())
		node := createDiscoveryNodeWithAnnotations(ctx, "managed-"+runID, runID, map[string]string{
			"jetson-model":                 "orin-nano",
			defaultDeviceDiscoveryLabelKey: "stale-class",
		}, map[string]string{
			deviceDiscoveryManagedByKey: deviceDiscoveryManagedBy,
		})

		reconciler := discoveryReconciler(namespace.Name, "catalog", runID, true)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels[defaultDeviceDiscoveryLabelKey]).To(Equal("jetson-orin-nano-8g"))
		Expect(updatedNode.Annotations[deviceDiscoveryManagedByKey]).To(Equal(deviceDiscoveryManagedBy))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryReason]).To(Equal(chilllabels.DiscoveryReasonCatalogMatched))

		deviceClass := &edgev1alpha1.DeviceClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "jetson-orin-nano-8g"}, deviceClass)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, deviceClass)
		})
	})

	It("skips unmatched nodes when catalog matches are required", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoveryCatalog(ctx, namespace.Name, "catalog", orinNanoCatalogYAML())
		node := createDiscoveryNode(ctx, "unmatched-"+runID, runID, map[string]string{
			"jetson-model": "unknown",
		})

		reconciler := discoveryReconciler(namespace.Name, "catalog", runID, true)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels).NotTo(HaveKey(defaultDeviceDiscoveryLabelKey))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryResult]).To(Equal(chilllabels.DiscoveryResultUnmatched))
		Expect(updatedNode.Annotations[chilllabels.DeviceClassDiscoveryReason]).To(Equal(chilllabels.DiscoveryReasonNoCatalogMatch))
		Expect(updatedNode.Annotations).NotTo(HaveKey(chilllabels.DeviceClassDiscoveryClass))

		deviceClass := &edgev1alpha1.DeviceClass{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "unknown-8g"}, deviceClass)
		Expect(err).To(HaveOccurred())
	})
})

func discoveryReconciler(namespace, catalogName, runID string, requireCatalogMatch bool) *DeviceDiscoveryReconciler {
	return &DeviceDiscoveryReconciler{
		Client: k8sClient,
		Options: DeviceDiscoveryOptions{
			LabelKey:            defaultDeviceDiscoveryLabelKey,
			NodeLabelSelector:   "edge.dacs.io/test-run=" + runID,
			RequireCatalogMatch: requireCatalogMatch,
			CatalogNamespace:    namespace,
			CatalogName:         catalogName,
		},
	}
}

func createDiscoveryCatalogNamespace(ctx context.Context, runID string) *corev1.Namespace {
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: "discovery-" + runID},
	}
	Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
	DeferCleanup(func() {
		_ = k8sClient.Delete(ctx, namespace)
	})
	return namespace
}

func createDiscoveryCatalog(ctx context.Context, namespace, name, catalog string) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Data: map[string]string{
			deviceclassdiscovery.CatalogDataKey: catalog,
		},
	}
	Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
}

func createDiscoveryNode(ctx context.Context, name, runID string, extraLabels map[string]string) *corev1.Node {
	return createDiscoveryNodeWithAnnotations(ctx, name, runID, extraLabels, nil)
}

func createDiscoveryNodeWithAnnotations(ctx context.Context, name, runID string, extraLabels, extraAnnotations map[string]string) *corev1.Node {
	labels := map[string]string{
		"edge.dacs.io/test-run": runID,
		corev1.LabelArchStable:  "arm64",
	}
	for key, value := range extraLabels {
		labels[key] = value
	}
	annotations := map[string]string{}
	for key, value := range extraAnnotations {
		annotations[key] = value
	}

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Labels:      labels,
			Annotations: annotations,
		},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{
				corev1.ResourceMemory: resource.MustParse("7802752Ki"),
			},
			NodeInfo: corev1.NodeSystemInfo{
				Architecture: "arm64",
			},
		},
	}
	Expect(k8sClient.Create(ctx, node)).To(Succeed())
	DeferCleanup(func() {
		_ = k8sClient.Delete(ctx, node)
	})
	return node
}

func orinNanoCatalogYAML() string {
	return `classes:
- name: jetson-orin-nano-8g
  matchLabels:
    jetson-model: orin-nano
  architecture: arm64
  memory: 8Gi
  accelerator: nvidia-jetson-orin-nano
  powerModes:
  - name: 15W
    nvpId: 0
  - name: 7W
    nvpId: 3
`
}

func uniqueDiscoveryRunID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}
