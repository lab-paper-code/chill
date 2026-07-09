package system

import (
	"testing"
	"time"
)

func TestOptionsDefaultAndValidate(t *testing.T) {
	options := Options{Namespace: " chill-system "}
	if err := options.DefaultAndValidate(); err != nil {
		t.Fatalf("DefaultAndValidate() error = %v", err)
	}

	if options.SystemName != DefaultSystemName {
		t.Fatalf("SystemName = %q, want %q", options.SystemName, DefaultSystemName)
	}
	if options.Namespace != "chill-system" {
		t.Fatalf("Namespace = %q, want chill-system", options.Namespace)
	}
	if options.OperatorDeploymentName != DefaultOperatorDeploymentName() {
		t.Fatalf("OperatorDeploymentName = %q, want %q", options.OperatorDeploymentName, DefaultOperatorDeploymentName())
	}
	if options.RefreshInterval != DefaultRefreshInterval {
		t.Fatalf("RefreshInterval = %s, want %s", options.RefreshInterval, DefaultRefreshInterval)
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
	if options.OperatorDeploymentName != DefaultOperatorDeploymentName() {
		t.Fatalf("OperatorDeploymentName = %q, want %q", options.OperatorDeploymentName, DefaultOperatorDeploymentName())
	}
}

func TestOptionsRequireNamespace(t *testing.T) {
	options := Options{}
	if err := options.DefaultAndValidate(); err == nil {
		t.Fatal("DefaultAndValidate() error = nil, want namespace error")
	}
}

func TestOptionsRejectNegativeRefreshInterval(t *testing.T) {
	options := Options{
		Namespace:       "chill-system",
		RefreshInterval: -1 * time.Second,
	}
	if err := options.DefaultAndValidate(); err == nil {
		t.Fatal("DefaultAndValidate() error = nil, want refresh interval error")
	}
}
