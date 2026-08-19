package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

type options struct {
	bindAddress string
	tempDir     string
	systemName  string
	namespace   string
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.bindAddress, "bind-address", ":8080", "Address for the CHILL Console read API.")
	flags.StringVar(
		&o.tempDir, "evidence-temp-dir", "/var/run/chill/evidence",
		"Directory for bounded Evidence Store reads.",
	)
	flags.StringVar(&o.systemName, "system-name", "", "ChillSystem name that owns the Evidence Store.")
	flags.StringVar(
		&o.namespace, "namespace", os.Getenv("POD_NAMESPACE"),
		"Management namespace holding the Evidence Store operand.",
	)
}

func (o options) validate() error {
	if strings.TrimSpace(o.bindAddress) == "" || strings.TrimSpace(o.bindAddress) != o.bindAddress {
		return errors.New("bind address is required without surrounding whitespace")
	}
	if !strings.HasPrefix(o.tempDir, "/") || strings.TrimSpace(o.tempDir) != o.tempDir {
		return errors.New("evidence temporary directory must be an absolute path without surrounding whitespace")
	}
	for name, value := range map[string]string{
		"system name": o.systemName,
		"namespace":   o.namespace,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return fmt.Errorf("%s is required without surrounding whitespace", name)
		}
	}
	return nil
}
