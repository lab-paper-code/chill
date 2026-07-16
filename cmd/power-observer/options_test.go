package main

import (
	"strings"
	"testing"
)

func TestOptionsRequireCanonicalIntentDigest(t *testing.T) {
	valid := options{intentDigest: "sha256:" + strings.Repeat("a", 64)}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	valid.intentDigest = "latest"
	if err := valid.validate(); err == nil {
		t.Fatal("expected invalid digest rejection")
	}
}
