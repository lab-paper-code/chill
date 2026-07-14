package profiler

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
)

func TestOptionsDefaultAndValidate(t *testing.T) {
	options := Options{Namespace: "chill-system"}
	if err := options.DefaultAndValidate(); err != nil {
		t.Fatal(err)
	}
	if options.SystemName != "chill" || options.DeploymentName != "chill-profiler" ||
		options.ConfigMapName != "chill-profiler-config" || options.ConfigMapKey != DefaultConfigKey {
		t.Fatalf("defaulted options = %#v", options)
	}
}

func TestConfigValidate(t *testing.T) {
	config := validConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	config.Replicas = 0
	if err := config.Validate(); err == nil {
		t.Fatal("zero replicas accepted")
	}
}

func TestBuildDeployment(t *testing.T) {
	deployment := buildDeployment(validOptions(), validConfig())
	if deployment.Namespace != "chill-system" || deployment.Name != "chill-profiler" {
		t.Fatalf("Deployment key = %s/%s", deployment.Namespace, deployment.Name)
	}
	if deployment.Spec.Template.Spec.ServiceAccountName != "chill-profiler" {
		t.Fatalf("ServiceAccountName = %q", deployment.Spec.Template.Spec.ServiceAccountName)
	}
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.Image != "daclab/chill-profiler:test" || container.Command[0] != "/profiler" {
		t.Fatalf("container = %#v", container)
	}
	if !containsArg(container.Args, "--leader-elect") {
		t.Fatalf("args = %v", container.Args)
	}
	if container.ReadinessProbe.HTTPGet.Path != "/readyz" || container.ReadinessProbe.HTTPGet.Port.IntVal != 8081 {
		t.Fatalf("readiness probe = %#v", container.ReadinessProbe)
	}
}

func TestReconcileCreatesAndDeletesOwnedDeployment(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	configData, err := yaml.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	system := &edgev1alpha1.ChillSystem{}
	system.Name = "chill"
	system.Spec.Profiler.Enabled = true
	system.Spec.Profiler.ConfigRef.Name = "custom-profiler-config"
	configMap := &corev1.ConfigMap{}
	configMap.Namespace = "chill-system"
	configMap.Name = "custom-profiler-config"
	configMap.Data = map[string]string{DefaultConfigKey: string(configData)}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, configMap).Build()
	reconciler := &Reconciler{Client: client, Scheme: scheme, Options: Options{SystemName: "chill", Namespace: "chill-system"}}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: "chill"}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile(create): %v", err)
	}

	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Namespace: "chill-system", Name: "chill-profiler"}
	if err := client.Get(context.Background(), key, deployment); err != nil {
		t.Fatalf("get created Deployment: %v", err)
	}
	if len(deployment.OwnerReferences) != 1 || deployment.OwnerReferences[0].Name != "chill" || !*deployment.OwnerReferences[0].Controller {
		t.Fatalf("owner references = %#v", deployment.OwnerReferences)
	}
	if err := client.Get(context.Background(), types.NamespacedName{Name: "chill"}, system); err != nil {
		t.Fatal(err)
	}
	system.Spec.Profiler.Enabled = false
	if err := client.Update(context.Background(), system); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatalf("Reconcile(delete): %v", err)
	}
	if err := client.Get(context.Background(), key, deployment); err == nil {
		t.Fatal("Profiler Deployment still exists after disable")
	}
}

func validOptions() Options {
	return Options{
		SystemName:     "chill",
		Namespace:      "chill-system",
		DeploymentName: "chill-profiler",
		ConfigMapName:  "chill-profiler-config",
		ConfigMapKey:   DefaultConfigKey,
	}
}

func validConfig() Config {
	return Config{
		Replicas:           1,
		Image:              "daclab/chill-profiler:test",
		ImagePullPolicy:    corev1.PullIfNotPresent,
		ServiceAccountName: "chill-profiler",
		MetricsBindAddress: "0",
		HealthProbePort:    8081,
		LeaderElection:     true,
		LeaderElectionID:   "profiler.edge.dacs.io",
	}
}

func containsArg(args []string, wanted string) bool {
	for _, arg := range args {
		if arg == wanted {
			return true
		}
	}
	return false
}
