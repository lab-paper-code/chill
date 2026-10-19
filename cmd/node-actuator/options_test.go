package main

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
)

func TestOptionsRequireExactNodeAndPolicyInputs(t *testing.T) {
	t.Parallel()
	valid := options{
		nodeName: "jetsonx", watchNamespace: "chill-system",
		leaseNamespace: "chill-system", leaseDuration: 30,
		allowedPolicies: []nodeactuator.ActuationPolicySpec{{
			Name: "xavier-mode", DeviceClass: "jetson-xavier-nx-8g", PowerMode: "MODE_15W_6CORE",
			ActuatorKind:            nodeactuator.ActuatorKindJetsonNVPModel,
			SettlingDurationSeconds: 60, MaxActuationDurationSeconds: 900,
		}},
		jetsonCommand:    []string{"/host/usr/sbin/nvpmodel"},
		jetsonTimeout:    30 * time.Second,
		kubeAPIServer:    "https://kubernetes.example:6443",
		kubeAPITokenFile: kubeconfig.DefaultServiceAccountTokenFile,
		kubeAPICAFile:    kubeconfig.DefaultServiceAccountCAFile,
		metricsAddr:      "0", probeAddr: ":8081",
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*options){
		"missing node name": func(candidate *options) { candidate.nodeName = "" },
		"missing policy":    func(candidate *options) { candidate.allowedPolicies = nil },
		"bad policy": func(candidate *options) {
			candidate.allowedPolicies = append(policyListFlag(nil), candidate.allowedPolicies...)
			candidate.allowedPolicies[0].PowerMode = "fixed"
		},
		"missing adapter":   func(candidate *options) { candidate.jetsonCommand = nil },
		"missing API token": func(candidate *options) { candidate.kubeAPITokenFile = "" },
		"spaced API server": func(candidate *options) { candidate.kubeAPIServer = " https://kubernetes.example:6443" },
		"duplicate policy": func(candidate *options) {
			candidate.allowedPolicies = append(candidate.allowedPolicies, candidate.allowedPolicies[0])
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
