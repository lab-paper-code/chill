package main

import (
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
