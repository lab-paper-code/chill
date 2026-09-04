package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const maximumBatchEnergyOptimizationPreflightBytes int64 = 4 << 20

type serviceProxyBatchEnergyOptimizationPreflightQuery struct {
	restClient rest.Interface
	namespace  string
	service    string
	manifest   []byte
}

func newBatchEnergyOptimizationPreflightQuery(opts preflightOptions) (batchEnergyOptimizationPreflightQuery, error) {
	manifest, manifestNamespace, err := readBatchEnergyOptimizationManifest(opts.file)
	if err != nil {
		return nil, err
	}
	config, defaultNamespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	namespace := opts.namespace
	if namespace == "" {
		namespace = manifestNamespace
	}
	if namespace == "" {
		namespace = defaultNamespace
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("construct Kubernetes Service proxy client: %w", err)
	}
	return &serviceProxyBatchEnergyOptimizationPreflightQuery{
		restClient: clientset.CoreV1().RESTClient(), namespace: namespace,
		service: opts.consoleService, manifest: manifest,
	}, nil
}

func readBatchEnergyOptimizationManifest(path string) ([]byte, string, error) {
	var (
		raw []byte
		err error
	)
	if path == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, "", fmt.Errorf("read BatchEnergyOptimization manifest %q: %w", path, err)
	}
	manifest, err := yaml.YAMLToJSON(raw)
	if err != nil {
		return nil, "", fmt.Errorf("decode BatchEnergyOptimization manifest %q: %w", path, err)
	}
	var identity struct {
		Metadata struct {
			Namespace string `json:"namespace"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(manifest, &identity); err != nil {
		return nil, "", fmt.Errorf("decode BatchEnergyOptimization manifest %q: %w", path, err)
	}
	return manifest, identity.Metadata.Namespace, nil
}

func (q *serviceProxyBatchEnergyOptimizationPreflightQuery) Read(
	ctx context.Context,
) (operatorconsole.BatchEnergyOptimizationPreflight, error) {
	stream, err := q.restClient.Post().
		Namespace(q.namespace).
		Resource("services").
		Name(q.service+":http").
		SubResource("proxy").
		Suffix("api", "v1", "namespaces", q.namespace, "batch-energy-optimizations", "preflight").
		SetHeader("Content-Type", "application/json").
		Body(q.manifest).
		Stream(ctx)
	if err != nil {
		return operatorconsole.BatchEnergyOptimizationPreflight{}, fmt.Errorf(
			"read BatchEnergyOptimization preflight through Console Service: %w", err,
		)
	}
	payload, readErr := io.ReadAll(io.LimitReader(stream, maximumBatchEnergyOptimizationPreflightBytes+1))
	closeErr := stream.Close()
	if readErr != nil {
		return operatorconsole.BatchEnergyOptimizationPreflight{}, fmt.Errorf("read Console response: %w", readErr)
	}
	if closeErr != nil {
		return operatorconsole.BatchEnergyOptimizationPreflight{}, fmt.Errorf("close Console response: %w", closeErr)
	}
	if int64(len(payload)) > maximumBatchEnergyOptimizationPreflightBytes {
		return operatorconsole.BatchEnergyOptimizationPreflight{}, errors.New(
			"Console response exceeds the BatchEnergyOptimization preflight size limit",
		)
	}
	var preflight operatorconsole.BatchEnergyOptimizationPreflight
	if err := json.Unmarshal(payload, &preflight); err != nil {
		return operatorconsole.BatchEnergyOptimizationPreflight{}, fmt.Errorf(
			"decode Console BatchEnergyOptimization preflight: %w", err,
		)
	}
	if preflight.APIVersion != operatorconsole.BatchEnergyOptimizationPreflightAPIVersion ||
		preflight.Kind != "BatchEnergyOptimizationPreflight" || preflight.Campaign.Namespace != q.namespace {
		return operatorconsole.BatchEnergyOptimizationPreflight{}, errors.New(
			"Console returned an unexpected preflight identity",
		)
	}
	return preflight, nil
}
