package kube

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/component"
	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
)

type EvidenceStoreResolverOptions struct {
	SystemName string
	Namespace  string
}

// KubernetesEvidenceStoreResolver exact-reads the owning ChillSystem and its
// configured operand data. It derives Secret identity without reading bytes.
type KubernetesEvidenceStoreResolver struct {
	reader  client.Reader
	options EvidenceStoreResolverOptions
}

func NewKubernetesEvidenceStoreResolver(
	reader client.Reader,
	options EvidenceStoreResolverOptions,
) (*KubernetesEvidenceStoreResolver, error) {
	if reader == nil {
		return nil, errors.New("Kubernetes Evidence Store reader is required")
	}
	for name, value := range map[string]string{
		"system name": options.SystemName,
		"namespace":   options.Namespace,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return nil, fmt.Errorf("Evidence Store %s is required without surrounding whitespace", name)
		}
	}
	return &KubernetesEvidenceStoreResolver{reader: reader, options: options}, nil
}

// evidenceStoreDeliveryConfig is deliberately a projection of the
// operator-owned operand configuration, not a second copy of that schema.
type evidenceStoreDeliveryConfig struct {
	Bucket string `json:"bucket"`
	Region string `json:"region"`
}

func (r *KubernetesEvidenceStoreResolver) Resolve(ctx context.Context) (storekube.Binding, error) {
	system := &edgev1alpha1.ChillSystem{}
	if err := r.reader.Get(ctx, types.NamespacedName{Name: r.options.SystemName}, system); err != nil {
		return storekube.Binding{}, fmt.Errorf("get ChillSystem %q: %w", r.options.SystemName, err)
	}
	if !system.Spec.EvidenceStore.Enabled {
		return storekube.Binding{}, errors.New("owning ChillSystem does not enable Evidence Store")
	}
	if err := requireEvidenceStoreAvailable(system); err != nil {
		return storekube.Binding{}, err
	}
	namespace := system.Spec.ManagementNamespace
	if strings.TrimSpace(namespace) != namespace {
		return storekube.Binding{}, errors.New("owning ChillSystem management namespace is invalid")
	}
	if namespace != "" && namespace != r.options.Namespace {
		return storekube.Binding{}, errors.New("Profiler namespace does not match the owning ChillSystem management namespace")
	}

	configMapName := system.Spec.EvidenceStore.ConfigRef.Name
	if strings.TrimSpace(configMapName) == "" || strings.TrimSpace(configMapName) != configMapName {
		return storekube.Binding{}, errors.New("owning ChillSystem Evidence Store config reference is invalid")
	}
	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{Namespace: r.options.Namespace, Name: configMapName}
	if err := r.reader.Get(ctx, key, configMap); err != nil {
		return storekube.Binding{}, fmt.Errorf("get Evidence Store config ConfigMap %s: %w", key, err)
	}
	raw, exists := configMap.Data[component.EvidenceStoreConfigKey]
	if !exists || strings.TrimSpace(raw) == "" {
		return storekube.Binding{}, fmt.Errorf(
			"Evidence Store config ConfigMap %s missing key %q", key, component.EvidenceStoreConfigKey,
		)
	}
	var config evidenceStoreDeliveryConfig
	if err := yaml.Unmarshal([]byte(raw), &config); err != nil {
		return storekube.Binding{}, fmt.Errorf("decode Evidence Store delivery configuration: %w", err)
	}
	for name, value := range map[string]string{"bucket": config.Bucket, "region": config.Region} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return storekube.Binding{}, fmt.Errorf(
				"Evidence Store delivery %s is required without surrounding whitespace", name,
			)
		}
	}
	bootstrapJob := &batchv1.Job{}
	bootstrapKey := types.NamespacedName{
		Namespace: r.options.Namespace,
		Name:      component.EvidenceStoreBootstrapJobName(system.Name),
	}
	if err := r.reader.Get(ctx, bootstrapKey, bootstrapJob); err != nil {
		return storekube.Binding{}, fmt.Errorf("get Evidence Store bootstrap Job %s: %w", bootstrapKey, err)
	}
	if err := requireCurrentBootstrap(system, bootstrapJob, configMap.ResourceVersion); err != nil {
		return storekube.Binding{}, err
	}

	serviceName := component.EvidenceStoreServiceName(system.Name)
	service := &corev1.Service{}
	serviceKey := types.NamespacedName{Namespace: r.options.Namespace, Name: serviceName}
	if err := r.reader.Get(ctx, serviceKey, service); err != nil {
		return storekube.Binding{}, fmt.Errorf("get Evidence Store Service %s: %w", serviceKey, err)
	}
	host, err := requireEvidenceStoreServiceAddress(system, service)
	if err != nil {
		return storekube.Binding{}, err
	}
	return storekube.Binding{Store: serviceName,
		Endpoint:              "http://" + net.JoinHostPort(host, strconv.Itoa(int(component.EvidenceStoreS3Port))),
		Bucket:                config.Bucket,
		Region:                config.Region,
		CredentialsSecretName: component.EvidenceStoreCredentialsSecretName(system.Name)}, nil
}

func requireEvidenceStoreAvailable(system *edgev1alpha1.ChillSystem) error {
	condition := meta.FindStatusCondition(
		system.Status.Conditions,
		edgev1alpha1.ChillSystemConditionEvidenceStoreAvailable,
	)
	if system.Status.ObservedGeneration != system.Generation || condition == nil ||
		condition.ObservedGeneration != system.Generation || condition.Status != metav1.ConditionTrue {
		return errors.New("owning ChillSystem Evidence Store is not available for the current generation")
	}
	return nil
}

// requireEvidenceStoreServiceAddress returns the allocated ClusterIP of the
// current system-owned Service that exposes the S3 TCP port.
func requireEvidenceStoreServiceAddress(system *edgev1alpha1.ChillSystem, service *corev1.Service) (string, error) {
	if !metav1.IsControlledBy(service, system) || !service.DeletionTimestamp.IsZero() {
		return "", errors.New("Evidence Store Service ownership or lifecycle is invalid")
	}
	ip := service.Spec.ClusterIP
	if ip == corev1.ClusterIPNone || net.ParseIP(ip) == nil {
		return "", errors.New("Evidence Store Service has no allocated valid ClusterIP")
	}
	for _, port := range service.Spec.Ports {
		protocol := port.Protocol
		if protocol == "" {
			protocol = corev1.ProtocolTCP
		}
		if port.Port == component.EvidenceStoreS3Port && protocol == corev1.ProtocolTCP {
			return ip, nil
		}
	}
	return "", fmt.Errorf("Evidence Store Service does not expose TCP port %d", component.EvidenceStoreS3Port)
}

func requireCurrentBootstrap(
	system *edgev1alpha1.ChillSystem,
	job *batchv1.Job,
	configResourceVersion string,
) error {
	if !metav1.IsControlledBy(job, system) || !job.DeletionTimestamp.IsZero() {
		return errors.New("Evidence Store bootstrap Job ownership or lifecycle is invalid")
	}
	if configResourceVersion == "" ||
		job.Annotations[component.EvidenceStoreConfigResourceVersionAnnotation] != configResourceVersion {
		return errors.New("Evidence Store bootstrap does not attest the current configuration")
	}
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			return nil
		}
	}
	return errors.New("Evidence Store bootstrap for the current configuration is not complete")
}
