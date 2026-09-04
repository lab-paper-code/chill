package main

import (
	"flag"
	"testing"
)

func TestOptionsBindAndValidateJobDeliveryContract(t *testing.T) {
	t.Setenv("POD_UID", "pod-uid")
	t.Setenv("NODE_NAME", "lattepanda")
	t.Setenv("AWS_ACCESS_KEY_ID", "access-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret-key")
	set := flag.NewFlagSet("measurement-worker", flag.ContinueOnError)
	options := options{}
	options.bindFlags(set)
	if err := set.Parse([]string{
		"--run-config={}",
		"--attempt-timeout=10m",
		"--observer-ready-timeout=1m",
		"--metric-result-timeout=1m",
		"--workload-result-timeout=1m",
		"--max-latency-samples=1000",
		"--max-input-bytes=1048576",
		"--max-runtime-result-bytes=1048576",
		"--max-metric-result-bytes=1048576",
		"--max-workload-result-bytes=1048576",
		"--store-name=chill-evidence-store",
		"--store-endpoint=http://chill-evidence-store.chill-system.svc:9000",
		"--store-bucket=chill-profiling-evidence",
		"--store-region=us-east-1",
		"--raw-object-key=profiling-runs/run/raw/result.json",
	}); err != nil {
		t.Fatal(err)
	}
	if err := options.validate(); err != nil {
		t.Fatal(err)
	}
	if options.runtimeResultPath() != "/workspace/runtime-result.json" || options.podUID != "pod-uid" ||
		options.scheduledNodeName != "lattepanda" {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestOptionsRejectInvalidDeliveryContract(t *testing.T) {
	valid := validOptions(t)
	tests := []func(*options){
		func(options *options) { options.podUID = "" },
		func(options *options) { options.scheduledNodeName = "" },
		func(options *options) { options.storeName = " store " },
		func(options *options) { options.workspacePath = "workspace" },
		func(options *options) { options.attemptTimeout = 0 },
		func(options *options) { options.metricResultTimeout = options.attemptTimeout },
		func(options *options) { options.workloadResultTimeout = options.attemptTimeout },
		func(options *options) { options.maxInputBytes = 0 },
		func(options *options) { options.rawObjectKey = "" },
	}
	for index, mutate := range tests {
		candidate := valid
		mutate(&candidate)
		if err := candidate.validate(); err == nil {
			t.Fatalf("invalid delivery contract %d accepted", index)
		}
	}
}
