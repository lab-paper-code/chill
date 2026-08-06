package evidencestore

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEnsureCredentialsSecretCreatesAndReusesImmutableSecret(t *testing.T) {
	ctx := context.Background()
	instance := validInstance()
	k8sClient := fake.NewClientBuilder().WithScheme(secretTestScheme(t)).Build()

	createdSecret, created, err := ensureCredentialsSecret(ctx, k8sClient, instance)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first ensure did not report creation")
	}
	if createdSecret.Immutable == nil || !*createdSecret.Immutable {
		t.Fatal("created Secret is mutable")
	}
	if len(createdSecret.OwnerReferences) != 0 {
		t.Fatalf("retained Secret ownerReferences = %#v", createdSecret.OwnerReferences)
	}
	if err := validateCredentialsSecret(createdSecret, instance); err != nil {
		t.Fatalf("created Secret validation: %v", err)
	}
	reusedSecret, created, err := ensureCredentialsSecret(ctx, k8sClient, instance)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second ensure reported creation")
	}
	if err := validateCredentialsSecret(reusedSecret, instance); err != nil {
		t.Fatalf("reused Secret validation: %v", err)
	}
}

func TestEnsureCredentialsSecretRejectsForeignSameNamedSecret(t *testing.T) {
	ctx := context.Background()
	instance := validInstance()
	foreign, err := newCredentialsSecret(instance, bytes.NewReader(make([]byte, accessKeyBytes+secretKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	delete(foreign.Labels, "app.kubernetes.io/managed-by")
	k8sClient := fake.NewClientBuilder().WithScheme(secretTestScheme(t)).WithObjects(foreign).Build()

	if _, _, err := ensureCredentialsSecret(ctx, k8sClient, instance); err == nil ||
		!strings.Contains(err.Error(), "not managed by this ChillSystem") {
		t.Fatalf("ensureCredentialsSecret() error = %v, want ownership error", err)
	}
}

func TestEnsureCredentialsSecretRejectsMalformedRetainedData(t *testing.T) {
	ctx := context.Background()
	instance := validInstance()
	secret, err := newCredentialsSecret(instance, bytes.NewReader(make([]byte, accessKeyBytes+secretKeyBytes)))
	if err != nil {
		t.Fatal(err)
	}
	secret.Data[secretKeySecretKey] = []byte("not-a-valid-secret")
	k8sClient := fake.NewClientBuilder().WithScheme(secretTestScheme(t)).WithObjects(secret).Build()

	if _, _, err := ensureCredentialsSecret(ctx, k8sClient, instance); err == nil ||
		!strings.Contains(err.Error(), `invalid "secret-key" data`) {
		t.Fatalf("ensureCredentialsSecret() error = %v, want malformed-data error", err)
	}
}

func TestNewCredentialsSecretPropagatesRandomSourceFailure(t *testing.T) {
	_, err := newCredentialsSecret(validInstance(), errorReader{})
	if err == nil || !strings.Contains(err.Error(), "random source failed") {
		t.Fatalf("newCredentialsSecret() error = %v", err)
	}
}

func secretTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	return scheme
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("random source failed")
}
