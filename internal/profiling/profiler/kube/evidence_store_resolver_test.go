package kube

import (
	"context"
	"strings"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
)

func TestKubernetesEvidenceStoreResolverResolvesNonSecretDeliveryBinding(t *testing.T) {
	resolver := newEvidenceStoreResolver(t, validEvidenceStoreObjects()...)
	binding, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if binding.Store != "chill-evidence-store" ||
		binding.Endpoint != "http://10.97.177.19:9000" ||
		binding.Bucket != "chill-profiling-evidence" || binding.Region != "us-east-1" ||
		binding.CredentialsSecretName != "chill-evidence-store-credentials" {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestKubernetesEvidenceStoreResolverRejectsUnavailableOrInvalidBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ChillSystem, *corev1.ConfigMap)
		want   string
	}{
		{
			name: "disabled",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.EvidenceStore.Enabled = false
			},
			want: "does not enable",
		},
		{
			name: "namespace drift",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.ManagementNamespace = "another-system"
			},
			want: "does not match",
		},
		{
			name: "invalid namespace",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.ManagementNamespace = " chill-system "
			},
			want: "management namespace is invalid",
		},
		{
			name: "invalid config reference",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap) {
				system.Spec.EvidenceStore.ConfigRef.Name = " "
			},
			want: "config reference is invalid",
		},
		{
			name: "missing config key",
			mutate: func(_ *edgev1alpha1.ChillSystem, config *corev1.ConfigMap) {
				delete(config.Data, "config.yaml")
			},
			want: "missing key",
		},
		{
			name: "malformed config",
			mutate: func(_ *edgev1alpha1.ChillSystem, config *corev1.ConfigMap) {
				config.Data["config.yaml"] = "bucket: ["
			},
			want: "decode Evidence Store",
		},
		{
			name: "blank required delivery field",
			mutate: func(_ *edgev1alpha1.ChillSystem, config *corev1.ConfigMap) {
				config.Data["config.yaml"] = "bucket: chill-profiling-evidence\nregion: ' '"
			},
			want: "delivery region is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := validEvidenceStoreObjects()
			system := objects[0].(*edgev1alpha1.ChillSystem)
			config := objects[1].(*corev1.ConfigMap)
			test.mutate(system, config)
			resolver := newEvidenceStoreResolver(t, objects...)
			if _, err := resolver.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestKubernetesEvidenceStoreResolverRejectsStaleBootstrapAttestation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*edgev1alpha1.ChillSystem, *corev1.ConfigMap, *batchv1.Job)
		want   string
	}{
		{
			name: "stale system status",
			mutate: func(system *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap, _ *batchv1.Job) {
				system.Status.ObservedGeneration--
			},
			want: "not available for the current generation",
		},
		{
			name: "changed configuration",
			mutate: func(_ *edgev1alpha1.ChillSystem, config *corev1.ConfigMap, _ *batchv1.Job) {
				config.Data[component.EvidenceStoreConfigKey] += "\n"
				config.ResourceVersion = "8"
			},
			want: "does not attest the current configuration",
		},
		{
			name: "bootstrap incomplete",
			mutate: func(_ *edgev1alpha1.ChillSystem, _ *corev1.ConfigMap, job *batchv1.Job) {
				job.Status.Conditions = nil
			},
			want: "is not complete",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			objects := validEvidenceStoreObjects()
			system := objects[0].(*edgev1alpha1.ChillSystem)
			config := objects[1].(*corev1.ConfigMap)
			job := objects[2].(*batchv1.Job)
			test.mutate(system, config, job)
			resolver := newEvidenceStoreResolver(t, objects...)
			if _, err := resolver.Resolve(context.Background()); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestKubernetesEvidenceStoreResolverBindsAllocatedServiceAddress(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*corev1.Service)
		want   string // empty means success with wantEndpoint
		wantEP string
	}{
		{name: "ipv6", mutate: func(s *corev1.Service) { s.Spec.ClusterIP = "fd00::1" }, wantEP: "http://[fd00::1]:9000"},
		{name: "missing service", mutate: nil, want: "get Evidence Store Service"},
		{name: "headless", mutate: func(s *corev1.Service) { s.Spec.ClusterIP = corev1.ClusterIPNone }, want: "no allocated valid ClusterIP"},
		{name: "unallocated", mutate: func(s *corev1.Service) { s.Spec.ClusterIP = "" }, want: "no allocated valid ClusterIP"},
		{name: "malformed", mutate: func(s *corev1.Service) { s.Spec.ClusterIP = "chill-evidence-store" }, want: "no allocated valid ClusterIP"},
		{name: "foreign owner", mutate: func(s *corev1.Service) { s.OwnerReferences[0].UID = "other" }, want: "ownership or lifecycle"},
		{name: "wrong port", mutate: func(s *corev1.Service) { s.Spec.Ports[0].Port = 9001 }, want: "does not expose TCP port"},
		{name: "udp port", mutate: func(s *corev1.Service) { s.Spec.Ports[0].Protocol = corev1.ProtocolUDP }, want: "does not expose TCP port"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var objects []client.Object
			for _, object := range validEvidenceStoreObjects() {
				service, isService := object.(*corev1.Service)
				switch {
				case !isService:
					objects = append(objects, object)
				case test.mutate != nil:
					test.mutate(service)
					objects = append(objects, service)
				}
			}
			binding, err := newEvidenceStoreResolver(t, objects...).Resolve(context.Background())
			if test.want == "" {
				if err != nil || binding.Endpoint != test.wantEP {
					t.Fatalf("Resolve() = %#v, %v; want endpoint %q", binding, err, test.wantEP)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNewKubernetesEvidenceStoreResolverValidatesDependencies(t *testing.T) {
	if _, err := NewKubernetesEvidenceStoreResolver(nil, EvidenceStoreResolverOptions{}); err == nil {
		t.Fatal("nil reader accepted")
	}
	reader := fake.NewClientBuilder().Build()
	for _, options := range []EvidenceStoreResolverOptions{
		{Namespace: "chill-system"},
		{SystemName: "chill"},
		{SystemName: " chill ", Namespace: "chill-system"},
	} {
		if _, err := NewKubernetesEvidenceStoreResolver(reader, options); err == nil {
			t.Fatalf("invalid options accepted: %#v", options)
		}
	}
}

func newEvidenceStoreResolver(t *testing.T, objects ...client.Object) *KubernetesEvidenceStoreResolver {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	resolver, err := NewKubernetesEvidenceStoreResolver(reader, EvidenceStoreResolverOptions{
		SystemName: "chill",
		Namespace:  "chill-system",
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}

func validEvidenceStoreObjects() []client.Object {
	system := &edgev1alpha1.ChillSystem{ObjectMeta: metav1.ObjectMeta{
		Name: "chill", UID: types.UID("system-uid"), Generation: 2,
	}}
	system.Spec.ManagementNamespace = "chill-system"
	system.Spec.EvidenceStore.Enabled = true
	system.Spec.EvidenceStore.ConfigRef.Name = "chill-evidence-store-config"
	system.Status.ObservedGeneration = system.Generation
	system.Status.Conditions = []metav1.Condition{{
		Type: edgev1alpha1.ChillSystemConditionEvidenceStoreAvailable, Status: metav1.ConditionTrue,
		ObservedGeneration: system.Generation, Reason: "Ready",
	}}
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{
		Namespace:       "chill-system",
		Name:            "chill-evidence-store-config",
		ResourceVersion: "7",
	}}
	rawConfig := `
image: registry.example/evidence@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
bucket: chill-profiling-evidence
region: us-east-1
storageSize: 1Gi
`
	config.Data = map[string]string{component.EvidenceStoreConfigKey: rawConfig}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Namespace: "chill-system", Name: component.EvidenceStoreBootstrapJobName(system.Name),
		Annotations: map[string]string{
			component.EvidenceStoreConfigResourceVersionAnnotation: config.ResourceVersion,
		},
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ChillSystem", Name: system.Name,
			UID: system.UID, Controller: ptr.To(true),
		}},
	}}
	job.Status.Conditions = []batchv1.JobCondition{{
		Type: batchv1.JobComplete, Status: corev1.ConditionTrue,
	}}
	service := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "chill-system", Name: component.EvidenceStoreServiceName(system.Name),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: edgev1alpha1.GroupVersion.String(), Kind: "ChillSystem", Name: system.Name,
				UID: system.UID, Controller: ptr.To(true),
			}},
		},
		Spec: corev1.ServiceSpec{
			ClusterIP: "10.97.177.19",
			Ports: []corev1.ServicePort{{
				Name: "s3", Protocol: corev1.ProtocolTCP, Port: component.EvidenceStoreS3Port,
			}},
		},
	}
	return []client.Object{system, config, job, service}
}
