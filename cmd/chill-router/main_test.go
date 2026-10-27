package main

import (
	"flag"
	"path/filepath"
	"testing"
)

func TestRouterRequiresPrivateAuthenticatedConfiguration(t *testing.T) {
	var config options
	flags := flag.NewFlagSet("router", flag.ContinueOnError)
	config.flags(flags)
	if err := config.validate(); err == nil {
		t.Fatal("missing TLS, identity and observation configuration accepted")
	}
	root := t.TempDir()
	config.cert = filepath.Join(root, "tls.crt")
	config.key = filepath.Join(root, "tls.key")
	config.ca = filepath.Join(root, "ca.pem")
	config.token = filepath.Join(root, "token")
	config.buffer = root
	config.control = "wss://control.example"
	config.podUID = "pod-uid"
	if err := config.validate(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"..", "a/b", "a\x00b", "a b"} {
		candidate := config
		candidate.podUID = id
		if err := candidate.validate(); err == nil {
			t.Errorf("unsafe Pod identity %q accepted", id)
		}
	}
	candidate := config
	candidate.control = ""
	if err := candidate.validate(); err == nil {
		t.Fatal("missing authenticated control origin accepted")
	}
	candidate = config
	candidate.buffer = "relative"
	if err := candidate.validate(); err == nil {
		t.Fatal("relative shared buffer accepted")
	}
}
