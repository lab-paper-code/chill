package main

import (
	"flag"
	"strings"
	"testing"
	"time"
)

func TestRequestTimeoutBounds(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, 130 * time.Second} {
		err := (options{requestTimeout: timeout}).validate()
		if err == nil || !strings.Contains(err.Error(), "request-timeout") {
			t.Fatalf("timeout %s: %v", timeout, err)
		}
	}
}

func TestServerHasNoSplitRouterFlags(t *testing.T) {
	var opts options
	flags := flag.NewFlagSet("chill-server", flag.ContinueOnError)
	opts.bindFlags(flags)
	for _, name := range []string{"router-count", "router-buffer-root", "gateway-enabled", "gateway-control"} {
		if flags.Lookup(name) != nil {
			t.Fatalf("unused split flag %q is still registered", name)
		}
	}
}
