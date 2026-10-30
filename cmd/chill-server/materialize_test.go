package main

import (
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lab-paper-code/chill/internal/evidencestore"
)

type profileObjects struct {
	content string
	opens   int
}

func (o *profileObjects) Open(context.Context, evidencestore.ObjectIdentity) (io.ReadCloser, error) {
	o.opens++
	return io.NopCloser(strings.NewReader(o.content)), nil
}

func TestMaterializerExactBoundAndAtomicOutput(t *testing.T) {
	for _, content := range []string{"abc", "ab", "abcd"} {
		t.Run(content, func(t *testing.T) {
			output := filepath.Join(t.TempDir(), "bundle.json")
			if err := os.WriteFile(output, []byte("original"), 0600); err != nil {
				t.Fatal(err)
			}
			objects := &profileObjects{content: content}
			err := materializeObject(context.Background(), objects,
				evidencestore.ObjectIdentity{Store: "profiles", Key: "bundle", Size: 3}, output, 3)
			raw, readErr := os.ReadFile(output)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if content == "abc" {
				if err != nil || string(raw) != content {
					t.Fatalf("exact materialization=%q error=%v", raw, err)
				}
			} else if err == nil || string(raw) != "original" {
				t.Fatalf("incomplete object replaced startup file: %q error=%v", raw, err)
			}
			if objects.opens != 1 {
				t.Fatalf("opens=%d", objects.opens)
			}
			objects.opens = 0
			err = materializeObject(context.Background(), objects,
				evidencestore.ObjectIdentity{Store: "profiles", Key: "bundle", Size: 4}, output, 3)
			if err == nil || objects.opens != 0 {
				t.Fatal("over-limit profile read attempted")
			}
		})
	}
}

func TestMaterializerFlagsBypassChillServerRequirements(t *testing.T) {
	var opts options
	flags := flag.NewFlagSet("materialize", flag.ContinueOnError)
	opts.bindFlags(flags)
	output := filepath.Join(t.TempDir(), "bundle.json")
	if err := flags.Parse([]string{
		"--materialize-profile", "--store-name=profiles", "--store-endpoint=http://store:9000",
		"--store-bucket=profiles", "--store-region=region", "--profile-object-key=bundle",
		"--profile-object-size=3", "--profile-output=" + output,
		"--kube-api-qps=NaN", "--kube-api-burst=0",
	}); err != nil {
		t.Fatal(err)
	}
	if err := opts.validate(); err != nil {
		t.Fatalf("materializer wrongly requires TLS/Kubernetes: %v", err)
	}
	opts.profileObjectSize = opts.maxEvidenceBytes + 1
	if err := opts.validate(); err == nil {
		t.Fatal("over-limit materializer accepted")
	}
	opts.profileObjectSize = 3
	opts.materializeProfile = false
	if err := opts.validate(); err == nil {
		t.Fatal("normal ChillServer mode unexpectedly bypassed validation")
	}
}

func TestChillServerRejectsInvalidKubernetesBudget(t *testing.T) {
	for _, args := range [][]string{
		{"--kube-api-qps=0"}, {"--kube-api-qps=NaN"}, {"--kube-api-qps=+Inf"},
		{"--kube-api-qps=1e100"}, {"--kube-api-qps=1e-100"}, {"--kube-api-burst=0"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var opts options
			flags := flag.NewFlagSet("chill-server", flag.ContinueOnError)
			opts.bindFlags(flags)
			base := []string{
				"--serving-namespace=serving", "--tls-cert=/cert", "--tls-key=/key",
				"--observation-directory=/observations",
				"--resource-max-samples=32", "--resource-max-containers=256",
				"--resource-max-thermal=256", "--resource-max-errors=256",
				"--resource-release=chill", "--resource-system=chill",
				"--resource-control-component=serving-entrypoint", "--resource-directory-interval=1m",
			}
			if err := flags.Parse(append(base, args...)); err != nil {
				t.Fatal(err)
			}
			if err := opts.validate(); err == nil {
				t.Fatal("invalid Kubernetes client budget accepted")
			}
		})
	}
}

func TestChillServerKubernetesBudgetDefaults(t *testing.T) {
	var opts options
	flags := flag.NewFlagSet("chill-server", flag.ContinueOnError)
	opts.bindFlags(flags)
	if opts.kubeAPIQPS != 200 || opts.kubeAPIBurst != 400 {
		t.Fatalf("Kubernetes budget defaults: qps=%g burst=%d", opts.kubeAPIQPS, opts.kubeAPIBurst)
	}
	if err := flags.Parse([]string{"--kube-api-qps=75", "--kube-api-burst=150"}); err != nil {
		t.Fatal(err)
	}
	if opts.kubeAPIQPS != 75 || opts.kubeAPIBurst != 150 {
		t.Fatal("configured Kubernetes budget was not applied")
	}
}
