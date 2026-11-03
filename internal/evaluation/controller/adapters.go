package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	storekube "github.com/lab-paper-code/chill/internal/evidencestore/kube"
	stores3 "github.com/lab-paper-code/chill/internal/evidencestore/s3"
)

// NewRuntime resolves only the run's frozen binding, never current operator config.
// stagingDir must be writable: the Profiler root filesystem is read-only.
func NewRuntime(reader client.Reader, stagingDir string) Runtime {
	return Runtime{
		Store: func(ctx context.Context, namespace, logicalStore string, v map[string]string) (ObjectStore, error) {
			credentials, err := storekube.LoadCredentials(ctx, reader, namespace, v["evidenceCredentialsSecret"])
			if err != nil {
				return nil, err
			}
			return stores3.New(stores3.Config{LogicalStore: logicalStore, Endpoint: v["evidenceEndpoint"], Bucket: v["evidenceBucket"], Region: v["evidenceRegion"], AccessKeyID: credentials.AccessKeyID, SecretAccessKey: credentials.SecretAccessKey, TempDir: stagingDir})
		},
	}
}
