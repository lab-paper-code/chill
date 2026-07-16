package main

import (
	"strings"
	"testing"
	"time"
)

func TestOptionsValidateRequiredBindings(t *testing.T) {
	valid := options{intentDigest: "sha256:" + strings.Repeat("a", 64), startWaitTimeout: time.Minute}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	valid.intentDigest = "latest"
	if err := valid.validate(); err == nil {
		t.Fatal("expected invalid digest rejection")
	}
	valid.intentDigest = "sha256:" + strings.Repeat("a", 64)
	valid.startWaitTimeout = 0
	if err := valid.validate(); err == nil || !strings.Contains(err.Error(), "supplied by the Job") {
		t.Fatalf("expected missing start wait timeout rejection, got %v", err)
	}
}
