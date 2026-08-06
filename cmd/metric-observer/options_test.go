package main

import (
	"strings"
	"testing"
	"time"
)

func TestOptionsValidateRequiredBindings(t *testing.T) {
	valid := options{
		startWaitTimeout: time.Minute, endWaitTimeout: time.Minute, maxEvents: 100,
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	valid.startWaitTimeout = 0
	if err := valid.validate(); err == nil || !strings.Contains(err.Error(), "supplied by the Job") {
		t.Fatalf("expected missing start wait timeout rejection, got %v", err)
	}
	valid.startWaitTimeout = time.Minute
	valid.endWaitTimeout = 0
	if err := valid.validate(); err == nil || !strings.Contains(err.Error(), "supplied by the Job") {
		t.Fatalf("expected missing end wait timeout rejection, got %v", err)
	}
	valid.endWaitTimeout = time.Minute
	valid.maxEvents = 0
	if err := valid.validate(); err == nil || !strings.Contains(err.Error(), "supplied by the Job") {
		t.Fatalf("expected missing event budget rejection, got %v", err)
	}
}
