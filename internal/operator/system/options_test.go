package system

import (
	"testing"

	"github.com/lab-paper-code/chill/internal/component"
)

func TestOptionsDefaultAndValidate(t *testing.T) {
	options := Options{
		Namespace: " chill-system ",
	}
	if err := options.DefaultAndValidate(); err != nil {
		t.Fatalf("DefaultAndValidate() error = %v", err)
	}

	if options.SystemName != component.DefaultSystemName {
		t.Fatalf("SystemName = %q, want %q", options.SystemName, component.DefaultSystemName)
	}
	if options.Namespace != "chill-system" {
		t.Fatalf("Namespace = %q, want chill-system", options.Namespace)
	}
}

func TestOptionsSystemNameDoesNotChangeWorkloadDefaults(t *testing.T) {
	options := Options{
		SystemName: "custom-status",
		Namespace:  "chill-system",
	}
	if err := options.DefaultAndValidate(); err != nil {
		t.Fatalf("DefaultAndValidate() error = %v", err)
	}

	if options.SystemName != "custom-status" {
		t.Fatalf("SystemName = %q, want custom-status", options.SystemName)
	}
}

func TestOptionsRequireNamespace(t *testing.T) {
	options := Options{}
	if err := options.DefaultAndValidate(); err == nil {
		t.Fatal("DefaultAndValidate() error = nil, want namespace error")
	}
}
