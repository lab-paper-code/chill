package main

import (
	"testing"
	"time"

	"github.com/lab-paper-code/chill/internal/kubeconfig"
	"github.com/lab-paper-code/chill/internal/nodeactuator"
)

func TestNvidiaOptionsSelectExactlyOneMatchingAdapter(t *testing.T) {
	valid := options{nodeName: "genesis2", nvidiaCommand: []string{"/host/nvidia-smi"}, nvidiaTimeout: time.Second,
		nvidiaClocks: "1500,1200,900", metricsAddr: "0", probeAddr: ":8081",
		kubeAPITokenFile: kubeconfig.DefaultServiceAccountTokenFile, kubeAPICAFile: kubeconfig.DefaultServiceAccountCAFile,
		allowedPolicies: policyListFlag{{Name: "gpu-900", DeviceClass: "edge-server-rtx-2080-ti-64g", PowerMode: "GPU_900",
			ActuatorKind:            nodeactuator.ActuatorKindNvidiaLockedGPUClock,
			SettlingDurationSeconds: 10, MaxActuationDurationSeconds: 60}},
	}
	if err := valid.validateActuation(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*options){
		func(o *options) { o.jetsonCommand = []string{"nvpmodel"} },
		func(o *options) { o.nvidiaClocks = "900,N/A" },
		func(o *options) { o.nvidiaCommand = []string{" nvidia-smi"} },
		func(o *options) { o.nvidiaTimeout = 0 },
		func(o *options) {
			o.allowedPolicies = append(policyListFlag(nil), o.allowedPolicies...)
			o.allowedPolicies[0].ActuatorKind = nodeactuator.ActuatorKindJetsonNVPModel
		},
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.validateActuation(); err == nil {
			t.Fatalf("invalid NVIDIA options accepted: %+v", candidate)
		}
	}
}
