package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/lab-paper-code/chill/internal/operatorconsole"
)

type inventoryQuery interface {
	Read(context.Context) (operatorconsole.InventorySnapshot, error)
}

type systemQuery interface {
	Read(context.Context) (operatorconsole.SystemSnapshot, error)
}

type dependencies struct {
	newInventoryQuery func(inventoryOptions) (inventoryQuery, error)
	newSystemQuery    func(systemOptions) (systemQuery, error)
}

func productionDependencies() dependencies {
	return dependencies{newInventoryQuery: newInventoryQuery, newSystemQuery: newSystemQuery}
}

func execute(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected command: inventory or system", errUsage)
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return writeUsage(stdout)
	}
	switch args[0] {
	case "inventory":
		return executeInventory(ctx, args[1:], stdout, stderr, deps)
	case "system":
		return executeSystem(ctx, args[1:], stdout, stderr, deps)
	default:
		return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
	}
}

func executeInventory(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	opts, err := parseInventoryOptions(args, stderr)
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

func executeSystem(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	opts, err := parseSystemOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	query, err := deps.newSystemQuery(opts)
	if err != nil {
		return fmt.Errorf("construct system query: %w", err)
	}
	snapshot, err := query.Read(ctx)
	if err != nil {
		return fmt.Errorf("read systems: %w", err)
	}
	if err := writeSystems(stdout, opts.output, snapshot); err != nil {
		return fmt.Errorf("write systems: %w", err)
	}
	return nil
}

func writeUsage(writer io.Writer) error {
	if _, err := fmt.Fprintln(writer, "Usage:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer, "  kubectl chill inventory [--kubeconfig PATH] [--context NAME] [-o table|json]"); err != nil {
		return err
	}
	_, err := fmt.Fprintln(writer, "  kubectl chill system    [--kubeconfig PATH] [--context NAME] [-o table|json]")
	return err
}

func newInventoryQuery(opts inventoryOptions) (inventoryQuery, error) {
	kubeClient, err := newKubernetesReader(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	query, err := operatorconsole.NewKubernetesInventoryQuery(kubeClient)
	if err != nil {
		return nil, fmt.Errorf("construct read-through query: %w", err)
	}
	return query, nil
}

func newSystemQuery(opts systemOptions) (systemQuery, error) {
	kubeClient, err := newKubernetesReader(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	query, err := operatorconsole.NewKubernetesSystemQuery(kubeClient)
	if err != nil {
		return nil, fmt.Errorf("construct read-through query: %w", err)
	}
	return query, nil
}
