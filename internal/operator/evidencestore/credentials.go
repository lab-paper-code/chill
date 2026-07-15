package evidencestore

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	accessKeyBytes = 10
	secretKeyBytes = 32
)

type credentialsSecretClient interface {
	Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error
	Create(context.Context, client.Object, ...client.CreateOption) error
}

// ensureCredentialsSecret creates immutable credentials once and reuses the
// retained Secret on subsequent reconciles.
func ensureCredentialsSecret(
	ctx context.Context,
	k8sClient credentialsSecretClient,
	instance Instance,
) (*corev1.Secret, bool, error) {
	key := types.NamespacedName{Namespace: instance.Namespace, Name: instance.CredentialsSecretName}
	current := &corev1.Secret{}
	if err := k8sClient.Get(ctx, key, current); err == nil {
		if err := validateCredentialsSecret(current, instance); err != nil {
			return nil, false, err
		}
		return current, false, nil
	} else if !apierrors.IsNotFound(err) {
		return nil, false, fmt.Errorf("get Evidence Store credentials Secret %s: %w", key.String(), err)
	}

	desired, err := newCredentialsSecret(instance, rand.Reader)
	if err != nil {
		return nil, false, fmt.Errorf("generate Evidence Store credentials: %w", err)
	}
	if err := k8sClient.Create(ctx, desired); err != nil {
		if !apierrors.IsAlreadyExists(err) {
			return nil, false, fmt.Errorf("create Evidence Store credentials Secret %s: %w", key.String(), err)
		}
		current = &corev1.Secret{}
		if err := k8sClient.Get(ctx, key, current); err != nil {
			return nil, false, fmt.Errorf("get concurrently created Evidence Store credentials Secret %s: %w", key.String(), err)
		}
		if err := validateCredentialsSecret(current, instance); err != nil {
			return nil, false, err
		}
		return current, false, nil
	}
	return desired, true, nil
}

func newCredentialsSecret(instance Instance, random io.Reader) (*corev1.Secret, error) {
	accessKey, err := randomHex(random, accessKeyBytes)
	if err != nil {
		return nil, err
	}
	secretKey, err := randomHex(random, secretKeyBytes)
	if err != nil {
		return nil, err
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: instance.Namespace,
			Name:      instance.CredentialsSecretName,
			Labels:    operandLabels(instance.SystemName),
		},
		Immutable: ptr.To(true),
		Type:      corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			accessKeySecretKey: []byte(accessKey),
			secretKeySecretKey: []byte(secretKey),
		},
	}, nil
}

func randomHex(random io.Reader, bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func validateCredentialsSecret(secret *corev1.Secret, instance Instance) error {
	key := types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}
	expectedLabels := operandLabels(instance.SystemName)
	for label, expected := range expectedLabels {
		if secret.Labels[label] != expected {
			return fmt.Errorf("Evidence Store credentials Secret %s is not managed by this ChillSystem", key.String())
		}
	}
	if secret.Type != corev1.SecretTypeOpaque {
		return fmt.Errorf("Evidence Store credentials Secret %s must have type Opaque", key.String())
	}
	if secret.Immutable == nil || !*secret.Immutable {
		return fmt.Errorf("Evidence Store credentials Secret %s must be immutable", key.String())
	}
	for name, encodedLength := range map[string]int{
		accessKeySecretKey: hex.EncodedLen(accessKeyBytes),
		secretKeySecretKey: hex.EncodedLen(secretKeyBytes),
	} {
		value := secret.Data[name]
		if len(value) != encodedLength {
			return fmt.Errorf("Evidence Store credentials Secret %s has invalid %q data", key.String(), name)
		}
		if _, err := hex.DecodeString(string(value)); err != nil {
			return fmt.Errorf("Evidence Store credentials Secret %s has invalid %q data", key.String(), name)
		}
	}
	return nil
}
