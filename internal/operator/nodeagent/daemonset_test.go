package nodeagent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	profilingrun "github.com/lab-paper-code/chill/internal/profiling/run"
)

func TestBuildAgentDaemonSet(t *testing.T) {
	t.Parallel()
	config := validConfig()
	config.Discovery.ExcludeNodeNames = []string{"bad-edge"}
	daemonSet := buildAgentDaemonSet(validOptions(), config)
	spec := daemonSet.Spec.Template.Spec
	if daemonSet.Name != "chill-node-agent" || daemonSet.Namespace != "chill-system" ||
		daemonSet.Spec.Template.Labels[profilingrun.MeasurementWorkloadLabelKey] != profilingrun.SystemRole {
		t.Fatalf("DaemonSet identity = %#v", daemonSet.ObjectMeta)
	}
	if spec.ServiceAccountName != "chill-node-agent" || !spec.HostNetwork || spec.HostPID ||
		spec.DNSPolicy != corev1.DNSClusterFirstWithHostNet {
		t.Fatalf("agent Pod identity = %#v", spec)
	}
	container := spec.Containers[0]
	if container.Image != "daclab/chill-node-agent:test" || container.Command[0] != "/node-agent" ||
		hasArg(container.Args, "--actuate") || !containsEnv(container.Env, "CHILL_SYSTEM_NAME", "chill") ||
		!hasFieldEnvironment(container.Env, "NODE_NAME", "spec.nodeName") {
		t.Fatalf("agent container = %#v", container)
	}
	for _, mount := range container.VolumeMounts {
		if !mount.ReadOnly {
			t.Fatalf("agent mount %q is writable", mount.Name)
		}
	}
	if !reflect.DeepEqual(container.SecurityContext, config.Discovery.SecurityContext) ||
		!*spec.SecurityContext.RunAsNonRoot {
		t.Fatalf("agent security = pod:%#v container:%#v", spec.SecurityContext, container.SecurityContext)
	}
	if spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
		NodeSelectorTerms[0].MatchExpressions[0].Values[0] != "bad-edge" {
		t.Fatalf("Affinity = %#v, want excluded node", spec.Affinity)
	}
}

func TestBothDaemonSetsTolerateMeasurementTaint(t *testing.T) {
	t.Parallel()
	config := validConfig()
	edge := corev1.Toleration{Key: "node-role.kubernetes.io/edge", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	config.Discovery.Tolerations = []corev1.Toleration{edge}
	config.Actuation.Tolerations = []corev1.Toleration{edge}
	actuation, err := buildActuationDaemonSet(validOptions(), config, validClasses())
	if err != nil {
		t.Fatal(err)
	}
	want := []corev1.Toleration{edge, profilingrun.MeasurementToleration()}
	for _, daemonSet := range []*appsv1.DaemonSet{buildAgentDaemonSet(validOptions(), config), actuation} {
		if got := daemonSet.Spec.Template.Spec.Tolerations; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s tolerations = %#v", daemonSet.Name, got)
		}
	}
	if len(config.Discovery.Tolerations) != 1 || len(config.Actuation.Tolerations) != 1 {
		t.Fatal("configured tolerations were mutated")
	}
}

func TestActuationAffinityComesFromPolicyDeviceClasses(t *testing.T) {
	t.Parallel()
	config := validConfig()
	daemonSet, err := buildActuationDaemonSet(validOptions(), config, validClasses())
	if err != nil {
		t.Fatal(err)
	}
	spec := daemonSet.Spec.Template.Spec
	if len(spec.NodeSelector) != 0 {
		t.Fatalf("actuation nodeSelector = %#v, want DeviceClass affinity only", spec.NodeSelector)
	}
	want := []corev1.NodeSelectorTerm{
		{MatchExpressions: []corev1.NodeSelectorRequirement{
			{Key: "edge.dacs.io/device-class", Operator: corev1.NodeSelectorOpIn, Values: []string{"jetson-nano-4g"}},
		}},
		{MatchExpressions: []corev1.NodeSelectorRequirement{
			{Key: "edge.dacs.io/device-class", Operator: corev1.NodeSelectorOpIn, Values: []string{"jetson-xavier-nx-8g"}},
			{Key: "kubernetes.io/arch", Operator: corev1.NodeSelectorOpIn, Values: []string{"arm64"}},
		}},
	}
	if got := spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms; !reflect.DeepEqual(got, want) {
		t.Fatalf("actuation nodeSelectorTerms = %#v, want %#v", got, want)
	}
	if _, err := buildActuationDaemonSet(validOptions(), config, validClasses()[2:]); err == nil {
		t.Fatal("actuation DaemonSet built without a policy DeviceClass")
	}
}

func TestActuationDaemonSetKeepsActuatorPrivileges(t *testing.T) {
	t.Parallel()
	config := validConfig()
	daemonSet, err := buildActuationDaemonSet(validOptions(), config, validClasses())
	if err != nil {
		t.Fatal(err)
	}
	spec := daemonSet.Spec.Template.Spec
	if daemonSet.Name != "chill-node-agent-actuation" || spec.ServiceAccountName != "chill-node-agent-actuation" ||
		!reflect.DeepEqual(daemonSet.Spec.UpdateStrategy, config.Actuation.UpdateStrategy) {
		t.Fatalf("actuation DaemonSet identity = %#v", daemonSet)
	}
	// reboot(2) restarts the Node only from the host PID namespace, so a mode
	// established at boot is unreachable without this.
	if !spec.HostPID || !spec.HostNetwork || *spec.SecurityContext.RunAsUser != 0 {
		t.Fatalf("actuation Pod host access = %#v", spec)
	}
	container := spec.Containers[0]
	security := container.SecurityContext
	if container.Command[0] != "/node-agent" || !hasArg(container.Args, "--actuate") ||
		*security.Privileged || *security.AllowPrivilegeEscalation || !*security.ReadOnlyRootFilesystem ||
		!reflect.DeepEqual(security.Capabilities.Add, []corev1.Capability{"SYS_BOOT"}) ||
		!hasPolicyArg(container.Args, config.Actuation.Policies[0]) ||
		!hasArg(container.Args, "--jetson-nvpmodel-command=/host/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1") ||
		!hasArg(container.Args, "--kube-api-server=https://kubernetes.example:6443") ||
		!hasArg(container.Args, "--kube-api-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token") ||
		!hasArg(container.Args, "--kube-api-ca-file=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt") ||
		!hasFieldEnvironment(container.Env, "NODE_NAME", "spec.nodeName") {
		t.Fatalf("actuation container = %#v", container)
	}
	if len(spec.Volumes) != len(config.Actuation.JetsonNVPModel.HostPaths) || len(spec.Containers) != 1 ||
		!hasHostMount(daemonSet, "host-sys", "/sys", "/sys", false) ||
		!hasHostMount(daemonSet, "host-nvpmodel", "/usr/sbin/nvpmodel", "/host/usr/sbin/nvpmodel", true) {
		t.Fatalf("actuation host mounts = volumes:%#v mounts:%#v", spec.Volumes, container.VolumeMounts)
	}
}

func TestActuationDaemonSetUpdateStrategy(t *testing.T) {
	t.Parallel()
	for name, strategy := range map[string]appsv1.DaemonSetUpdateStrategy{
		"OnDelete": {Type: appsv1.OnDeleteDaemonSetStrategyType},
		"missing":  {},
		"unknown":  {Type: "Paused"},
		"OnDelete with rolling options": {
			Type:          appsv1.OnDeleteDaemonSetStrategyType,
			RollingUpdate: &appsv1.RollingUpdateDaemonSet{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			config.Actuation.UpdateStrategy = strategy
			daemonSet, err := buildActuationDaemonSet(validOptions(), config, validClasses())
			if name != "OnDelete" {
				if err == nil || !strings.Contains(err.Error(), "updateStrategy") {
					t.Fatalf("invalid strategy returned %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(daemonSet.Spec.UpdateStrategy, strategy) || daemonSet.Spec.UpdateStrategy.RollingUpdate != nil {
				t.Fatalf("OnDelete strategy changed: %#v", daemonSet.Spec.UpdateStrategy)
			}
		})
	}
}

func TestActuationDaemonSetEnsuresJetsonBootOrderingOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	config := validConfig()
	daemonSet, err := buildActuationDaemonSet(validOptions(), config, validClasses())
	if err != nil || len(daemonSet.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatalf("disabled boot ordering: err=%v spec=%#v", err, daemonSet)
	}
	config.Actuation.JetsonNVPModel.EnsureBootOrdering = true
	daemonSet, err = buildActuationDaemonSet(validOptions(), config, validClasses())
	if err != nil {
		t.Fatal(err)
	}
	spec := daemonSet.Spec.Template.Spec
	if len(spec.InitContainers) != 1 || len(spec.Volumes) != len(config.Actuation.JetsonNVPModel.HostPaths)+2 ||
		len(spec.Containers[0].VolumeMounts) != len(config.Actuation.JetsonNVPModel.HostPaths) {
		t.Fatalf("boot-ordering Pod surface = %#v", spec)
	}
	initContainer := spec.InitContainers[0]
	security := initContainer.SecurityContext
	if initContainer.Image != config.Image || !reflect.DeepEqual(initContainer.Command, []string{"/node-agent", "ensure-jetson-boot-ordering"}) ||
		security == nil || *security.Privileged || *security.AllowPrivilegeEscalation || !*security.ReadOnlyRootFilesystem ||
		len(security.Capabilities.Add) != 0 || !reflect.DeepEqual(security.Capabilities.Drop, []corev1.Capability{"ALL"}) {
		t.Fatalf("boot-ordering init container = %#v", initContainer)
	}
	want := []corev1.VolumeMount{
		{Name: "host-systemd-etc", MountPath: "/host/etc/systemd/system"},
		{Name: "host-systemd-lib", MountPath: "/host/lib/systemd/system", ReadOnly: true},
	}
	if !reflect.DeepEqual(initContainer.VolumeMounts, want) {
		t.Fatalf("boot-ordering mounts = %#v", initContainer.VolumeMounts)
	}
}

func TestConfigRejectsInvalidContract(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Config){
		"API token":  func(config *Config) { config.KubeAPITokenFile = "" },
		"API server": func(config *Config) { config.KubeAPIServer = " https://kubernetes.example:6443" },
		"discovery exclude with required affinity": func(config *Config) {
			config.Discovery.ExcludeNodeNames = []string{"bad-edge"}
			config.Discovery.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{},
			}}
		},
		"missing actuation": func(config *Config) { config.Actuation = nil },
		"policy":            func(config *Config) { config.Actuation.Policies = nil },
		"host adapter":      func(config *Config) { config.Actuation.JetsonNVPModel = nil },
		"unsupported adapter": func(config *Config) {
			config.Actuation.Policies[0].ActuatorKind = actuatorcore.ActuatorKindNvidiaLockedGPUClock
		},
		"broad host path": func(config *Config) {
			config.Actuation.JetsonNVPModel.HostPaths[0].HostPath = "/"
		},
		"boot-ordering host path collision": func(config *Config) {
			config.Actuation.JetsonNVPModel.EnsureBootOrdering = true
			config.Actuation.JetsonNVPModel.HostPaths[0].HostPath = "/etc/systemd/system"
		},
		"no restart capability": func(config *Config) {
			config.Actuation.SecurityContext.Capabilities.Add = nil
		},
		"capability beyond restart": func(config *Config) {
			config.Actuation.SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_BOOT", "NET_ADMIN"}
		},
		"wrong capability": func(config *Config) {
			config.Actuation.SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			mutate(&config)
			if err := config.Validate(true); err == nil {
				t.Fatalf("invalid config accepted: %#v", config)
			}
		})
	}
	config := validConfig()
	config.Actuation = nil
	if err := config.Validate(false); err != nil {
		t.Fatalf("discovery-only config rejected: %v", err)
	}
}

func validOptions() Options {
	return Options{SystemName: "chill", Namespace: "chill-system"}
}

func validClasses() []edgev1alpha1.DeviceClass {
	class := func(name string, selector map[string]string) edgev1alpha1.DeviceClass {
		return edgev1alpha1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec:       edgev1alpha1.DeviceClassSpec{NodeSelector: selector},
		}
	}
	return []edgev1alpha1.DeviceClass{
		class("jetson-xavier-nx-8g", map[string]string{
			"kubernetes.io/arch": "arm64", "edge.dacs.io/device-class": "jetson-xavier-nx-8g",
		}),
		class("jetson-nano-4g", map[string]string{"edge.dacs.io/device-class": "jetson-nano-4g"}),
		class("x86-rtx-2080ti", map[string]string{"edge.dacs.io/device-class": "x86-rtx-2080ti"}),
	}
}

func validConfig() Config {
	runAsUser := int64(0)
	runAsNonRoot, privileged := false, false
	noEscalation, readOnlyRoot := false, true
	maxUnavailable := intstr.FromInt(1)
	policy := func(name, class, mode string) actuatorcore.ActuationPolicySpec {
		return actuatorcore.ActuationPolicySpec{
			Name: name, DeviceClass: class, PowerMode: mode, ActuatorKind: actuatorcore.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		}
	}
	return Config{
		Image: "daclab/chill-node-agent:test", ImagePullPolicy: corev1.PullIfNotPresent,
		KubeAPIServer:    "https://kubernetes.example:6443",
		KubeAPITokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
		KubeAPICAFile:    "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		Discovery: DiscoveryConfig{
			ServiceAccountName:     "chill-node-agent",
			HostRoot:               "/host",
			Interval:               "10s",
			SignatureFile:          "/etc/chill/node-agent/signatures.yaml",
			SignatureConfigMapName: "chill-node-agent-signatures",
			SignatureConfigMapKey:  "signatures.yaml",
			HostNetwork:            true,
			HostPaths:              []ReadOnlyHostPath{{Name: "host-proc", HostPath: "/proc", MountPath: "/proc"}},
			PodSecurityContext:     &corev1.PodSecurityContext{RunAsNonRoot: &readOnlyRoot},
			SecurityContext: &corev1.SecurityContext{
				AllowPrivilegeEscalation: &noEscalation, ReadOnlyRootFilesystem: &readOnlyRoot,
				Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
			},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType},
		},
		Actuation: &ActuationConfig{
			ServiceAccountName: "chill-node-agent-actuation", MetricsBindAddress: "0", HealthProbePort: 8081,
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type:          appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &maxUnavailable},
			},
			Policies: []actuatorcore.ActuationPolicySpec{
				policy("xavier-nvpmodel-v1", "jetson-xavier-nx-8g", "MODE_15W_6CORE"),
				policy("nano-5w-v1", "jetson-nano-4g", "5W"),
				policy("orin-maxn-v1", "jetson-agx-orin-64g", "MAXN"),
			},
			JetsonNVPModel: &JetsonNVPModelConfig{
				Command: []string{
					"/host/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1", "--library-path",
					"/host/lib/aarch64-linux-gnu:/host/usr/lib/aarch64-linux-gnu/tegra",
					"/host/usr/sbin/nvpmodel",
				},
				CommandTimeout: "30s",
				HostPaths: []HostPathMount{
					{Name: "host-nvpmodel", HostPath: "/usr/sbin/nvpmodel", MountPath: "/host/usr/sbin/nvpmodel", Type: corev1.HostPathFile, ReadOnly: true},
					{Name: "host-sys", HostPath: "/sys", MountPath: "/sys", Type: corev1.HostPathDirectory},
				},
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

func hasPolicyArg(args []string, want actuatorcore.ActuationPolicySpec) bool {
	for _, argument := range args {
		const prefix = "--allowed-policy="
		if !strings.HasPrefix(argument, prefix) {
			continue
		}
		var got actuatorcore.ActuationPolicySpec
		if json.Unmarshal([]byte(strings.TrimPrefix(argument, prefix)), &got) == nil && reflect.DeepEqual(got, want) {
			return true
		}
	}
	return false
}

func hasHostMount(daemonSet *appsv1.DaemonSet, name, hostPath, mountPath string, readOnly bool) bool {
	volumeFound := false
	for _, volume := range daemonSet.Spec.Template.Spec.Volumes {
		if volume.Name == name && volume.HostPath != nil && volume.HostPath.Path == hostPath {
			volumeFound = true
		}
	}
	if !volumeFound {
		return false
	}
	for _, mount := range daemonSet.Spec.Template.Spec.Containers[0].VolumeMounts {
		if mount.Name == name && mount.MountPath == mountPath && mount.ReadOnly == readOnly {
			return true
		}
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsEnv(env []corev1.EnvVar, name, value string) bool {
	for _, item := range env {
		if item.Name == name && item.Value == value {
			return true
		}
	}
	return false
}

func hasFieldEnvironment(environment []corev1.EnvVar, name, fieldPath string) bool {
	for _, variable := range environment {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.FieldRef != nil &&
			variable.ValueFrom.FieldRef.FieldPath == fieldPath {
			return true
		}
	}
	return false
}
