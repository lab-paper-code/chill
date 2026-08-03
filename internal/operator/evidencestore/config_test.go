package evidencestore

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/lab-paper-code/chill/internal/component"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestConfigValidate(t *testing.T) {
	config := validConfig()
	if err := config.Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	config.Image = "versity/versitygw:latest"
	config.BootstrapImage = ""
	config.ImagePullPolicy = corev1.PullPolicy("Sometimes")
	config.Bucket = "192.168.1.1"
	config.StorageSize = resource.Quantity{}
	err := config.Validate()
	if err == nil {
		t.Fatal("invalid config accepted")
	}
	for _, wanted := range []string{
		"image must be repository@sha256",
		"bootstrapImage is required",
		"imagePullPolicy must be Always, IfNotPresent, or Never",
		"bucket must be a valid S3 bucket name",
		"storageSize must be positive",
	} {
		if !strings.Contains(err.Error(), wanted) {
			t.Fatalf("Validate() error = %q, want %q", err, wanted)
		}
	}
}

func TestValidBucketName(t *testing.T) {
	for _, name := range []string{"chill-profiling-evidence", "evidence.v1", "abc"} {
		if !validBucketName(name) {
			t.Errorf("validBucketName(%q) = false", name)
		}
	}
	for _, name := range []string{
		"ab",
		"Uppercase",
		"bucket..name",
		"bucket.-name",
		"192.168.1.1",
		"xn--bucket",
		"sthree-bucket",
		"amzn-s3-demo-bucket",
		"bucket-s3alias",
		"bucket--ol-s3",
		"bucket.mrap",
		"bucket--x-s3",
		"bucket--table-s3",
		"bucket-an",
	} {
		if validBucketName(name) {
			t.Errorf("validBucketName(%q) = true", name)
		}
	}
}

func TestParseConfig(t *testing.T) {
	raw := []byte(`
image: docker.io/versity/versitygw@` + testDigest + `
bootstrapImage: registry.local/chill/evidence-store-client@` + testDigest + `
imagePullPolicy: IfNotPresent
bucket: chill-profiling-evidence
region: us-east-1
storageClassName: rook-cephfs
storageSize: 1Gi
resources:
  requests:
    cpu: 100m
    memory: 256Mi
`)
	config, err := parseConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if config.Bucket != "chill-profiling-evidence" || config.StorageSize.Cmp(resource.MustParse("1Gi")) != 0 {
		t.Fatalf("parsed config = %#v", config)
	}
	if config.Resources.Requests.Cpu().Cmp(resource.MustParse("100m")) != 0 {
		t.Fatalf("parsed resources = %#v", config.Resources)
	}
}

func TestParseConfigRejectsUnknownFields(t *testing.T) {
	raw := []byte(`
image: docker.io/versity/versitygw@` + testDigest + `
bootstrapImage: registry.local/chill/evidence-store-client@` + testDigest + `
imagePullPolicy: IfNotPresent
bucket: chill-profiling-evidence
region: us-east-1
storageSize: 1Gi
unexpectedPolicy: true
`)
	if _, err := parseConfig(raw); err == nil || !strings.Contains(err.Error(), "unexpectedPolicy") {
		t.Fatalf("parseConfig() error = %v, want unknown-field error", err)
	}
}

func TestLoadConfigReadsResolvedConfigMap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "custom-store-config"},
		Data: map[string]string{DefaultConfigKey: `
image: docker.io/versity/versitygw@` + testDigest + `
bootstrapImage: registry.local/chill/evidence-store-client@` + testDigest + `
imagePullPolicy: IfNotPresent
bucket: chill-profiling-evidence
region: us-east-1
storageSize: 1Gi
`},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
	instance := Instance{
		Namespace:     configMap.Namespace,
		ConfigMapName: configMap.Name,
		ConfigMapKey:  DefaultConfigKey,
	}

	config, configDigest, err := loadConfig(context.Background(), reader, instance)
	if err != nil {
		t.Fatal(err)
	}
	if config.Bucket != "chill-profiling-evidence" {
		t.Fatalf("Bucket = %q", config.Bucket)
	}
	if configDigest != component.EvidenceStoreConfigDigest(configMap.Data[DefaultConfigKey]) {
		t.Fatalf("config digest = %q", configDigest)
	}
}

func TestLoadConfigRejectsMissingDataKey(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "chill-system", Name: "store-config"},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
	instance := Instance{
		Namespace:     configMap.Namespace,
		ConfigMapName: configMap.Name,
		ConfigMapKey:  DefaultConfigKey,
	}

	if _, _, err := loadConfig(context.Background(), reader, instance); err == nil ||
		!strings.Contains(err.Error(), `missing key "config.yaml"`) {
		t.Fatalf("loadConfig() error = %v, want missing-key error", err)
	}
}

func validConfig() Config {
	return Config{
		Image:            "docker.io/versity/versitygw@" + testDigest,
		BootstrapImage:   "registry.local/chill/evidence-store-client@" + testDigest,
		ImagePullPolicy:  corev1.PullIfNotPresent,
		Bucket:           "chill-profiling-evidence",
		Region:           "us-east-1",
		StorageClassName: "rook-cephfs",
		StorageSize:      resource.MustParse("1Gi"),
	}
}
