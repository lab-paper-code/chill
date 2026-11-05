package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestArgumentsFailClosedWithoutEcho(t *testing.T) {
	valid := []string{"--kubeconfig", "/missing/secret-kubeconfig", "--context", "reviewed", "--namespace", "ns", "--system-name", "system", "--name", "request", "--uid", "uid"}
	cases := [][]string{nil, {"--endpoint=credential-canary"}, append(append([]string{}, valid...), "credential-canary"), append(append([]string{}, valid...), "--allow-file=credential-canary")}
	for i := 0; i < len(valid); i += 2 {
		args := append([]string{}, valid[:i]...)
		cases = append(cases, append(args, valid[i+2:]...))
	}
	for _, args := range cases {
		var out bytes.Buffer
		if code := run(context.Background(), args, &out); code == 0 {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
		var proof map[string]any
		if err := json.Unmarshal(out.Bytes(), &proof); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if proof["eligible"] != false || proof["apiVersion"] != "chill.dacs.io/pretrial-retirement.v1" || proof["error"] == "" {
			t.Fatalf("invalid refusal: %s", &out)
		}
		if strings.Contains(out.String(), "credential-canary") || strings.Contains(out.String(), "secret-kubeconfig") {
			t.Fatalf("echoed untrusted argument: %s", &out)
		}
	}
	var out bytes.Buffer
	if run(context.Background(), valid, &out) == 0 || !strings.Contains(out.String(), "load explicit kubeconfig/context failed") {
		t.Fatalf("missing kubeconfig did not fail closed: %s", &out)
	}
}
