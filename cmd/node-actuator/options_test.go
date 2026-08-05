package main

import (
	"strings"
	"testing"
	"time"
)

func TestOptionsRequireExactNodePodAndPolicyInputs(t *testing.T) {
	t.Parallel()
	valid := options{
		nodeName: "jetsonx", podUID: "pod-uid", watchNamespace: "chill-system",
		leaseNamespace: "chill-system", leaseDuration: 30, safetyValidFor: time.Minute,
		allowedNamespaces:    []string{"kube-system"},
		allowedPolicyDigests: []string{"sha256:" + strings.Repeat("a", 64)},
		metricsAddr:          "0", probeAddr: ":8081",
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*options){
		"missing Pod UID":   func(candidate *options) { candidate.podUID = "" },
		"missing namespace": func(candidate *options) { candidate.allowedNamespaces = nil },
		"bad policy":        func(candidate *options) { candidate.allowedPolicyDigests = []string{"bad"} },
		"duplicate policy": func(candidate *options) {
			candidate.allowedPolicyDigests = append(candidate.allowedPolicyDigests, candidate.allowedPolicyDigests[0])
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			mutate(&candidate)
			if err := candidate.validate(); err == nil {
				t.Fatalf("invalid options accepted: %#v", candidate)
			}
		})
	}
}
