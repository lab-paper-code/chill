package main

import "testing"

func TestOptionsValidate(t *testing.T) {
	t.Parallel()
	if err := (options{bindAddress: ":8080", tempDir: "/tmp/evidence"}).validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for _, invalid := range []options{
		{bindAddress: " ", tempDir: "/tmp/evidence"},
		{bindAddress: ":8080", tempDir: "relative"},
		{bindAddress: ":8080", tempDir: " /tmp/evidence"},
	} {
		if err := invalid.validate(); err == nil {
			t.Fatalf("invalid options accepted: %#v", invalid)
		}
	}
}
