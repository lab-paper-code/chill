package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	edgev1alpha1 "github.com/lab-paper-code/chill/api/v1alpha1"
	"github.com/lab-paper-code/chill/internal/operatorconsole"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type inventoryQuery interface {
	Read(context.Context) (operatorconsole.InventorySnapshot, error)
}

type dependencies struct {
	newInventoryQuery func(inventoryOptions) (inventoryQuery, error)
}

func productionDependencies() dependencies {
	return dependencies{newInventoryQuery: newInventoryQuery}
}

func execute(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected command: inventory", errUsage)
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return writeUsage(stdout)
	}
	if args[0] != "inventory" {
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
	opts, err := parseInventoryOptions(args[1:], stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	query, err := deps.newInventoryQuery(opts)
	if err != nil {
		return fmt.Errorf("construct inventory query: %w", err)
	}
	snapshot, err := query.Read(ctx)
	if err != nil {
		return fmt.Errorf("read inventory: %w", err)
	}
	if err := writeInventory(stdout, opts.output, snapshot); err != nil {
		return fmt.Errorf("write inventory: %w", err)
	}
	return nil
}

func writeUsage(writer io.Writer) error {
	if _, err := fmt.Fprintln(writer, "Usage:"); err != nil {
		return err
	}
	_, err := fmt.Fprintln(writer, "  kubectl chill inventory [--kubeconfig PATH] [--context NAME] [-o table|json]")
	return err
}

func newInventoryQuery(opts inventoryOptions) (inventoryQuery, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if opts.kubeconfig != "" {
		rules.ExplicitPath = opts.kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: opts.context}
	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register Kubernetes APIs: %w", err)
	}
	if err := edgev1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("register CHILL API: %w", err)
	}
	kubeClient, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("construct Kubernetes client: %w", err)
	}
	query, err := operatorconsole.NewKubernetesInventoryQuery(kubeClient)
	if err != nil {
		return nil, fmt.Errorf("construct read-through query: %w", err)
	}
	return query, nil
}
