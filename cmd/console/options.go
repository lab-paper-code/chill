package main

import (
	"errors"
	"flag"
	"strings"
)

type options struct {
	bindAddress string
	tempDir     string
}

func (o *options) bindFlags(flags *flag.FlagSet) {
	flags.StringVar(&o.bindAddress, "bind-address", ":8080", "Address for the CHILL Console read API.")
	flags.StringVar(&o.tempDir, "evidence-temp-dir", "/var/run/chill/evidence", "Directory for bounded Evidence Store reads.")
}

func (o options) validate() error {
	if strings.TrimSpace(o.bindAddress) == "" || strings.TrimSpace(o.bindAddress) != o.bindAddress {
		return errors.New("bind address is required without surrounding whitespace")
	}
	if !strings.HasPrefix(o.tempDir, "/") || strings.TrimSpace(o.tempDir) != o.tempDir {
		return errors.New("evidence temporary directory must be an absolute path without surrounding whitespace")
	}
	return nil
}
