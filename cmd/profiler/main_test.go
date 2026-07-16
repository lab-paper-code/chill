package main

import "testing"

func TestOptionsValidateFactSourceContract(t *testing.T) {
	valid := options{
		watchNamespace: "chill-system", factSourceConfigMap: "chill-profiler-facts",
		executionFactsKey: "execution-facts.yaml", powerSourcesKey: "power-sources.yaml",
	}
	if err := valid.validate(); err != nil {
		t.Fatal(err)
	}

	for _, mutate := range []func(*options){
		func(options *options) { options.factSourceConfigMap = "" },
		func(options *options) { options.executionFactsKey = " " },
		func(options *options) { options.powerSourcesKey = options.executionFactsKey },
	} {
		candidate := valid
		mutate(&candidate)
		if err := candidate.validate(); err == nil {
			t.Fatal("expected invalid Profiler fact-source options rejection")
		}
	}
}
