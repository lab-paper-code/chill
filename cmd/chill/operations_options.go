package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/pflag"
)

const (
	profilesCommand = "profiles"
	operationShow   = "show"
	operationSubmit = "submit"
	operationList   = "list"
	dryRunServer    = "server"
)

type operationOptions struct {
	readOptions
	namespace      string
	allNamespaces  bool
	name           string
	file           string
	dryRun         string
	consoleService string
	servingService string
	servingURL     string
	servingCAFile  string
	tokenFile      string
}

func parseOperationOptions(group, action string, args []string, stderr io.Writer) (operationOptions, error) {
	opts := operationOptions{}
	flags := pflag.NewFlagSet("kubectl chill "+group+" "+action, pflag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context override")
	flags.StringVarP(&opts.output, "output", "o", outputFormatTable, "output format: table or json")
	flags.StringVarP(&opts.namespace, "namespace", "n", "", "resource and Service namespace")
	if group == profilesCommand {
		flags.StringVar(&opts.servingService, "serving-service", "chill-serving-entrypoint", "scheduler owner Service name")
		flags.StringVar(&opts.servingURL, "serving-url", "", "HTTPS owner origin; default https://SERVICE.NAMESPACE.svc:8080")
		flags.StringVar(&opts.servingCAFile, "serving-ca-file", "", "owner TLS CA file; default system trust roots")
		flags.StringVar(&opts.tokenFile, "token-file", "",
			"owner caller bearer token file; default kubeconfig bearer credentials")
	} else {
		if action == operationList {
			flags.BoolVarP(&opts.allNamespaces, "all-namespaces", "A", false, "query all namespaces")
		} else {
			flags.StringVar(&opts.consoleService, "console-service", "chill-console", "CHILL Console Service name")
		}
		if action == operationSubmit {
			flags.StringVarP(&opts.file, "file", "f", "", "measurement task manifest file")
			flags.StringVar(&opts.dryRun, "dry-run", "none", "none or server; server validates without submitting")
		}
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return opts, flag.ErrHelp
		}
		return opts, fmt.Errorf("%w: %v", errUsage, err)
	}
	needsName := action == operationShow || action == "adopt"
	if (needsName && flags.NArg() != 1) || (!needsName && flags.NArg() != 0) {
		expectedNames := 0
		if needsName {
			expectedNames = 1
		}
		return opts, fmt.Errorf("%w: %s %s requires %d positional names", errUsage, group, action, expectedNames)
	}
	if needsName {
		opts.name = flags.Arg(0)
	}
	if opts.output != outputFormatTable && opts.output != outputFormatJSON {
		return opts, fmt.Errorf("%w: output must be table or json", errUsage)
	}
	if opts.allNamespaces && opts.namespace != "" {
		return opts, fmt.Errorf("%w: namespace and all-namespaces cannot be used together", errUsage)
	}
	if action == operationSubmit && opts.dryRun != "none" && opts.dryRun != dryRunServer {
		return opts, fmt.Errorf("%w: dry-run must be none or server", errUsage)
	}
	values := map[string]string{}
	if needsName {
		values["measurement task name"] = opts.name
	}
	if action == operationSubmit {
		values["manifest file"] = opts.file
	}
	if group == profilesCommand {
		values["serving Service"] = opts.servingService
	} else if action != operationList {
		values["Console Service"] = opts.consoleService
	}
	for label, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value ||
			(label == "measurement task name" && strings.ContainsAny(value, "/?#")) {
			return opts, fmt.Errorf("%w: invalid %s", errUsage, label)
		}
	}
	return opts, nil
}
