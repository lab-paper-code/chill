package nodediscovery

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func TestBuildDaemonSet(t *testing.T) {
	config := validConfig()
	config.ExcludeNodeNames = []string{"bad-edge"}
	daemonSet := buildDaemonSet(validOptions(), config)

	if daemonSet.Name != "chill-node-discovery" {
		t.Fatalf("Name = %q, want chill-node-discovery", daemonSet.Name)
	}
	if daemonSet.Namespace != "chill-system" {
		t.Fatalf("Namespace = %q, want chill-system", daemonSet.Namespace)
	}
	if daemonSet.Spec.Template.Spec.ServiceAccountName != "chill-node-discovery" {
		t.Fatalf("ServiceAccountName = %q, want chill-node-discovery", daemonSet.Spec.Template.Spec.ServiceAccountName)
	}
	container := daemonSet.Spec.Template.Spec.Containers[0]
	if container.Image != "daclab/chill-node-discovery:0.1.4" {
		t.Fatalf("Image = %q, want daclab/chill-node-discovery:0.1.4", container.Image)
	}
	if !containsArg(container.Args, "--cleanup-on-exit") {
		t.Fatalf("Args = %v, want cleanup-on-exit", container.Args)
	}
	if containsArgPrefix(container.Args, "--system-name=") {
		t.Fatalf("Args = %v, want system name passed by environment only", container.Args)
	}
	if !containsEnv(container.Env, "CHILL_SYSTEM_NAME", "chill") {
		t.Fatalf("Env = %v, want CHILL_SYSTEM_NAME=chill", container.Env)
	}
	if daemonSet.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.
		NodeSelectorTerms[0].MatchExpressions[0].Values[0] != "bad-edge" {
		t.Fatalf("Affinity = %#v, want excluded node", daemonSet.Spec.Template.Spec.Affinity)
	}
}

func TestConfigRejectsExcludeWithRequiredNodeAffinity(t *testing.T) {
	config := validConfig()
	config.ExcludeNodeNames = []string{"bad-edge"}
	config.Affinity = &corev1.Affinity{
		NodeAffinity: &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{},
		},
	}

	if err := config.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want exclude/nodeAffinity conflict")
	}
}

func validOptions() Options {
	return Options{
		SystemName:    "chill",
		Namespace:     "chill-system",
		DaemonSetName: "chill-node-discovery",
		ConfigMapName: "chill-node-discovery-config",
		ConfigMapKey:  DefaultConfigKey,
	}
}

func validConfig() Config {
	return Config{
		Image:                  "daclab/chill-node-discovery:0.1.4",
		ImagePullPolicy:        corev1.PullIfNotPresent,
		ServiceAccountName:     "chill-node-discovery",
		HostRoot:               "/host",
		Interval:               "10m",
		SignatureFile:          "/etc/chill/node-discovery/signatures.yaml",
		SignatureConfigMapName: "chill-node-discovery-signatures",
		SignatureConfigMapKey:  "signatures.yaml",
		CleanupOnExit:          true,
		CleanupTimeout:         "10s",
		KubeAPITokenFile:       "/var/run/secrets/kubernetes.io/serviceaccount/token",
		KubeAPICAFile:          "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt",
		HostPaths: []HostPathMount{
			{Name: "host-proc", HostPath: "/proc", MountPath: "/proc"},
		},
		UpdateStrategy: appsv1.DaemonSetUpdateStrategy{Type: appsv1.RollingUpdateDaemonSetStrategyType},
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func containsArgPrefix(args []string, want string) bool {
	for _, arg := range args {
		if len(arg) >= len(want) && arg[:len(want)] == want {
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
