package nodeactuator

import (
	"testing"

	actuatorcore "github.com/lab-paper-code/chill/internal/nodeactuator"
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
	policyDigests, err := configuredPolicyDigests(config.ActuationPolicies)
	if err != nil {
		t.Fatal(err)
	}
	if daemonSet.Name != options.DaemonSetName || daemonSet.Namespace != options.Namespace ||
		daemonSet.Spec.Template.Spec.ServiceAccountName != config.ServiceAccountName ||
		daemonSet.Spec.Template.Spec.NodeSelector["edge.dacs.io/node-actuation"] != "enabled" {
		t.Fatalf("DaemonSet identity or selection = %#v", daemonSet)
	}
	if len(daemonSet.Spec.Template.Spec.Volumes) != 0 || len(daemonSet.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("pre-adapter DaemonSet has host surface: %#v", daemonSet.Spec.Template.Spec)
	}
	container := daemonSet.Spec.Template.Spec.Containers[0]
	if container.Command[0] != "/node-actuator" || container.SecurityContext == nil ||
		container.SecurityContext.Privileged != nil || !hasArg(container.Args, "--allowed-namespace=kube-system") ||
		!hasArg(container.Args, "--allowed-policy-digest="+policyDigests[0]) ||
		!hasFieldEnvironment(container.Env, "NODE_NAME", "spec.nodeName") ||
		!hasFieldEnvironment(container.Env, "POD_UID", "metadata.uid") {
		t.Fatalf("NodeActuator container contract = %#v", container)
	}
}

func TestConfigRejectsImplicitEligibilityOrAuthorization(t *testing.T) {
	t.Parallel()
	for name, mutate := range map[string]func(*Config){
		"node selection": func(config *Config) { config.NodeSelector = nil },
		"namespace":      func(config *Config) { config.AllowedNamespaces = nil },
		"policy":         func(config *Config) { config.ActuationPolicies = nil },
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
	return Config{
		Image: "daclab/chill-node-actuator:test", ImagePullPolicy: corev1.PullIfNotPresent,
		ServiceAccountName: "chill-node-actuator", MetricsBindAddress: "0", HealthProbePort: 8081,
		LeaseDurationSeconds: 30, SafetyValidFor: "1m", AllowedNamespaces: []string{"kube-system"},
		ActuationPolicies: []actuatorcore.ActuationPolicySpec{{
			Name: "xavier-nvpmodel-v1", DeviceClass: "jetson-xavier-nx-8g",
			PowerMode: "MODE_15W_6CORE", ActuatorKind: actuatorcore.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		}},
		NodeSelector:    map[string]string{"edge.dacs.io/node-actuation": "enabled"},
		SecurityContext: &corev1.SecurityContext{},
	}
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
