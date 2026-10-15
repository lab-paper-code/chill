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
	profilesCommand  = "profiles"
	preflightCommand = "preflight"
	operationShow    = "show"
	operationSubmit  = "submit"
	operationList    = "list"
	operationDelete  = "delete"
	operationUnadopt = "unadopt"
	dryRunServer     = "server"
)

type operationOptions struct {
	readOptions
	namespace      string
	name           string
	file           string
	dryRun         string
	servingService string
	servingURL     string
	servingCAFile  string
	tokenFile      string
	ifMatch        string
	uid            string
}

func parseOperationOptions(group, action string, args []string, stderr io.Writer) (operationOptions, error) {
	opts := operationOptions{}
	flags := pflag.NewFlagSet("kubectl chill "+strings.TrimSpace(group+" "+action), pflag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.kubeconfig, "kubeconfig", "", "path to a kubeconfig file")
	flags.StringVar(&opts.context, "context", "", "kubeconfig context override")
	flags.StringVarP(&opts.output, "output", "o", outputFormatTable, "output format: table or json")
	flags.StringVarP(&opts.namespace, "namespace", "n", "", "resource and Service namespace")
	flags.StringVar(&opts.servingService, "serving-service", "chill-serving-entrypoint", "scheduler owner Service name")
	flags.StringVar(&opts.servingURL, "serving-url", "", "HTTPS owner origin; default https://SERVICE.NAMESPACE.svc:8080")
	flags.StringVar(&opts.servingCAFile, "serving-ca-file", "", "owner TLS CA file; default system trust roots")
	flags.StringVar(&opts.tokenFile, "token-file", "",
		"owner caller bearer token file; default kubeconfig bearer credentials")
	if action == operationDelete || action == operationUnadopt {
		flags.StringVar(&opts.ifMatch, "if-match", "", "required queried resourceVersion; profiles use configVersion")
	}
	if action == operationDelete {
		flags.StringVar(&opts.uid, "uid", "",
			"required queried measurement UID; deletion irreversibly removes owned evidence")
	}
	if action == operationSubmit || group == preflightCommand {
		flags.StringVarP(&opts.file, "file", "f", "", "measurement task manifest file")
	}
	if action == operationSubmit {
		flags.StringVar(&opts.dryRun, "dry-run", "none", "none or server; server validates without submitting")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return opts, flag.ErrHelp
		}
		return opts, fmt.Errorf("%w: %v", errUsage, err)
	}
	needsName := action == operationShow || action == "adopt" || action == operationUnadopt || action == operationDelete
	expectedNames := 0
	if needsName {
		expectedNames = 1
		opts.name = flags.Arg(0)
	}
	if flags.NArg() != expectedNames {
		return opts, fmt.Errorf("%w: %s %s requires %d positional names", errUsage, group, action, expectedNames)
	}
	if opts.output != outputFormatTable && opts.output != outputFormatJSON {
		return opts, fmt.Errorf("%w: output must be table or json", errUsage)
	}
	if action == operationSubmit && opts.dryRun != "none" && opts.dryRun != dryRunServer {
		return opts, fmt.Errorf("%w: dry-run must be none or server", errUsage)
	}
	values := map[string]string{}
	if needsName {
		values["measurement task name"] = opts.name
	}
	if action == operationSubmit || group == preflightCommand {
		values["manifest file"] = opts.file
	}
	values["serving Service"] = opts.servingService
	if action == operationDelete || action == operationUnadopt {
		values["resourceVersion"] = opts.ifMatch
	}
	if action == operationDelete {
		values["measurement UID"] = opts.uid
	}
	for label, value := range values {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value ||
			(label == "measurement task name" && strings.ContainsAny(value, "/?#")) {
			return opts, fmt.Errorf("%w: invalid %s", errUsage, label)
		}
	}
	return opts, nil
}
