package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"sigs.k8s.io/yaml"
)

const maximumBatchEnergyOptimizationPreflightBytes int64 = 4 << 20

type ownerBatchEnergyOptimizationPreflightQuery struct {
	*ownerClient
	manifest []byte
}

func newBatchEnergyOptimizationPreflightQuery(opts operationOptions) (batchEnergyOptimizationPreflightQuery, error) {
	manifest, manifestNamespace, err := readBatchEnergyOptimizationManifest(opts.file)
	if err != nil {
		return nil, err
	}
	config, defaultNamespace, err := loadKubernetesConfig(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	if opts.namespace == "" {
		opts.namespace = manifestNamespace
	}
	if opts.namespace == "" {
		opts.namespace = defaultNamespace
	}
	owner, err := newOwnerHTTPSClient(opts, config)
	if err != nil {
		return nil, err
	}
	return &ownerBatchEnergyOptimizationPreflightQuery{ownerClient: owner, manifest: manifest}, nil
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

func (q *ownerBatchEnergyOptimizationPreflightQuery) Read(
	ctx context.Context,
) (operatorconsole.BatchEnergyOptimizationPreflight, error) {
	var preflight operatorconsole.BatchEnergyOptimizationPreflight
	path := "/api/v1/namespaces/" + url.PathEscape(q.opts.namespace) + "/batch-energy-optimizations/preflight"
	if err := q.read(ctx, http.MethodPost, path, q.manifest, "",
		maximumBatchEnergyOptimizationPreflightBytes, &preflight); err != nil {
		return preflight, err
	}
	if preflight.APIVersion != operatorconsole.BatchEnergyOptimizationPreflightAPIVersion ||
		preflight.Kind != "BatchEnergyOptimizationPreflight" || preflight.Campaign.Namespace != q.opts.namespace {
		return preflight, errors.New("owner returned an unexpected preflight identity")
	}
	return preflight, nil
}
