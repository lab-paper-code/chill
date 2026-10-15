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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	"github.com/lab-paper-code/chill/internal/kubeconfig"
	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	operatornodeactuator "github.com/lab-paper-code/chill/internal/operator/nodeactuator"
)

var _ = Describe("NodeActuator operand reconciliation", func() {
	It("retains the DaemonSet until every handoff is terminal without unrestored mutation", func() {
		ctx := context.Background()
		suffix := time.Now().UnixNano()
		systemName := fmt.Sprintf("node-actuator-it-%d", suffix)
		namespaceName := fmt.Sprintf("node-actuator-it-%d", suffix)
		configMapName := component.NodeActuatorConfigMapName(systemName)
		daemonSetName := component.NodeActuatorDaemonSetName(systemName)

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, namespace) })
		config := nodeActuatorIntegrationConfig()
		configData, err := yaml.Marshal(config)
		Expect(err).NotTo(HaveOccurred())
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespaceName, Name: configMapName},
			Data:       map[string]string{operatornodeactuator.DefaultConfigKey: string(configData)},
		}
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		system := &edgev1alpha1.ChillSystem{
			ObjectMeta: metav1.ObjectMeta{Name: systemName},
			Spec: edgev1alpha1.ChillSystemSpec{
				ManagementNamespace: namespaceName,
				NodeActuator: edgev1alpha1.ChillNodeActuatorSpec{
					Enabled: true, ConfigRef: corev1.LocalObjectReference{Name: configMapName},
				},
			},
		}
		Expect(k8sClient.Create(ctx, system)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, system) })

		reconciler := newNodeActuatorIntegrationReconciler(systemName, namespaceName)
		request := ctrl.Request{NamespacedName: types.NamespacedName{Name: systemName}}
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		key := types.NamespacedName{Namespace: namespaceName, Name: daemonSetName}
		created := &appsv1.DaemonSet{}
		Expect(k8sClient.Get(ctx, key, created)).To(Succeed())
		Expect(metav1.IsControlledBy(created, system)).To(BeTrue())

		handoff := validNodeActuation(uniqueNodeActuationName("actuator-stop"))
		handoff.Namespace = namespaceName
		Expect(k8sClient.Create(ctx, handoff)).To(Succeed())

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: systemName}, system)).To(Succeed())
		system.Spec.NodeActuator.Enabled = false
		system.Spec.NodeActuator.ConfigRef = corev1.LocalObjectReference{}
		Expect(k8sClient.Update(ctx, system)).To(Succeed())
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(k8sClient.Get(ctx, key, &appsv1.DaemonSet{})).To(Succeed())

		now := metav1.Now()
		handoff.Status = edgev1alpha1.NodeActuationStatus{
			ObservedGeneration: handoff.Generation, Phase: edgev1alpha1.NodeActuationPhaseFailed,
			TerminalFailure: &edgev1alpha1.NodeActuationFailure{
				Stage: edgev1alpha1.NodeActuationFailureStagePolicy, Reason: "PolicyRejected",
				Message: "rejected before mutation", OccurredAt: now,
			},
		}
		Expect(k8sClient.Status().Update(ctx, handoff)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, key, &appsv1.DaemonSet{}))).To(BeTrue())
	})
})

func newNodeActuatorIntegrationReconciler(systemName, namespace string) *operatornodeactuator.Reconciler {
	return &operatornodeactuator.Reconciler{
		Client: k8sClient, Scheme: scheme.Scheme,
		Options: operatornodeactuator.Options{SystemName: systemName, Namespace: namespace},
	}
}

func nodeActuatorIntegrationConfig() operatornodeactuator.Config {
	runAsUser := int64(0)
	runAsNonRoot, privileged := false, false
	noEscalation, readOnlyRoot := false, true
	maxUnavailable := intstr.FromInt(1)
	return operatornodeactuator.Config{
		Image: "daclab/chill-node-actuator:test", ImagePullPolicy: corev1.PullIfNotPresent,
		ServiceAccountName: "chill-node-actuator", MetricsBindAddress: "0", HealthProbePort: 8081,
		LeaseDurationSeconds: 30, AllowedNamespaces: []string{"kube-system"},
		UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
			Type:          appsv1.RollingUpdateDaemonSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &maxUnavailable},
		},
		KubeAPITokenFile: kubeconfig.DefaultServiceAccountTokenFile,
		KubeAPICAFile:    kubeconfig.DefaultServiceAccountCAFile,
		ActuationPolicies: []actuatorcore.ActuationPolicySpec{{
			Name: "xavier-nvpmodel-v1", DeviceClass: "jetson-xavier-nx-8g",
			PowerMode: "MODE_15W_6CORE", ActuatorKind: actuatorcore.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		}},
		JetsonNVPModel: &operatornodeactuator.JetsonNVPModelConfig{
			Command: []string{"/host/nvpmodel"}, CommandTimeout: "30s",
			HostPaths: []operatornodeactuator.HostPathMount{{
				Name: "host-nvpmodel", HostPath: "/usr/sbin/nvpmodel",
				MountPath: "/host/nvpmodel", Type: corev1.HostPathFile, ReadOnly: true,
			}},
		},
		NodeSelector: map[string]string{"edge.dacs.io/node-actuation": "enabled"},
		PodSecurityContext: &corev1.PodSecurityContext{
			RunAsUser: &runAsUser, RunAsNonRoot: &runAsNonRoot,
		},
		SecurityContext: &corev1.SecurityContext{
			Privileged: &privileged, AllowPrivilegeEscalation: &noEscalation,
			ReadOnlyRootFilesystem: &readOnlyRoot,
			Capabilities: &corev1.Capabilities{
				Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"SYS_BOOT"},
			},
		},
	}
}
