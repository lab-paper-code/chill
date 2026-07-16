package profiler

import (
	"context"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
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

	config = validConfig()
	config.ImagePullPolicy = corev1.PullPolicy("Sometimes")
	if err := config.Validate(); err == nil {
		t.Fatal("invalid imagePullPolicy accepted")
	}

	config = validConfig()
	config.FactSource.PowerSourcesKey = config.FactSource.ExecutionFactsKey
	if err := config.Validate(); err == nil {
		t.Fatal("overlapping Profiler fact-source keys accepted")
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
	for _, wanted := range []string{
		"--fact-source-config-map=chill-profiler-facts",
		"--execution-facts-key=execution-facts.yaml",
		"--power-sources-key=power-sources.yaml",
	} {
		if !containsArg(container.Args, wanted) {
			t.Fatalf("args lost %q: %v", wanted, container.Args)
		}
	}
	if container.ReadinessProbe.HTTPGet.Path != "/readyz" || container.ReadinessProbe.HTTPGet.Port.IntVal != 8081 {
		t.Fatalf("readiness probe = %#v", container.ReadinessProbe)
	}
}

func TestReconcileCreatesAndDeletesOwnedDeployment(t *testing.T) {
	scheme := testScheme(t)

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

func TestReconcileDoesNotDeleteSameNamedDeploymentWithoutChillSystem(t *testing.T) {
	scheme := testScheme(t)
	foreign := &appsv1.Deployment{}
	foreign.Namespace = "chill-system"
	foreign.Name = "chill-profiler"
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(foreign).Build()
	reconciler := &Reconciler{Client: client, Scheme: scheme, Options: validOptions()}

	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "chill"}}); err != nil {
		t.Fatalf("Reconcile(): %v", err)
	}
	if err := client.Get(context.Background(), types.NamespacedName{Namespace: foreign.Namespace, Name: foreign.Name}, &appsv1.Deployment{}); err != nil {
		t.Fatalf("same-named foreign Deployment was deleted: %v", err)
	}
}

func TestReconcileRefusesToAdoptUncontrolledDeployment(t *testing.T) {
	scheme := testScheme(t)
	configData, err := yaml.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	system := &edgev1alpha1.ChillSystem{}
	system.Name = "chill"
	system.Spec.Profiler.Enabled = true
	configMap := &corev1.ConfigMap{}
	configMap.Namespace = "chill-system"
	configMap.Name = "chill-profiler-config"
	configMap.Data = map[string]string{DefaultConfigKey: string(configData)}
	foreign := &appsv1.Deployment{}
	foreign.Namespace = "chill-system"
	foreign.Name = "chill-profiler"

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, configMap, foreign).Build()
	reconciler := &Reconciler{Client: client, Scheme: scheme, Options: validOptions()}
	_, err = reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "chill"}})
	if err == nil || !strings.Contains(err.Error(), "without a controller reference") {
		t.Fatalf("Reconcile() error = %v, want uncontrolled Deployment error", err)
	}
}

func TestReconcileRequeuesAfterDeletingImmutableSelectorDrift(t *testing.T) {
	scheme := testScheme(t)
	configData, err := yaml.Marshal(validConfig())
	if err != nil {
		t.Fatal(err)
	}
	system := &edgev1alpha1.ChillSystem{}
	system.Name = "chill"
	system.Spec.Profiler.Enabled = true
	configMap := &corev1.ConfigMap{}
	configMap.Namespace = "chill-system"
	configMap.Name = "chill-profiler-config"
	configMap.Data = map[string]string{DefaultConfigKey: string(configData)}
	drifted := buildDeployment(validOptions(), validConfig())
	drifted.Spec.Selector.MatchLabels["drifted"] = "true"
	if err := controllerutil.SetControllerReference(system, drifted, scheme); err != nil {
		t.Fatal(err)
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(system, configMap, drifted).Build()
	reconciler := &Reconciler{Client: client, Scheme: scheme, Options: validOptions()}
	result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: "chill"}})
	if err != nil {
		t.Fatalf("Reconcile(): %v", err)
	}
	if !result.Requeue {
		t.Fatal("Reconcile() did not request recreation after selector drift")
	}
	key := types.NamespacedName{Namespace: drifted.Namespace, Name: drifted.Name}
	if err := client.Get(context.Background(), key, &appsv1.Deployment{}); !apierrors.IsNotFound(err) {
		t.Fatalf("drifted Deployment still exists: %v", err)
	}
}

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		edgev1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
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
		FactSource: FactSourceConfig{
			ConfigMapName: "chill-profiler-facts", ExecutionFactsKey: "execution-facts.yaml",
			PowerSourcesKey: "power-sources.yaml",
		},
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
