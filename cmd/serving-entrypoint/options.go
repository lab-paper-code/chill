package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
)

type options struct {
	bindAddress      string
	routesPath       string
	servingNamespace string
	servingPort      int
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.bindAddress, "bind-address", ":8080", "Address the inference entry point accepts requests on.")
	flags.StringVar(&o.routesPath, "routes", "", "Path to the routing table this entry point serves from.")
	flags.StringVar(&o.servingNamespace, "serving-namespace", "", "Namespace holding the per-node serving Services.")
	flags.IntVar(&o.servingPort, "serving-port", 0, "Port the per-node serving Services expose.")
}

func (o options) validate() error {
	if strings.TrimSpace(o.bindAddress) == "" || strings.TrimSpace(o.bindAddress) != o.bindAddress {
		return errors.New("bind address is required without surrounding whitespace")
	}
	if !strings.HasPrefix(o.routesPath, "/") || strings.TrimSpace(o.routesPath) != o.routesPath {
		return errors.New("routing table path must be absolute without surrounding whitespace")
	}
	if len(validation.IsDNS1123Label(o.servingNamespace)) != 0 {
		return fmt.Errorf("invalid serving namespace %q", o.servingNamespace)
	}
	if len(validation.IsValidPortNum(o.servingPort)) != 0 {
		return fmt.Errorf("invalid serving port %d", o.servingPort)
	}
	return nil
}
