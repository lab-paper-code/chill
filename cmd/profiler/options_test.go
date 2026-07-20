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
	}); err != nil {
		t.Fatal(err)
	}
	if err := opts.validate(); err != nil {
		t.Fatal(err)
	}
	if opts.systemName != "chill" || opts.watchNamespace != "chill-system" ||
		opts.metricsAddr != "0" || opts.probeAddr != ":8081" {
		t.Fatalf("unexpected bound options: %#v", opts)
	}
}

func TestOptionsValidateFactSourceContract(t *testing.T) {
	valid := options{
		systemName: "chill", watchNamespace: "chill-system", factSourceConfigMap: "chill-profiler-facts",
		executionFactsKey: "execution-facts.yaml", metricSourcesKey: "metric-sources.yaml",
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}

	for _, mutate := range []func(*options){
		func(options *options) { options.systemName = "" },
		func(options *options) { options.factSourceConfigMap = "" },
		func(options *options) { options.executionFactsKey = " " },
		func(options *options) { options.metricSourcesKey = options.executionFactsKey },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.validate(); err == nil {
			t.Fatal("expected invalid Profiler fact-source options rejection")
		}
	}
}
