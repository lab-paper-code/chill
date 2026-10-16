package nodeactuator

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestBuildDaemonSetRetainsOnlyControlPlaneInputs(t *testing.T) {
	t.Parallel()
	options := validOptions()
	config := validConfig()
	daemonSet, err := buildDaemonSet(options, config)
	if err != nil {
		t.Fatal(err)
	}
	if daemonSet.Name != options.DaemonSetName || daemonSet.Namespace != options.Namespace ||
		daemonSet.Spec.Template.Spec.ServiceAccountName != config.ServiceAccountName ||
		daemonSet.Spec.Template.Spec.NodeSelector["edge.dacs.io/node-actuation"] != "enabled" {
		t.Fatalf("DaemonSet identity or selection = %#v", daemonSet)
	}
	if len(daemonSet.Spec.Template.Spec.Volumes) != len(config.JetsonNVPModel.HostPaths) ||
		len(daemonSet.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("NodeActuator host surface = %#v", daemonSet.Spec.Template.Spec)
	}
	// reboot(2) restarts the Node only from the host PID namespace, so a mode
	// established at boot is unreachable without this.
	if !daemonSet.Spec.Template.Spec.HostPID {
		t.Fatal("NodeActuator Pod is not in the host PID namespace")
	}
	container := daemonSet.Spec.Template.Spec.Containers[0]
	if container.Command[0] != "/node-actuator" || container.SecurityContext == nil ||
		container.SecurityContext.Privileged == nil || *container.SecurityContext.Privileged ||
		!hasArg(container.Args, "--allowed-namespace=kube-system") ||
		!hasPolicyArg(container.Args, config.ActuationPolicies[0]) ||
		!hasArg(container.Args, "--jetson-nvpmodel-command=/host/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1") ||
		!hasArg(container.Args, "--kube-api-server=https://kubernetes.example:6443") ||
		!hasArg(container.Args, "--kube-api-token-file=/var/run/secrets/kubernetes.io/serviceaccount/token") ||
		!hasArg(container.Args, "--kube-api-ca-file=/var/run/secrets/kubernetes.io/serviceaccount/ca.crt") ||
		!hasFieldEnvironment(container.Env, "NODE_NAME", "spec.nodeName") ||
		!hasFieldEnvironment(container.Env, "POD_UID", "metadata.uid") {
		t.Fatalf("NodeActuator container contract = %#v", container)
	}
	if !hasHostMount(daemonSet, "host-sys", "/sys", "/sys", false) ||
		!hasHostMount(daemonSet, "host-nvpmodel", "/usr/sbin/nvpmodel", "/host/usr/sbin/nvpmodel", true) {
		t.Fatalf("NodeActuator host mounts = volumes:%#v mounts:%#v", daemonSet.Spec.Template.Spec.Volumes, container.VolumeMounts)
	}
}

func TestBuildDaemonSetEnsuresJetsonBootOrderingOnlyWhenEnabled(t *testing.T) {
	t.Parallel()
	config := validConfig()
	daemonSet, err := buildDaemonSet(validOptions(), config)
	if err != nil || len(daemonSet.Spec.Template.Spec.InitContainers) != 0 {
		t.Fatalf("disabled boot ordering: err=%v spec=%#v", err, daemonSet)
	}
	config.JetsonNVPModel.EnsureBootOrdering = true
	daemonSet, err = buildDaemonSet(validOptions(), config)
	if err != nil {
		t.Fatal(err)
	}
	spec := daemonSet.Spec.Template.Spec
	if len(spec.InitContainers) != 1 || len(spec.Volumes) != len(config.JetsonNVPModel.HostPaths)+2 ||
		len(spec.Containers[0].VolumeMounts) != len(config.JetsonNVPModel.HostPaths) {
		t.Fatalf("boot-ordering Pod surface = %#v", spec)
	}
	initContainer := spec.InitContainers[0]
	security := initContainer.SecurityContext
	if initContainer.Image != config.Image || !reflect.DeepEqual(initContainer.Command, []string{"/node-actuator", "ensure-jetson-boot-ordering"}) ||
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

func TestConfigRejectsImplicitEligibilityOrAuthorization(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Config){
		"node selection": func(config *Config) { config.NodeSelector = nil },
		"namespace":      func(config *Config) { config.AllowedNamespaces = nil },
		"policy":         func(config *Config) { config.ActuationPolicies = nil },
		"host adapter":   func(config *Config) { config.JetsonNVPModel = nil },
		"API token":      func(config *Config) { config.KubeAPITokenFile = "" },
		"API server":     func(config *Config) { config.KubeAPIServer = " https://kubernetes.example:6443" },
		"unsupported adapter": func(config *Config) {
			config.ActuationPolicies[0].ActuatorKind = actuatorcore.ActuatorKindNvidiaLockedGPUClock
		},
		"broad host path": func(config *Config) {
			config.JetsonNVPModel.HostPaths[0].HostPath = "/"
		},
		"boot-ordering host path collision": func(config *Config) {
			config.JetsonNVPModel.EnsureBootOrdering = true
			config.JetsonNVPModel.HostPaths[0].HostPath = "/etc/systemd/system"
		},
		"no restart capability": func(config *Config) {
			config.SecurityContext.Capabilities.Add = nil
		},
		"capability beyond restart": func(config *Config) {
			config.SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_BOOT", "NET_ADMIN"}
		},
		"wrong capability": func(config *Config) {
			config.SecurityContext.Capabilities.Add = []corev1.Capability{"SYS_ADMIN"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			config := validConfig()
			mutate(&config)
			if err := config.Validate(); err == nil {
				t.Fatalf("invalid config accepted: %#v", config)
			}
		})
	}
}

func validOptions() Options {
	return Options{SystemName: "chill", Namespace: "chill-system", DaemonSetName: "chill-node-actuator"}
}

func validConfig() Config {
	runAsUser := int64(0)
	runAsNonRoot, privileged := false, false
	noEscalation, readOnlyRoot := false, true
	return Config{
		Image: "daclab/chill-node-actuator:test", ImagePullPolicy: corev1.PullIfNotPresent,
		ServiceAccountName: "chill-node-actuator", MetricsBindAddress: "0", HealthProbePort: 8081,
		LeaseDurationSeconds: 30, AllowedNamespaces: []string{"kube-system"},
		KubeAPIServer:    "https://kubernetes.example:6443",
		KubeAPITokenFile: "/var/run/secrets/kubernetes.io/serviceaccount/token",
		KubeAPICAFile:    "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		ActuationPolicies: []actuatorcore.ActuationPolicySpec{{
			Name: "xavier-nvpmodel-v1", DeviceClass: "jetson-xavier-nx-8g",
			PowerMode: "MODE_15W_6CORE", ActuatorKind: actuatorcore.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		}},
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

func hasFieldEnvironment(environment []corev1.EnvVar, name, fieldPath string) bool {
	for _, variable := range environment {
		if variable.Name == name && variable.ValueFrom != nil && variable.ValueFrom.FieldRef != nil &&
			variable.ValueFrom.FieldRef.FieldPath == fieldPath {
			return true
		}
	}
	return false
}
