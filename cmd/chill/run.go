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

type profilingRunsQuery interface {
	Read(context.Context) (operatorconsole.ProfilingRunsSnapshot, error)
}

type dependencies struct {
	newInventoryQuery func(inventoryOptions) (inventoryQuery, error)
	newSystemQuery    func(systemOptions) (systemQuery, error)
	newRunsQuery      func(profilingRunsOptions) (profilingRunsQuery, error)
	newRunShowQuery   func(profilingRunShowOptions) (evidencePassportQuery, error)
}

func productionDependencies() dependencies {
	return dependencies{
		newInventoryQuery: newInventoryQuery,
		newSystemQuery:    newSystemQuery,
		newRunsQuery:      newProfilingRunsQuery,
		newRunShowQuery:   newEvidencePassportQuery,
	}
}

func execute(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: expected command: inventory, system, or runs", errUsage)
	}
	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		return writeUsage(stdout)
	}
	switch args[0] {
	case "inventory":
		return executeInventory(ctx, args[1:], stdout, stderr, deps)
	case "system":
		return executeSystem(ctx, args[1:], stdout, stderr, deps)
	case "runs":
		return executeProfilingRuns(ctx, args[1:], stdout, stderr, deps)
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
	if err := writeInventory(stdout, opts.output, operatorconsole.BuildInventory(snapshot)); err != nil {
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
	if err := writeSystems(stdout, opts.output, operatorconsole.BuildSystems(snapshot)); err != nil {
		return fmt.Errorf("write systems: %w", err)
	}
	return nil
}

func executeProfilingRuns(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	if len(args) > 0 && args[0] == "show" {
		return executeProfilingRunShow(ctx, args[1:], stdout, stderr, deps)
	}
	opts, err := parseProfilingRunsOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	query, err := deps.newRunsQuery(opts)
	if err != nil {
		return fmt.Errorf("construct ProfilingRun query: %w", err)
	}
	snapshot, err := query.Read(ctx)
	if err != nil {
		return fmt.Errorf("read ProfilingRuns: %w", err)
	}
	if err := writeProfilingRuns(stdout, opts.output, operatorconsole.BuildProfilingRuns(snapshot)); err != nil {
		return fmt.Errorf("write ProfilingRuns: %w", err)
	}
	return nil
}

func executeProfilingRunShow(ctx context.Context, args []string, stdout, stderr io.Writer, deps dependencies) error {
	opts, err := parseProfilingRunShowOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	query, err := deps.newRunShowQuery(opts)
	if err != nil {
		return fmt.Errorf("construct Evidence Passport query: %w", err)
	}
	passport, err := query.Read(ctx)
	if err != nil {
		return fmt.Errorf("read Evidence Passport: %w", err)
	}
	if err := writeEvidencePassport(stdout, opts.output, passport); err != nil {
		return fmt.Errorf("write Evidence Passport: %w", err)
	}
	return nil
}

func writeUsage(writer io.Writer) error {
	if _, err := fmt.Fprintln(writer, "Usage:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer,
		"  kubectl chill inventory [--kubeconfig PATH] [--context NAME] [-o table|json]"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer,
		"  kubectl chill system    [--kubeconfig PATH] [--context NAME] [-o table|json]"); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(writer,
		"  kubectl chill runs      [-n NAMESPACE|-A] [--kubeconfig PATH] [--context NAME] [-o table|json]"); err != nil {
		return err
	}
	_, err := fmt.Fprintln(writer,
		"  kubectl chill runs show NAME [-n NAMESPACE] [--console-service NAME] [-o table|json]")
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

func newProfilingRunsQuery(opts profilingRunsOptions) (profilingRunsQuery, error) {
	kubeClient, defaultNamespace, err := newKubernetesAccess(opts.kubeconfig, opts.context)
	if err != nil {
		return nil, err
	}
	namespace := opts.namespace
	if opts.allNamespaces {
		namespace = ""
	} else if namespace == "" {
		namespace = defaultNamespace
	}
	query, err := operatorconsole.NewKubernetesProfilingRunsQuery(kubeClient, namespace)
	if err != nil {
		return nil, fmt.Errorf("construct read-through query: %w", err)
	}
	return query, nil
}
