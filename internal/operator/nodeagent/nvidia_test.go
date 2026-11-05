package nodeagent

import (
	"context"
	"strings"
	"testing"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func nvidiaConfig() Config {
	config := validConfig()
	config.Actuation.NvidiaLockedGPUClock = &NvidiaClockConfig{
		Command: []string{"/host/nvidia-smi"}, CommandTimeout: "10s", Clocks: []int32{1500, 1200, 900},
		RuntimeClassName: "nvidia", NodeNames: []string{"genesis2"},
		HostPaths:          []HostPathMount{{Name: "host-nvidia-smi", HostPath: "/usr/bin/nvidia-smi", MountPath: "/host/nvidia-smi", Type: corev1.HostPathFile, ReadOnly: true}},
		PodSecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr.To[int64](0), RunAsNonRoot: ptr.To(false)},
		SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(false), AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true),
			Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"SYS_ADMIN"}}},
	}
	config.Actuation.Policies = append(config.Actuation.Policies, actuatorcore.ActuationPolicySpec{
		Name: "gpu-900", DeviceClass: "edge-server-rtx-2080-ti-64g", PowerMode: "GPU_900", ActuatorKind: actuatorcore.ActuatorKindNvidiaLockedGPUClock,
		SettlingDurationSeconds: 10, MaxActuationDurationSeconds: 60,
	})
	return config
}

func TestNvidiaOperandKeepsJetsonPrivilegesAndHostPathsSeparate(t *testing.T) {
	config := nvidiaConfig()
	if err := config.Validate(true); err != nil {
		t.Fatal(err)
	}
	classes := append(validClasses(), edgev1alpha1.DeviceClass{ObjectMeta: metav1.ObjectMeta{Name: "edge-server-rtx-2080-ti-64g"},
		Spec: edgev1alpha1.DeviceClassSpec{NodeSelector: map[string]string{"edge.dacs.io/device-class": "edge-server-rtx-2080-ti-64g"}}})
	for _, kind := range []string{actuatorcore.ActuatorKindJetsonNVPModel, actuatorcore.ActuatorKindNvidiaLockedGPUClock} {
		perKind := config
		actuation := config.Actuation.forKind(kind)
		perKind.Actuation = &actuation
		ds, err := buildActuationDaemonSet(validOptions(), perKind, classes)
		if err != nil {
			t.Fatal(err)
		}
		pod := ds.Spec.Template.Spec
		container := pod.Containers[0]
		if kind == actuatorcore.ActuatorKindJetsonNVPModel {
			if ds.Name != validOptions().actuationDaemonSetName() || !pod.HostPID ||
				container.SecurityContext.Capabilities.Add[0] != "SYS_BOOT" {
				t.Fatalf("Jetson operand changed: %+v", pod)
			}
			continue
		}
		if ds.Name != "chill-node-agent-actuation-nvidia" || pod.HostPID || len(pod.InitContainers) != 0 || len(pod.Volumes) != 1 ||
			pod.RuntimeClassName == nil || *pod.RuntimeClassName != "nvidia" || container.SecurityContext.Capabilities.Add[0] != "SYS_ADMIN" ||
			!strings.Contains(strings.Join(container.Args, " "), "--nvidia-clocks=1500,1200,900") {
			t.Fatalf("NVIDIA operand contract: %+v", pod)
		}
		for _, term := range pod.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			if len(term.MatchFields) != 1 || len(term.MatchFields[0].Values) != 1 || term.MatchFields[0].Values[0] != "genesis2" {
				t.Fatalf("NVIDIA pilot not restricted: %+v", term)
			}
		}
	}
	config.Actuation.NvidiaLockedGPUClock.SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_BOOT"}
	if err := config.Validate(true); err == nil {
		t.Fatal("NVIDIA accepted reboot capability")
	}
}

func TestStoppingRemovesBothAdapterOperands(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	opts := validOptions()
	jetson := &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: opts.actuationDaemonSetName(), Namespace: opts.Namespace}}
	nv := jetson.DeepCopy()
	nv.Name += nvidiaActuationSuffix
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(jetson, nv).Build()
	r := &Reconciler{Client: c, Scheme: scheme}
	if err := r.stopActuation(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	list := &appsv1.DaemonSetList{}
	if err := c.List(context.Background(), list, client.InNamespace(opts.Namespace)); err != nil || len(list.Items) != 0 {
		t.Fatalf("operands remain: %v error=%v", list.Items, err)
	}
}

func TestNvidiaNamedNodesPreserveDeviceClassIntersection(t *testing.T) {
	config := nvidiaConfig()
	actuation := config.Actuation.forKind(actuatorcore.ActuatorKindNvidiaLockedGPUClock)
	config.Actuation = &actuation
	config.Actuation.NvidiaLockedGPUClock.NodeNames = []string{"gpu-a", "gpu-b"}
	second := config.Actuation.Policies[0]
	second.Name, second.DeviceClass = "second-gpu", "second-class"
	config.Actuation.Policies = append(config.Actuation.Policies, second)
	classes := []edgev1alpha1.DeviceClass{
		{ObjectMeta: metav1.ObjectMeta{Name: config.Actuation.Policies[0].DeviceClass},
			Spec: edgev1alpha1.DeviceClassSpec{NodeSelector: map[string]string{"device": "first", "arch": "amd64"}}},
		{ObjectMeta: metav1.ObjectMeta{Name: second.DeviceClass},
			Spec: edgev1alpha1.DeviceClassSpec{NodeSelector: map[string]string{"device": "second", "arch": "amd64"}}},
	}
	ds, err := buildActuationDaemonSet(validOptions(), config, classes)
	if err != nil {
		t.Fatal(err)
	}
	terms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	for _, test := range []struct {
		name, device, arch string
		want               bool
	}{
		{"gpu-a", "first", "amd64", true},
		{"gpu-b", "first", "amd64", true},
		{"gpu-a", "second", "amd64", true},
		{"gpu-b", "second", "amd64", true},
		{"other", "first", "amd64", false},
		{"gpu-a", "other", "amd64", false},
		{"gpu-b", "first", "arm64", false},
	} {
		matched := false
		for _, term := range terms {
			selector := &metav1.LabelSelector{}
			for _, requirement := range term.MatchExpressions {
				selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{
					Key: requirement.Key, Operator: metav1.LabelSelectorOperator(requirement.Operator), Values: requirement.Values})
			}
			labelSelector, err := metav1.LabelSelectorAsSelector(selector)
			if err != nil {
				t.Fatal(err)
			}
			fieldMatches := true
			for _, requirement := range term.MatchFields {
				// Unlike label selectors, Kubernetes field selectors require one value.
				if requirement.Key != "metadata.name" || requirement.Operator != corev1.NodeSelectorOpIn || len(requirement.Values) != 1 {
					t.Fatalf("invalid node-name field selector: %+v", requirement)
				}
				fieldMatches = fieldMatches && requirement.Values[0] == test.name
			}
			matched = matched || fieldMatches && labelSelector.Matches(labels.Set{"device": test.device, "arch": test.arch})
		}
		if matched != test.want {
			t.Errorf("selection for %s/%s/%s = %t, want %t", test.name, test.device, test.arch, matched, test.want)
		}
	}
}
