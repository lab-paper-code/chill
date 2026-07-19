package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/pflag"
)

var errUsage = errors.New("invalid command usage")

type readOptions struct {
	kubeconfig string
	context    string
	output     string
}

type inventoryOptions = readOptions
type systemOptions = readOptions

type profilingRunsOptions struct {
	readOptions
	namespace     string
	allNamespaces bool
}

type profilingRunShowOptions struct {
	readOptions
	namespace      string
	name           string
	consoleService string
}

func parseInventoryOptions(args []string, stderr io.Writer) (inventoryOptions, error) {
	return parseReadOptions("inventory", args, stderr)
}

func parseSystemOptions(args []string, stderr io.Writer) (systemOptions, error) {
	return parseReadOptions("system", args, stderr)
}

func parseProfilingRunsOptions(args []string, stderr io.Writer) (profilingRunsOptions, error) {
	opts := profilingRunsOptions{}
	flags := flag.NewFlagSet("kubectl chill runs", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context override")
	flags.StringVar(&opts.output, "o", "table", "output format: table or json")
	flags.StringVar(&opts.namespace, "namespace", "", "namespace to query")
	flags.StringVar(&opts.namespace, "n", "", "namespace to query")
	flags.BoolVar(&opts.allNamespaces, "all-namespaces", false, "query all namespaces")
	flags.BoolVar(&opts.allNamespaces, "A", false, "query all namespaces")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return profilingRunsOptions{}, flag.ErrHelp
		}
		return profilingRunsOptions{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 {
		return profilingRunsOptions{}, fmt.Errorf("%w: runs accepts no positional arguments", errUsage)
	}
	if opts.output != "table" && opts.output != "json" {
		return profilingRunsOptions{}, fmt.Errorf("%w: output must be table or json", errUsage)
	}
	if opts.allNamespaces && opts.namespace != "" {
		return profilingRunsOptions{}, fmt.Errorf("%w: namespace and all-namespaces cannot be used together", errUsage)
	}
	return opts, nil
}

func parseProfilingRunShowOptions(args []string, stderr io.Writer) (profilingRunShowOptions, error) {
	opts := profilingRunShowOptions{}
	flags := pflag.NewFlagSet("kubectl chill runs show", pflag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context override")
	flags.StringVarP(&opts.output, "output", "o", "table", "output format: table or json")
	flags.StringVarP(&opts.namespace, "namespace", "n", "", "ProfilingRun and Console Service namespace")
	flags.StringVar(&opts.consoleService, "console-service", "chill-console", "CHILL Console ClusterIP Service name")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return profilingRunShowOptions{}, flag.ErrHelp
		}
		return profilingRunShowOptions{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 1 {
		return profilingRunShowOptions{}, fmt.Errorf("%w: runs show requires exactly one ProfilingRun name", errUsage)
	}
	opts.name = flags.Arg(0)
	if opts.output != "table" && opts.output != "json" {
		return profilingRunShowOptions{}, fmt.Errorf("%w: output must be table or json", errUsage)
	}
	for label, value := range map[string]string{"ProfilingRun name": opts.name, "Console Service name": opts.consoleService} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return profilingRunShowOptions{}, fmt.Errorf("%w: %s is required without surrounding whitespace", errUsage, label)
		}
	}
	return opts, nil
}

func parseReadOptions(command string, args []string, stderr io.Writer) (readOptions, error) {
	opts := readOptions{}
	flags := flag.NewFlagSet("kubectl chill "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context override")
	flags.StringVar(&opts.output, "o", "table", "output format: table or json")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return readOptions{}, flag.ErrHelp
		}
		return readOptions{}, fmt.Errorf("%w: %v", errUsage, err)
	}
	if flags.NArg() != 0 {
		return readOptions{}, fmt.Errorf("%w: %s accepts no positional arguments", errUsage, command)
	}
	if opts.output != "table" && opts.output != "json" {
		return readOptions{}, fmt.Errorf("%w: output must be table or json", errUsage)
	}
	return opts, nil
}
