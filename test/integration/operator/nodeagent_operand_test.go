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
	chillmeta "github.com/lab-paper-code/chill/internal/metadata"
	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	"github.com/lab-paper-code/chill/internal/operator/nodeagent"
)

var _ = Describe("Node agent operand reconciliation", func() {
	It("selects actuation Nodes from DeviceClasses and keeps the actuation DaemonSet while a mode is pending", func() {
		ctx := context.Background()
		suffix := time.Now().UnixNano()
		systemName := fmt.Sprintf("node-agent-it-%d", suffix)
		namespaceName := fmt.Sprintf("node-agent-it-%d", suffix)
		configMapName := component.NodeAgentConfigMapName(systemName)

		namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespaceName}}
		Expect(k8sClient.Create(ctx, namespace)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, namespace) })
		deviceClass := validDeviceClass(uniqueDeviceClassName("node-agent"))
		Expect(k8sClient.Create(ctx, deviceClass)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, deviceClass) })
		configData, err := yaml.Marshal(nodeAgentIntegrationConfig(deviceClass.Name))
		Expect(err).NotTo(HaveOccurred())
		configMap := &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Namespace: namespaceName, Name: configMapName},
			Data:       map[string]string{nodeagent.DefaultConfigKey: string(configData)},
		}
		Expect(k8sClient.Create(ctx, configMap)).To(Succeed())
		system := &edgev1alpha1.ChillSystem{
			ObjectMeta: metav1.ObjectMeta{Name: systemName},
			Spec: edgev1alpha1.ChillSystemSpec{
				ManagementNamespace: namespaceName,
				NodeAgent: edgev1alpha1.ChillNodeAgentSpec{
					Enabled: true, Actuation: true, ConfigRef: corev1.LocalObjectReference{Name: configMapName},
				},
			},
		}
		Expect(k8sClient.Create(ctx, system)).To(Succeed())
		DeferCleanup(func() { _ = k8sClient.Delete(ctx, system) })

		reconciler := &nodeagent.Reconciler{
			Client: k8sClient, Scheme: scheme.Scheme,
			Options: nodeagent.Options{SystemName: systemName, Namespace: namespaceName},
		}
		request := ctrl.Request{NamespacedName: types.NamespacedName{Name: systemName}}
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		agentKey := types.NamespacedName{Namespace: namespaceName, Name: component.NodeAgentDaemonSetName(systemName)}
		actuationKey := types.NamespacedName{
			Namespace: namespaceName, Name: component.NodeAgentActuationDaemonSetName(systemName),
		}
		agent := &appsv1.DaemonSet{}
		Expect(k8sClient.Get(ctx, agentKey, agent)).To(Succeed())
		Expect(metav1.IsControlledBy(agent, system)).To(BeTrue())
		actuation := &appsv1.DaemonSet{}
		Expect(k8sClient.Get(ctx, actuationKey, actuation)).To(Succeed())
		Expect(metav1.IsControlledBy(actuation, system)).To(BeTrue())
		affinity := actuation.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		Expect(affinity.NodeSelectorTerms).
			To(Equal([]corev1.NodeSelectorTerm{{MatchExpressions: []corev1.NodeSelectorRequirement{{
				Key: "edge.dacs.io/device-class", Operator: corev1.NodeSelectorOpIn, Values: []string{deviceClass.Name},
			}}}}))

		pending := &corev1.Node{ObjectMeta: metav1.ObjectMeta{
			Name:   fmt.Sprintf("agent-stop-%d", suffix),
			Labels: map[string]string{chillmeta.PowerModeState: chillmeta.PowerModePending},
		}}
		Expect(k8sClient.Create(ctx, pending)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, pending)).To(Succeed()) })

		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: systemName}, system)).To(Succeed())
		system.Spec.NodeAgent.Actuation = false
		Expect(k8sClient.Update(ctx, system)).To(Succeed())
		result, err := reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		Expect(k8sClient.Get(ctx, actuationKey, &appsv1.DaemonSet{})).To(Succeed())

		pending.Labels[chillmeta.PowerModeState] = chillmeta.PowerModeFailed
		Expect(k8sClient.Update(ctx, pending)).To(Succeed())
		_, err = reconciler.Reconcile(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(apierrors.IsNotFound(k8sClient.Get(ctx, actuationKey, &appsv1.DaemonSet{}))).To(BeTrue())
		Expect(k8sClient.Get(ctx, agentKey, &appsv1.DaemonSet{})).To(Succeed())
	})
})

func nodeAgentIntegrationConfig(deviceClass string) nodeagent.Config {
	runAsUser := int64(0)
	runAsNonRoot, privileged := false, false
	noEscalation, readOnlyRoot := false, true
	maxUnavailable := intstr.FromInt(1)
	return nodeagent.Config{
		Image: "daclab/chill-node-agent:test", ImagePullPolicy: corev1.PullIfNotPresent,
		KubeAPITokenFile: kubeconfig.DefaultServiceAccountTokenFile,
		KubeAPICAFile:    kubeconfig.DefaultServiceAccountCAFile,
		Discovery: nodeagent.DiscoveryConfig{
			ServiceAccountName: "chill-node-agent", HostRoot: "/host", Interval: "10s",
			SignatureFile:          "/etc/chill/node-agent/signatures.yaml",
			SignatureConfigMapName: "chill-node-agent-signatures", SignatureConfigMapKey: "signatures.yaml",
			HostPaths:      []nodeagent.ReadOnlyHostPath{{Name: "host-proc", HostPath: "/proc", MountPath: "/proc"}},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType},
		},
		Actuation: &nodeagent.ActuationConfig{
			ServiceAccountName: "chill-node-agent-actuation", MetricsBindAddress: "0", HealthProbePort: 8081,
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type:          appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &maxUnavailable},
			},
			Policies: []actuatorcore.ActuationPolicySpec{{
				Name: "integration-nvpmodel-v1", DeviceClass: deviceClass,
				PowerMode: "MODE_15W_6CORE", ActuatorKind: actuatorcore.ActuatorKindJetsonNVPModel,
				SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
			}},
			JetsonNVPModel: &nodeagent.JetsonNVPModelConfig{
				Command: []string{"/host/nvpmodel"}, CommandTimeout: "30s",
				HostPaths: []nodeagent.HostPathMount{{
					Name: "host-nvpmodel", HostPath: "/usr/sbin/nvpmodel",
					MountPath: "/host/nvpmodel", Type: corev1.HostPathFile, ReadOnly: true,
				}},
			},
			PodSecurityContext: &corev1.PodSecurityContext{RunAsUser: &runAsUser, RunAsNonRoot: &runAsNonRoot},
			SecurityContext: &corev1.SecurityContext{
				Privileged: &privileged, AllowPrivilegeEscalation: &noEscalation,
				ReadOnlyRootFilesystem: &readOnlyRoot,
				Capabilities: &corev1.Capabilities{
					Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"SYS_BOOT"},
				},
			},
		},
	}
}
