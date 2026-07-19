package main

import (
	"fmt"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func newKubernetesReader(kubeconfig, contextName string) (client.Reader, error) {
	reader, _, err := newKubernetesAccess(kubeconfig, contextName)
	return reader, err
}

func newKubernetesAccess(kubeconfig, contextName string) (client.Reader, string, error) {
	restConfig, namespace, err := loadKubernetesConfig(kubeconfig, contextName)
	if err != nil {
		return nil, "", err
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, "", fmt.Errorf("register Kubernetes APIs: %w", err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		return nil, "", fmt.Errorf("register CHILL API: %w", err)
	}
	kubeClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("construct Kubernetes client: %w", err)
	}
	return kubeClient, namespace, nil
}

func loadKubernetesConfig(kubeconfig, contextName string) (*rest.Config, string, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: contextName}
	loadingConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
	restConfig, err := loadingConfig.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("load kubeconfig: %w", err)
	}
	namespace, _, err := loadingConfig.Namespace()
	if err != nil {
		return nil, "", fmt.Errorf("resolve kubeconfig namespace: %w", err)
	}
	return restConfig, namespace, nil
}
