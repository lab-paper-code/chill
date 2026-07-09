package kubeconfig

import (
	"fmt"
	"strings"

	"github.com/lab-paper-code/chill/internal/defaults"
	"k8s.io/client-go/rest"
)

const (
	DefaultServiceAccountTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	DefaultServiceAccountCAFile    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

// Options describes how a workload should connect to the Kubernetes API.
type Options struct {
	APIServer string
	TokenFile string
	CAFile    string
}

// BuildConfig returns a Kubernetes REST config for regular Kubernetes pods and
// KubeEdge edge pods that cannot rely on Kubernetes service environment vars.
func BuildConfig(options Options) (*rest.Config, error) {
	if strings.TrimSpace(options.APIServer) == "" {
		config, err := rest.InClusterConfig()
		if err != nil {
			return nil, fmt.Errorf(
				"load in-cluster config: %w; for KubeEdge edge pods, enable KubeEdge in-cluster config support or set a CHILL Kubernetes API endpoint",
				err,
			)
		}
		return config, nil
	}

	tokenFile := defaults.String(options.TokenFile, DefaultServiceAccountTokenFile)
	caFile := defaults.String(options.CAFile, DefaultServiceAccountCAFile)
	return &rest.Config{
		Host:            strings.TrimSpace(options.APIServer),
		BearerTokenFile: tokenFile,
		TLSClientConfig: rest.TLSClientConfig{CAFile: caFile},
	}, nil
}
