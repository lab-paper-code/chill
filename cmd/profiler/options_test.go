package main

import (
	"flag"
	"testing"
)

func TestOptionsBindFlags(t *testing.T) {
	t.Setenv("POD_NAMESPACE", "chill-system")
	flags := flag.NewFlagSet("profiler", flag.ContinueOnError)
	opts := options{}
	opts.bindFlags(flags)
	if err := flags.Parse([]string{
		"--system-name=chill",
		"--fact-source-config-map=chill-profiler-facts",
		"--execution-facts-key=execution-facts.yaml",
		"--metric-sources-key=metric-sources.yaml",
		"--workload-observation-key=workload-observation.yaml",
		"--execution-policies-key=execution-policies.yaml",
		"--actuation-policies-key=actuation-policies.yaml",
		"--workload-observer-service-account=chill-profiler-workload-observer",
		"--transition-observer-image=registry/chill-profiler:test",
		"--transition-observer-image-pull-policy=IfNotPresent",
		"--transition-observer-node-name=observer-node",
		"--transition-observer-service-account=chill-profiler-transition-observer",
		"--transition-observer-active-deadline-overhead-seconds=180",
		"--transition-maximum-object-bytes=67108864",
	}); err != nil {
		t.Fatal(err)
	}
	if err := opts.validate(); err != nil {
		t.Fatal(err)
	}
	if opts.systemName != "chill" || opts.watchNamespace != "chill-system" ||
		opts.metricsAddr != "0" || opts.probeAddr != ":8081" ||
		opts.transitionObserverNodeName != "observer-node" ||
		opts.transitionObserverDeadlineOverheadSeconds != 180 ||
		opts.transitionMaximumObjectBytes != 67108864 {
		t.Fatalf("unexpected bound options: %#v", opts)
	}
}

func TestOptionsValidateFactSourceContract(t *testing.T) {
	valid := options{
		systemName: "chill", watchNamespace: "chill-system", factSourceConfigMap: "chill-profiler-facts",
		executionFactsKey: "execution-facts.yaml", metricSourcesKey: "metric-sources.yaml",
		workloadObservationKey:                    "workload-observation.yaml",
		executionPoliciesKey:                      "execution-policies.yaml",
		actuationPoliciesKey:                      "actuation-policies.yaml",
		workloadObserverServiceAccount:            "chill-profiler-workload-observer",
		transitionObserverImage:                   "registry/chill-profiler:test",
		transitionObserverPullPolicy:              "IfNotPresent",
		transitionObserverNodeName:                "observer-node",
		transitionObserverServiceAccount:          "chill-profiler-transition-observer",
		transitionObserverDeadlineOverheadSeconds: 180,
		transitionMaximumObjectBytes:              67108864,
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}

	for _, mutate := range []func(*options){
		func(options *options) { options.systemName = "" },
		func(options *options) { options.factSourceConfigMap = "" },
		func(options *options) { options.executionFactsKey = " " },
		func(options *options) { options.metricSourcesKey = options.executionFactsKey },
		func(options *options) { options.transitionObserverNodeName = "" },
		func(options *options) { options.transitionObserverDeadlineOverheadSeconds = 0 },
		func(options *options) { options.transitionMaximumObjectBytes = 0 },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.validate(); err == nil {
			t.Fatal("expected invalid Profiler fact-source options rejection")
		}
	}
}
