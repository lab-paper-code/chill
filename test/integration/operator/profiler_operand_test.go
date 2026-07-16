package operatorintegration

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	operatorprofiler "github.com/lab-paper-code/chill/internal/operator/profiler"
)

var _ = Describe("Profiler operand reconciliation", func() {
	It("applies updates idempotently through the Kubernetes API", func() {
		testContext := context.Background()
		suffix := time.Now().UnixNano()
		systemName := fmt.Sprintf("profiler-it-%d", suffix)
		namespaceName := fmt.Sprintf("profiler-it-%d", suffix)
		configMapName := component.ProfilerConfigMapName(systemName)
		deploymentName := component.ProfilerDeploymentName(systemName)

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}
		Expect(k8sClient.Create(testContext, namespace)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(testContext, namespace) })

		config := profilerIntegrationConfig("daclab/chill-profiler:first")
		configData, err := yaml.Marshal(config)
		Expect(err).NotTo(HaveOccurred())
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespaceName, Name: configMapName},
			Data:       map[string]string{operatorprofiler.DefaultConfigKey: string(configData)},
		}
		Expect(k8sClient.Create(testContext, configMap)).To(Succeed())

		system := &edgev1alpha1.ChillSystem{
			ObjectMeta: metav1.ObjectMeta{Name: systemName},
			Spec: edgev1alpha1.ChillSystemSpec{Profiler: edgev1alpha1.ChillProfilerSpec{
				Enabled:   true,
				ConfigRef: corev1.LocalObjectReference{Name: configMapName},
			}},
		}
		Expect(k8sClient.Create(testContext, system)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(testContext, system) })

		reconciler := &operatorprofiler.Reconciler{
			Client: k8sClient,
			Scheme: scheme.Scheme,
			Options: operatorprofiler.Options{
				SystemName: systemName,
				Namespace:  namespaceName,
			},
		}
		request := ctrl.Request{NamespacedName: types.NamespacedName{Name: systemName}}
		_, err = reconciler.Reconcile(testContext, request)
		Expect(err).NotTo(HaveOccurred())

		key := types.NamespacedName{Namespace: namespaceName, Name: deploymentName}
		created := &appsv1.Deployment{}
		Expect(k8sClient.Get(testContext, key, created)).To(Succeed())
		Expect(metav1.IsControlledBy(created, system)).To(BeTrue())

		_, err = reconciler.Reconcile(testContext, request)
		Expect(err).NotTo(HaveOccurred())
		applied := &appsv1.Deployment{}
		Expect(k8sClient.Get(testContext, key, applied)).To(Succeed())

		_, err = reconciler.Reconcile(testContext, request)
		Expect(err).NotTo(HaveOccurred())
		stable := &appsv1.Deployment{}
		Expect(k8sClient.Get(testContext, key, stable)).To(Succeed())
		Expect(stable.Generation).To(Equal(applied.Generation))
		Expect(stable.ResourceVersion).To(Equal(applied.ResourceVersion))

		Expect(k8sClient.Get(testContext, client.ObjectKeyFromObject(configMap), configMap)).To(Succeed())
		config.Image = "daclab/chill-profiler:second"
		configData, err = yaml.Marshal(config)
		Expect(err).NotTo(HaveOccurred())
		configMap.Data[operatorprofiler.DefaultConfigKey] = string(configData)
		Expect(k8sClient.Update(testContext, configMap)).To(Succeed())
		_, err = reconciler.Reconcile(testContext, request)
		Expect(err).NotTo(HaveOccurred())

		updated := &appsv1.Deployment{}
		Expect(k8sClient.Get(testContext, key, updated)).To(Succeed())
		Expect(updated.Generation).To(BeNumerically(">", stable.Generation))
		Expect(updated.Spec.Template.Spec.Containers[0].Image).To(Equal(config.Image))

		Expect(k8sClient.Get(testContext, types.NamespacedName{Name: systemName}, system)).To(Succeed())
		system.Spec.Profiler.Enabled = false
		Expect(k8sClient.Update(testContext, system)).To(Succeed())
		_, err = reconciler.Reconcile(testContext, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(k8sClient.Get(testContext, key, &appsv1.Deployment{}))).To(BeTrue())
	})
})

func profilerIntegrationConfig(image string) operatorprofiler.Config {
	return operatorprofiler.Config{
		Replicas:           1,
		Image:              image,
		ImagePullPolicy:    corev1.PullIfNotPresent,
		ServiceAccountName: "chill-profiler",
		MetricsBindAddress: "0",
		HealthProbePort:    8081,
		LeaderElection:     true,
		LeaderElectionID:   "profiler.edge.dacs.io",
	}
}
