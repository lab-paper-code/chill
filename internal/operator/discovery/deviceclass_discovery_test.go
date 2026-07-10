package discovery

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
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/deviceclass"
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
)

const discoveryCatalogName = "catalog"

var _ = Describe("DeviceClass discovery", func() {
	It("creates a catalog-matched DeviceClass and labels the node", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoveryCatalog(ctx, namespace.Name, orinNanoCatalogYAML())
		createDiscoverySystem(ctx, namespace.Name, runID)
		node := createDiscoveryNode(ctx, "orin-nano-"+runID, runID, map[string]string{
			"jetson-model": "orin-nano",
		})

		reconciler := discoveryReconciler(namespace.Name, runID)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		deviceClass := &edgev1alpha1.DeviceClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "jetson-orin-nano-8g"}, deviceClass)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, deviceClass)
		})
		Expect(deviceClass.Spec.NodeSelector).To(Equal(map[string]string{
			chillmeta.DeviceClass: "jetson-orin-nano-8g",
		}))
		Expect(deviceClass.Spec.Architecture).To(Equal("arm64"))
		Expect(deviceClass.Spec.MemoryBytes.Cmp(resource.MustParse("8Gi"))).To(Equal(0))
		Expect(deviceClass.Spec.Accelerator).To(Equal("nvidia-jetson-orin-nano"))
		Expect(deviceClass.Spec.PowerModes).To(HaveLen(2))
		Expect(deviceClass.Labels[chillmeta.System]).To(Equal(discoverySystemName(runID)))

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels[chillmeta.DeviceClass]).To(Equal("jetson-orin-nano-8g"))
		Expect(updatedNode.Annotations[chillmeta.ManagedBy]).To(Equal(chillmeta.ManagedByDeviceDiscovery))
		Expect(updatedNode.Annotations[chillmeta.System]).To(Equal(discoverySystemName(runID)))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryResult]).To(Equal(chillmeta.DiscoveryResultMatched))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryReason]).To(Equal(chillmeta.DiscoveryReasonCatalogMatched))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryClass]).To(Equal("jetson-orin-nano-8g"))
	})

	It("does not overwrite an existing manual node label by default", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoveryCatalog(ctx, namespace.Name, orinNanoCatalogYAML())
		createDiscoverySystem(ctx, namespace.Name, runID)
		node := createDiscoveryNode(ctx, "manual-"+runID, runID, map[string]string{
			"jetson-model":        "orin-nano",
			chillmeta.DeviceClass: "manual-class",
		})

		reconciler := discoveryReconciler(namespace.Name, runID)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels[chillmeta.DeviceClass]).To(Equal("manual-class"))
		Expect(updatedNode.Annotations).NotTo(HaveKey(chillmeta.ManagedBy))
		Expect(updatedNode.Annotations[chillmeta.System]).To(Equal(discoverySystemName(runID)))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryResult]).To(Equal(chillmeta.DiscoveryResultMatched))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryReason]).To(Equal(chillmeta.DiscoveryReasonManualLabelPreserved))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryClass]).To(Equal("jetson-orin-nano-8g"))

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
		createDiscoveryCatalog(ctx, namespace.Name, orinNanoCatalogYAML())
		createDiscoverySystem(ctx, namespace.Name, runID)
		node := createDiscoveryNodeWithAnnotations(ctx, "managed-"+runID, runID, map[string]string{
			"jetson-model":        "orin-nano",
			chillmeta.DeviceClass: "stale-class",
		}, map[string]string{
			chillmeta.ManagedBy: chillmeta.ManagedByDeviceDiscovery,
		})

		reconciler := discoveryReconciler(namespace.Name, runID)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels[chillmeta.DeviceClass]).To(Equal("jetson-orin-nano-8g"))
		Expect(updatedNode.Annotations[chillmeta.ManagedBy]).To(Equal(chillmeta.ManagedByDeviceDiscovery))
		Expect(updatedNode.Annotations[chillmeta.System]).To(Equal(discoverySystemName(runID)))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryReason]).To(Equal(chillmeta.DiscoveryReasonCatalogMatched))

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
		createDiscoveryCatalog(ctx, namespace.Name, orinNanoCatalogYAML())
		createDiscoverySystem(ctx, namespace.Name, runID)
		node := createDiscoveryNode(ctx, "unmatched-"+runID, runID, map[string]string{
			"jetson-model": "unknown",
		})

		reconciler := discoveryReconciler(namespace.Name, runID)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		updatedNode := &corev1.Node{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, updatedNode)).To(Succeed())
		Expect(updatedNode.Labels).NotTo(HaveKey(chillmeta.DeviceClass))
		Expect(updatedNode.Annotations[chillmeta.System]).To(Equal(discoverySystemName(runID)))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryResult]).To(Equal(chillmeta.DiscoveryResultUnmatched))
		Expect(updatedNode.Annotations[chillmeta.DeviceClassDiscoveryReason]).To(Equal(chillmeta.DiscoveryReasonNoCatalogMatch))
		Expect(updatedNode.Annotations).NotTo(HaveKey(chillmeta.DeviceClassDiscoveryClass))

		deviceClass := &edgev1alpha1.DeviceClass{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: "unknown-8g"}, deviceClass)
		Expect(err).To(HaveOccurred())
	})

	It("deletes stale CHILL-managed DeviceClasses", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoveryCatalog(ctx, namespace.Name, orinNanoCatalogYAML())
		createDiscoverySystem(ctx, namespace.Name, runID)
		createDiscoveryNode(ctx, "prune-"+runID, runID, map[string]string{
			"jetson-model": "orin-nano",
		})
		stale := &edgev1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "stale-" + runID,
				Labels: map[string]string{
					chillmeta.System: discoverySystemName(runID),
				},
				Annotations: map[string]string{
					chillmeta.ManagedBy: chillmeta.ManagedByDeviceDiscovery,
				},
			},
			Spec: edgev1alpha1.DeviceClassSpec{
				NodeSelector: map[string]string{chillmeta.DeviceClass: "stale-" + runID},
				Architecture: "arm64",
				MemoryBytes:  resource.MustParse("1Gi"),
				Accelerator:  "none",
				PowerModes:   []edgev1alpha1.PowerMode{{Name: "fixed"}},
			},
		}
		Expect(k8sClient.Create(ctx, stale)).To(Succeed())
		otherSystem := stale.DeepCopy()
		otherSystem.Name = "other-" + runID
		otherSystem.ResourceVersion = ""
		otherSystem.Labels[chillmeta.System] = "other-system"
		Expect(k8sClient.Create(ctx, otherSystem)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, otherSystem)
		})

		reconciler := discoveryReconciler(namespace.Name, runID)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).NotTo(HaveOccurred())

		deviceClass := &edgev1alpha1.DeviceClass{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: stale.Name}, deviceClass)
		Expect(err).To(HaveOccurred())
		err = k8sClient.Get(ctx, types.NamespacedName{Name: otherSystem.Name}, deviceClass)
		Expect(err).NotTo(HaveOccurred())
	})

	It("fails without pruning when the required catalog is missing", func() {
		ctx := context.Background()
		runID := uniqueDiscoveryRunID()
		namespace := createDiscoveryCatalogNamespace(ctx, runID)
		createDiscoverySystem(ctx, namespace.Name, runID)
		createDiscoveryNode(ctx, "missing-catalog-"+runID, runID, map[string]string{
			"jetson-model": "orin-nano",
		})
		stale := &edgev1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{
				Name: "missing-catalog-stale-" + runID,
				Labels: map[string]string{
					chillmeta.System: discoverySystemName(runID),
				},
				Annotations: map[string]string{
					chillmeta.ManagedBy: chillmeta.ManagedByDeviceDiscovery,
				},
			},
			Spec: edgev1alpha1.DeviceClassSpec{
				NodeSelector: map[string]string{chillmeta.DeviceClass: "missing-catalog-stale-" + runID},
				Architecture: "arm64",
				MemoryBytes:  resource.MustParse("1Gi"),
				Accelerator:  "none",
				PowerModes:   []edgev1alpha1.PowerMode{{Name: "fixed"}},
			},
		}
		Expect(k8sClient.Create(ctx, stale)).To(Succeed())
		DeferCleanup(func() {
			_ = k8sClient.Delete(ctx, stale)
		})

		reconciler := discoveryReconciler(namespace.Name, runID)
		_, err := reconciler.Reconcile(ctx, ctrl.Request{})
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("not found"))

		deviceClass := &edgev1alpha1.DeviceClass{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: stale.Name}, deviceClass)).To(Succeed())
	})
})

func discoveryReconciler(namespace, runID string) *DeviceDiscoveryReconciler {
	return &DeviceDiscoveryReconciler{
		Client: k8sClient,
		Scheme: scheme.Scheme,
		Options: DeviceDiscoveryOptions{
			SystemName:          discoverySystemName(runID),
			Namespace:           namespace,
			LabelKey:            chillmeta.DeviceClass,
			NodeLabelSelector:   "edge.dacs.io/test-run=" + runID,
			RequireCatalogMatch: true,
			CatalogNamespace:    namespace,
			CatalogName:         discoveryCatalogName,
		},
	}
}

func createDiscoverySystem(ctx context.Context, namespace, runID string) {
	requireCatalogMatch := true
	system := &edgev1alpha1.ChillSystem{
		ObjectMeta: metav1.ObjectMeta{Name: discoverySystemName(runID)},
		Spec: edgev1alpha1.ChillSystemSpec{
			ManagementNamespace: namespace,
			DeviceDiscovery: edgev1alpha1.ChillDeviceDiscoverySpec{
				Enabled:             true,
				LabelKey:            chillmeta.DeviceClass,
				NodeLabelSelector:   "edge.dacs.io/test-run=" + runID,
				RequireCatalogMatch: &requireCatalogMatch,
				Catalog: edgev1alpha1.ChillConfigMapKeyRef{
					Namespace: namespace,
					Name:      discoveryCatalogName,
					Key:       deviceclass.CatalogDataKey,
				},
			},
		},
	}
	Expect(k8sClient.Create(ctx, system)).To(Succeed())
	DeferCleanup(func() {
		_ = k8sClient.Delete(ctx, system)
	})
}

func discoverySystemName(runID string) string {
	return "system-" + runID
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

func createDiscoveryCatalog(ctx context.Context, namespace, catalog string) {
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      discoveryCatalogName,
		},
		Data: map[string]string{
			deviceclass.CatalogDataKey: catalog,
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
