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

func TestEvaluationStoreConfiguration(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(map[bool]string{false: "unset", true: "partial"}[partial], func(t *testing.T) {
			t.Setenv("EVALUATION_STORE_ACCESS_KEY_ID", "")
			t.Setenv("EVALUATION_STORE_SECRET_ACCESS_KEY", "")
			var opts options
			flags := flag.NewFlagSet("chill-server", flag.ContinueOnError)
			opts.bindFlags(flags)
			if partial {
				if err := flags.Parse([]string{"--evaluation-store-name=evaluation"}); err != nil {
					t.Fatal(err)
				}
			}
			err := opts.validate()
			if !partial {
				if err != nil && strings.Contains(err.Error(), "evaluation store") {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("partial evaluation store configuration accepted")
			}
			for _, missing := range []string{
				"--evaluation-store-endpoint", "--evaluation-store-bucket", "--evaluation-store-region",
				"EVALUATION_STORE_ACCESS_KEY_ID", "EVALUATION_STORE_SECRET_ACCESS_KEY",
			} {
				if !strings.Contains(err.Error(), missing) {
					t.Fatalf("missing input %s not reported: %v", missing, err)
				}
			}
		})
	}
}

func TestTLSFilePathsAcceptEquivalentAbsolutePaths(t *testing.T) {
	var opts options
	flags := flag.NewFlagSet("chill-server", flag.ContinueOnError)
	opts.bindFlags(flags)
	if err := flags.Parse([]string{
		"--serving-namespace=serving", "--tls-cert=/tls/./cert.pem", "--tls-key=/tls/./key.pem",
		"--observation-directory=/observations",
		"--resource-max-samples=32", "--resource-max-containers=256",
		"--resource-max-thermal=256", "--resource-max-errors=256",
		"--resource-release=chill", "--resource-system=chill",
		"--resource-control-component=serving-entrypoint", "--resource-directory-interval=1m",
	}); err != nil {
		t.Fatal(err)
	}
	if err := opts.validate(); err != nil {
		t.Fatal(err)
	}
}
