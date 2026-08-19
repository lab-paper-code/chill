package main

import (
	"flag"
	"testing"
)

func validOptions() options {
	return options{
		bindAddress: ":8080", tempDir: "/tmp/evidence",
		systemName: "chill", namespace: "chill-system",
	}
}

func TestOptionsValidate(t *testing.T) {
	t.Parallel()
	if err := validOptions().validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for name, mutate := range map[string]func(*options){
		"blank bind address":  func(o *options) { o.bindAddress = " " },
		"relative temp dir":   func(o *options) { o.tempDir = "relative" },
		"padded temp dir":     func(o *options) { o.tempDir = " /tmp/evidence" },
		"missing system name": func(o *options) { o.systemName = "" },
		"padded system name":  func(o *options) { o.systemName = "chill " },
		"missing namespace":   func(o *options) { o.namespace = "" },
		"padded namespace":    func(o *options) { o.namespace = " chill-system" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := validOptions()
			mutate(&invalid)
			if err := invalid.validate(); err == nil {
				t.Fatalf("invalid options accepted: %#v", invalid)
			}
		})
	}
}

// The Evidence Store lives in the namespace the Console runs in, so the
// namespace defaults from the downward API rather than requiring a flag.
func TestNamespaceDefaultsToThePodNamespace(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "chill-system")
	opts := options{}
	flags := flag.NewFlagSet("console", flag.ContinueOnError)
	opts.bindFlags(flags)
	if err := flags.Parse([]string{"--system-name=chill"}); err != nil {
		t.Fatal(err)
	}
	if opts.namespace != "chill-system" {
		t.Fatalf("namespace = %q", opts.namespace)
	}
}
